package artifacts

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Projects is the slice of the projects service this package needs. Membership
// and archived-state rules live there, and reaching past them into the projects
// table would skip both (backend-standards.md 3).
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int64(ctx context.Context, key string, target settings.Target) (int64, error)
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	StringList(ctx context.Context, key string, target settings.Target) ([]string, error)
}

// Service owns uploaded inputs and their versions.
type Service struct {
	db       *store.DB
	store    objectstore.Store
	projects Projects
	settings Settings
	recorder *audit.Recorder
}

func NewService(
	db *store.DB,
	objects objectstore.Store,
	projectsService Projects,
	settingsService Settings,
	recorder *audit.Recorder,
) *Service {
	return &Service{
		db:       db,
		store:    objects,
		projects: projectsService,
		settings: settingsService,
		recorder: recorder,
	}
}

// UploadInput is one incoming file.
type UploadInput struct {
	ProjectID uuid.UUID
	Kind      Kind
	Filename  string
	Body      io.Reader
}

// signedURLTTL bounds a redirect to the object store. Long enough to start a slow
// download, short enough that a URL copied out of a browser's history is useless.
const signedURLTTL = 10 * time.Minute

// Upload stores one file and records it.
//
// The order of the checks is the security property, not an implementation detail:
// the size cap bounds what is read at all, the sniffed content type is checked
// against the allowlist rather than the extension a client claimed, and an archive
// is proved not to be a bomb before anything is written to the object store.
//
// The body is spooled to a temporary file rather than held in memory, and the
// SHA-256 is computed during that single pass. Hashing first is what makes
// deduplication free: an identical re-upload is recognised before a byte reaches
// the object store, so no generation is re-run (FR-1.4).
func (s *Service) Upload(ctx context.Context, actor httpx.Principal, input UploadInput) (Upload, error) {
	if !input.Kind.Valid() {
		return Upload{}, apierr.Validation(
			fmt.Sprintf("%q is not a kind of input this platform accepts.", input.Kind),
			map[string]any{"field": "kind"})
	}
	if err := s.projects.EnsureActive(ctx, input.ProjectID); err != nil {
		return Upload{}, err
	}

	global := settings.Target{}
	maxBytes, err := s.settings.Int64(ctx, "uploads.max_bytes", global)
	if err != nil {
		return Upload{}, err
	}
	allowedTypes, err := s.settings.StringList(ctx, "uploads.allowed_types", global)
	if err != nil {
		return Upload{}, err
	}
	maxRatio, err := s.settings.Int(ctx, "uploads.max_archive_ratio", global)
	if err != nil {
		return Upload{}, err
	}

	spooled, err := spool(ctx, input.Body, maxBytes)
	if err != nil {
		return Upload{}, err
	}
	defer spooled.discard()

	if !slices.Contains(allowedTypes, spooled.contentType) &&
		!slices.Contains(allowedTypes, baseType(spooled.contentType)) {
		return Upload{}, apierr.UnsupportedMediaType(spooled.contentType, allowedTypes)
	}

	if input.Kind.IsArchive() || isCompressed(spooled.contentType) {
		if err := checkArchive(spooled, int64(maxRatio), maxBytes); err != nil {
			return Upload{}, err
		}
	}

	// Deduplication before storage. The unique index on (project_id, sha256) is the
	// real guarantee; this read is what turns a conflict into a useful answer rather
	// than an error the client has to interpret.
	if existing, found, err := s.byHash(ctx, input.ProjectID, spooled.sum); err != nil {
		return Upload{}, err
	} else if found {
		return Upload{Artifact: existing, Deduplicated: true}, nil
	}

	filename := objectstore.SanitizeFilename(input.Filename)
	version, lineageID, err := s.nextVersion(ctx, input.ProjectID, input.Kind, filename)
	if err != nil {
		return Upload{}, err
	}

	artifactID := uuid.New()
	key := objectstore.Key(input.ProjectID, string(input.Kind), artifactID, filename)

	if _, err := s.store.Put(ctx, key, spooled.reader(), objectstore.PutOptions{
		ContentType: spooled.contentType,
		Size:        spooled.size,
	}); err != nil {
		return Upload{}, apierr.StorageUnreachable(err)
	}

	row, err := s.db.Queries().CreateArtifact(ctx, dbgen.CreateArtifactParams{
		ID:          artifactID,
		ProjectID:   input.ProjectID,
		Kind:        string(input.Kind),
		Filename:    filename,
		StorageKey:  key,
		ContentType: spooled.contentType,
		SizeBytes:   spooled.size,
		Sha256:      spooled.sum,
		Version:     int32(version),
		LineageID:   lineageID,
		UploadedBy:  &actor.UserID,
	})
	if err != nil {
		// The object is already written, so it is removed rather than left
		// orphaned. A concurrent identical upload is the expected cause: the unique
		// index caught it, and the winner's row is returned.
		s.discardObject(ctx, key)

		if store.IsUniqueViolation(err) {
			if existing, found, lookupErr := s.byHash(ctx, input.ProjectID, spooled.sum); lookupErr == nil && found {
				return Upload{Artifact: existing, Deduplicated: true}, nil
			}
		}
		return Upload{}, fmt.Errorf("create artifact: %w", err)
	}

	artifact := toArtifact(row)
	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionArtifactUploaded,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    artifact.Filename,
		ProjectID:  &artifact.ProjectID,
		Detail: map[string]any{
			"kind": string(artifact.Kind), "version": artifact.Version,
			"sizeBytes": artifact.SizeBytes, "sha256": artifact.Hex(),
		},
	})
	return Upload{Artifact: artifact}, nil
}

// List returns a page of a project's inputs, newest first.
func (s *Service) List(
	ctx context.Context,
	actor httpx.Principal,
	projectID uuid.UUID,
	kind Kind,
	limit int,
	cursor string,
) (Page, error) {
	if err := s.projects.EnsureMember(ctx, actor, projectID); err != nil {
		return Page{}, err
	}

	limit = paging.ClampLimit(limit)
	params := dbgen.ListArtifactsParams{ProjectID: projectID, PageSize: int32(limit + 1)}
	if kind != "" {
		value := string(kind)
		params.Kind = &value
	}
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &at
	}

	rows, err := s.db.Queries().ListArtifacts(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list artifacts: %w", err)
	}

	page := Page{Items: make([]Artifact, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = paging.EncodeTime(rows[i-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toArtifact(row))
	}
	return page, nil
}

// Get loads one artifact the caller may see.
func (s *Service) Get(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Artifact, error) {
	artifact, err := s.load(ctx, id)
	if err != nil {
		return Artifact{}, err
	}
	if err := s.projects.EnsureMember(ctx, actor, artifact.ProjectID); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

// Versions returns every version of one logical input, newest first.
func (s *Service) Versions(ctx context.Context, actor httpx.Principal, id uuid.UUID) ([]Artifact, error) {
	artifact, err := s.Get(ctx, actor, id)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Queries().ListArtifactVersions(ctx, artifact.LineageID)
	if err != nil {
		return nil, fmt.Errorf("list artifact versions: %w", err)
	}

	out := make([]Artifact, 0, len(rows))
	for _, row := range rows {
		out = append(out, toArtifact(row))
	}
	return out, nil
}

// Download opens the stored bytes, or hands back a signed URL where the driver
// supports one.
//
// A driver that can sign gets the download out of this process entirely, which
// matters once artifacts are run traces and videos rather than specifications.
func (s *Service) Download(
	ctx context.Context,
	actor httpx.Principal,
	id uuid.UUID,
) (artifact Artifact, signedURL string, body io.ReadCloser, err error) {
	artifact, err = s.Get(ctx, actor, id)
	if err != nil {
		return Artifact{}, "", nil, err
	}

	signedURL, err = s.store.SignedURL(ctx, artifact.StorageKey, signedURLTTL)
	switch {
	case err == nil:
		return artifact, signedURL, nil, nil
	case !errors.Is(err, objectstore.ErrSignedURLUnsupported):
		return Artifact{}, "", nil, apierr.StorageUnreachable(err)
	}

	body, err = s.store.Get(ctx, artifact.StorageKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			return Artifact{}, "", nil, apierr.ArtifactNotFound(id)
		}
		return Artifact{}, "", nil, apierr.StorageUnreachable(err)
	}
	return artifact, "", body, nil
}

// Delete removes the row and the stored object.
//
// The object goes first. A stored file with no row is invisible and only wastes
// space; a row with no file is a download that fails at the worst moment, so the
// order is chosen to fail the harmless way.
func (s *Service) Delete(ctx context.Context, actor httpx.Principal, id uuid.UUID) error {
	artifact, err := s.Get(ctx, actor, id)
	if err != nil {
		return err
	}
	if err := s.projects.EnsureActive(ctx, artifact.ProjectID); err != nil {
		return err
	}

	if err := s.store.Delete(ctx, artifact.StorageKey); err != nil {
		return apierr.StorageUnreachable(err)
	}
	if _, err := s.db.Queries().DeleteArtifact(ctx, id); err != nil {
		return fmt.Errorf("delete artifact %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionArtifactDeleted,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    artifact.Filename,
		ProjectID:  &artifact.ProjectID,
	})
	return nil
}

// StorageKeyFor exposes where an artifact's bytes live, for the job pipeline,
// which reads them without going through a request.
func (s *Service) StorageKeyFor(ctx context.Context, id uuid.UUID) (Artifact, error) {
	return s.load(ctx, id)
}

func (s *Service) load(ctx context.Context, id uuid.UUID) (Artifact, error) {
	row, err := s.db.Queries().GetArtifact(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Artifact{}, apierr.ArtifactNotFound(id)
		}
		return Artifact{}, fmt.Errorf("load artifact %s: %w", id, err)
	}
	return toArtifact(row), nil
}

func (s *Service) byHash(ctx context.Context, projectID uuid.UUID, sum []byte) (Artifact, bool, error) {
	row, err := s.db.Queries().GetArtifactByHash(ctx, dbgen.GetArtifactByHashParams{
		ProjectID: projectID, Sha256: sum,
	})
	if err != nil {
		if store.IsNotFound(err) {
			return Artifact{}, false, nil
		}
		return Artifact{}, false, fmt.Errorf("look up artifact by hash: %w", err)
	}
	return toArtifact(row), true, nil
}

// nextVersion places a new file in the history of the same logical input.
//
// Same project, kind, and filename means the same logical input, so a re-uploaded
// specification becomes version 2 and version 1 stays. That history is what the
// maintenance module diffs against, and retrofitting it later would be a rewrite.
func (s *Service) nextVersion(
	ctx context.Context,
	projectID uuid.UUID,
	kind Kind,
	filename string,
) (int, uuid.UUID, error) {
	row, err := s.db.Queries().FindArtifactLineage(ctx, dbgen.FindArtifactLineageParams{
		ProjectID: projectID, Kind: string(kind), Filename: filename,
	})
	if err != nil {
		if store.IsNotFound(err) {
			return 1, uuid.New(), nil
		}
		return 0, uuid.Nil, fmt.Errorf("find artifact lineage: %w", err)
	}
	return int(row.LatestVersion) + 1, row.LineageID, nil
}

// discardObject removes a stored object after the row it belonged to failed.
func (s *Service) discardObject(ctx context.Context, key string) {
	if err := s.store.Delete(context.WithoutCancel(ctx), key); err != nil {
		// Nothing to return this to: the caller is already reporting the failure
		// that caused it. The retention job clears it later.
		slog.ErrorContext(ctx, "remove orphaned object", "key", key, "error", err)
	}
}

// ------------------------------------------------------------------ upload spool

// spooled is an upload that has been read once: hashed, sniffed, and on disk.
type spooled struct {
	file        *os.File
	size        int64
	sum         []byte
	contentType string
}

func (s *spooled) reader() io.Reader { return s.file }

// discard removes the spool file. The upload either succeeded, in which case
// these bytes are already in the object store, or failed, in which case the
// caller is reporting that, so a failure here is logged rather than returned.
func (s *spooled) discard() {
	removeTemp(s.file)
}

// removeTemp closes and deletes a temporary file.
func removeTemp(file *os.File) {
	name := file.Name()
	if err := file.Close(); err != nil {
		slog.Warn("close temporary upload file", "path", name, "error", err)
	}
	if err := os.Remove(name); err != nil {
		slog.Warn("remove temporary upload file", "path", name, "error", err)
	}
}

// spool reads the body once, to a temporary file, hashing as it goes.
//
// Never buffered whole in memory: a 2 GB archive must not become 2 GB of heap on a
// single-VM deployment (NFR-9). The limit is enforced by reading one byte past it,
// so an oversized upload is refused rather than silently truncated.
func spool(ctx context.Context, body io.Reader, maxBytes int64) (*spooled, error) {
	file, err := os.CreateTemp("", "qavia-artifact-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	cleanup := func() { removeTemp(file) }

	digest := sha256.New()
	sniff := &sniffer{}

	written, err := io.Copy(
		io.MultiWriter(file, digest, sniff),
		io.LimitReader(&cancellable{ctx: ctx, inner: body}, maxBytes+1),
	)
	if err != nil {
		cleanup()
		return nil, apierr.UploadCorrupt(err.Error())
	}
	if written > maxBytes {
		cleanup()
		return nil, apierr.UploadTooLarge(maxBytes)
	}
	if written == 0 {
		cleanup()
		return nil, apierr.UploadCorrupt("the file is empty")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, fmt.Errorf("rewind upload: %w", err)
	}

	return &spooled{
		file:        file,
		size:        written,
		sum:         digest.Sum(nil),
		contentType: sniff.contentType(),
	}, nil
}

// sniffer keeps the first bytes so the content type comes from the content.
//
// The declared type on a multipart part is a claim by the client, and an
// allowlist checked against a claim is not an allowlist.
type sniffer struct {
	head []byte
}

func (s *sniffer) Write(p []byte) (int, error) {
	if remaining := 512 - len(s.head); remaining > 0 {
		s.head = append(s.head, p[:min(remaining, len(p))]...)
	}
	return len(p), nil
}

func (s *sniffer) contentType() string {
	return http.DetectContentType(s.head)
}

// baseType strips parameters, so "text/plain; charset=utf-8" matches an allowlist
// entry of "text/plain".
func baseType(contentType string) string {
	base, _, _ := strings.Cut(contentType, ";")
	return strings.TrimSpace(base)
}

func isCompressed(contentType string) bool {
	switch baseType(contentType) {
	case "application/zip", "application/gzip", "application/x-gzip", "application/x-tar":
		return true
	default:
		return false
	}
}

// checkArchive refuses a compression bomb before anything is stored.
//
// Two cheap checks rather than a trial extraction. A zip declares its uncompressed
// sizes in the central directory, so the ratio is arithmetic. A gzip declares
// nothing trustworthy, so it is decompressed into nothing, bounded by the
// allowance: the work is capped whatever the archive claims.
func checkArchive(upload *spooled, maxRatio, maxBytes int64) error {
	defer func() {
		// Every reader below leaves the file part-read, and the caller streams it
		// to the object store next.
		if _, err := upload.file.Seek(0, io.SeekStart); err != nil {
			slog.Warn("rewind upload after archive check", "error", err)
		}
	}()

	allowance := upload.size * maxRatio
	if allowance > maxBytes*maxRatio {
		allowance = maxBytes * maxRatio
	}

	switch baseType(upload.contentType) {
	case "application/zip":
		reader, err := zip.NewReader(upload.file, upload.size)
		if err != nil {
			return apierr.UploadCorrupt("it is not a readable zip archive")
		}

		var total uint64
		for _, entry := range reader.File {
			total += entry.UncompressedSize64
			if total > uint64(allowance) {
				return bombRejected(maxRatio)
			}
		}
		return nil

	case "application/gzip", "application/x-gzip":
		gz, err := gzip.NewReader(upload.file)
		if err != nil {
			return apierr.UploadCorrupt("it is not a readable gzip archive")
		}
		defer func() {
			if err := gz.Close(); err != nil {
				slog.Warn("close gzip reader", "error", err)
			}
		}()

		expanded, err := io.Copy(io.Discard, io.LimitReader(gz, allowance+1))
		if err != nil {
			return apierr.UploadCorrupt("the archive could not be decompressed")
		}
		if expanded > allowance {
			return bombRejected(maxRatio)
		}
		return nil

	default:
		// A plain tar is not compressed, so there is no ratio to abuse; its entry
		// count and total size are bounded at extraction time instead.
		return nil
	}
}

func bombRejected(maxRatio int64) error {
	return apierr.ArchiveRejected(fmt.Sprintf(
		"it expands by more than %d times its own size, which is how a compression bomb behaves",
		maxRatio))
}

// cancellable makes a long copy honour cancellation, which io.Copy cannot do on
// its own: an aborted upload would otherwise keep writing until the client's bytes
// ran out.
type cancellable struct {
	ctx   context.Context
	inner io.Reader
}

func (c *cancellable) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.inner.Read(p)
}
