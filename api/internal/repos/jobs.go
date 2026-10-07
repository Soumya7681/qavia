package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/sourceprovider"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	"github.com/hyscaler/qavia/api/internal/workspace"
)

// The repository sync stage (BE-6.1, BE-6.2, BE-6.3).
//
// It clones, measures, reads the manifests, and throws the checkout away. Nothing
// downstream holds a workspace open: a comprehension pass or a coverage run clones
// again, because a shared checkout is a checkout one job can change under another.
//
// The order is the security property:
//
//  1. the URL is checked against the project's allowlist and the private-address
//     rules, because a worker cloning a URL a user supplied is an SSRF primitive
//     (BE-4.7);
//  2. the token is read from the settings store at the moment of use and passed to
//     git in its environment, never in argv and never into a container (BE-6.1.3);
//  3. the workspace is measured after the clone and the job is refused if the
//     repository is over the limit, because git has no byte budget (BE-6.2.3);
//  4. the directory is removed on every path out, including failure.

// TypeSync is the stage name the pipeline declares.
const TypeSync = jobs.TypeRepoSync

// Fetcher materialises source into a workspace. Satisfied by the git provider and by
// the archive one, so a project with no clone URL syncs from its uploaded zip.
type Fetcher interface {
	Fetch(ctx context.Context, req sourceprovider.Request, root string) (sourceprovider.Result, error)
}

// TokenFetcher builds a fetcher per job with the project's own credential.
//
// It exists so no long-lived object holds a decrypted token: the secret is read at
// the moment of the clone, handed to one subprocess, and dropped when Fetch returns
// (BE-6.1.3).
type TokenFetcher struct {
	Repos *Service

	// New builds the underlying fetcher for one token.
	New func(token string) Fetcher
}

func (t TokenFetcher) Fetch(
	ctx context.Context,
	req sourceprovider.Request,
	root string,
) (sourceprovider.Result, error) {
	token, err := t.Repos.Token(ctx, req.ProjectID)
	if err != nil {
		return sourceprovider.Result{}, err
	}
	return t.New(token).Fetch(ctx, req, root)
}

// Targets is the allowlist check. The same one a run's target goes through: a
// repository URL is a URL a user supplied.
type TargetCheck interface {
	CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (host string, err error)
}

// Deps is everything the stage shares.
type Deps struct {
	// Repos carries the clone path itself, configured with WithCloner: the checks and
	// their order live in one place, because two copies of "allowlist, token, clone,
	// quota" would eventually be two different orders.
	Repos *Service
}

// SyncHandler clones a project's repository and reads what it is.
type SyncHandler struct {
	deps Deps
}

func NewSyncHandler(deps Deps) *SyncHandler { return &SyncHandler{deps: deps} }

func (h *SyncHandler) Type() string { return TypeSync }

// Payload names the project to sync.
type Payload struct {
	ProjectID uuid.UUID `json:"projectId"`

	// Ref overrides the connection's default branch, for a sync of a feature branch.
	Ref string `json:"ref,omitempty"`

	// RequestID is generated once per request and is what the idempotency key is built
	// from. See SyncIdempotencyKey for why the project alone is the wrong key.
	RequestID uuid.UUID `json:"requestId"`
}

// IdempotencyKey is the request, not the project.
func (h *SyncHandler) IdempotencyKey(payload Payload) string {
	return SyncIdempotencyKey(payload)
}

// SyncIdempotencyKey is the same rule, exported so the API can declare the type as
// enqueue-only without building a handler it cannot run.
//
// Keyed on a per-request nonce rather than on the project, because a sync is work
// somebody asks for repeatedly: fetching the latest commit is the entire point.
// Keying on the project would mean the second sync of a repository finds the first
// one's row and pushes nothing, which is the same trap the analyse chain fell into.
// A redelivered task carries the same nonce and is still deduplicated, which is what
// idempotency is actually for.
func SyncIdempotencyKey(payload Payload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("repo:%s:%s", payload.ProjectID, payload.Ref)
	}
	return fmt.Sprintf("repo:%s", payload.RequestID)
}

func (h *SyncHandler) Handle(ctx context.Context, payload Payload, jc jobs.JobContext) error {
	connection, found, err := h.deps.Repos.Get(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		return apierr.NoRepositoryConnected()
	}

	stack, commit, err := h.materialise(ctx, connection, payload.Ref, jc)
	if err != nil {
		var domain *apierr.Error
		if errors.As(err, &domain) {
			// An expected failure — the branch is wrong, the token is rejected, the
			// checkout is too large, the stack is unknown. Its message goes on the
			// connection, which is the one place a project page reads why the repository
			// it looks connected to does not work, and the job then succeeds at what it
			// was asked to do. Returning the error as well would surface the same reason a
			// second time as a failed job and retry a sync that will fail identically,
			// burying the message under a retry count — the same reason the execute stage
			// records a failed run on its row and returns nil (see runs/jobs.go).
			if recordErr := h.deps.Repos.RecordFetchError(ctx, payload.ProjectID, domain.Message); recordErr != nil {
				slog.WarnContext(ctx, "record the fetch error",
					"project_id", payload.ProjectID, "error", recordErr)
			}
			return nil
		}

		// A non-domain error is an incident, not something a user fixes on the project
		// page: it keeps its incident id and is surfaced through the job and retried,
		// rather than being written onto the connection as if the repository were at
		// fault.
		return err
	}

	if err := h.deps.Repos.RecordFetch(ctx, payload.ProjectID, commit); err != nil {
		return err
	}
	if err := h.deps.Repos.RecordDetection(ctx, payload.ProjectID, stack); err != nil {
		return err
	}

	if stack.Known() {
		jc.Event("Detected %s at %s", stack.Summary(), short(commit))
	} else {
		// The inspected list is the point of an unknown result: an operator can see what
		// was read and set an override, rather than reading a guess (BE-6.3.3).
		jc.Event("Could not identify the stack at %s. Files read: %v. "+
			"Set repo.stack_override for this project to say what it is.",
			short(commit), stack.Inspected)
	}

	jc.Progress(100)
	return nil
}

// materialise clones and detects, then throws the checkout away: a sync stores what
// it learned, not the files it learned it from.
func (h *SyncHandler) materialise(
	ctx context.Context,
	connection Connection,
	ref string,
	jc jobs.JobContext,
) (Stack, string, error) {
	if connection.Provider == ProviderArchive {
		jc.Event("Expanding the uploaded archive")
	} else {
		jc.Event("Cloning %s", connection.URL)
	}

	checkout, err := h.deps.Repos.Materialise(ctx, connection.ProjectID, ref)
	if err != nil {
		return Stack{}, "", err
	}
	defer func() {
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "remove the workspace",
				"project_id", connection.ProjectID, "error", err)
		}
	}()

	jc.Progress(70)
	return checkout.Stack, checkout.Commit, nil
}

// Checkout is a materialised repository, owned by the caller.
//
// It carries the workspace rather than a path, because reading anything inside it has
// to go through the path validation the workspace owns (BE-6.2.2). Close removes it.
type Checkout struct {
	Space  *workspace.Workspace
	Commit string
	Stack  Stack
}

// Close removes the checkout.
func (c Checkout) Close() error {
	if c.Space == nil {
		return nil
	}
	return c.Space.Close()
}

// Materialise clones a project's repository into a fresh workspace.
//
// One implementation for the two stages that need source — the sync that detects a
// stack and the exploration that maps the code — because each needs its own checkout
// and both need the same checks in the same order: allowlist, then token, then clone,
// then quota. Two copies of that order would eventually be two different orders.
//
// The caller closes it, on every path out.
func (s *Service) Materialise(
	ctx context.Context,
	projectID uuid.UUID,
	ref string,
) (Checkout, error) {
	if s.cloner.Fetchers == nil {
		// A process with no fetchers cannot clone. Said plainly rather than failing
		// somewhere deeper: the API process is deliberately in this state.
		return Checkout{}, apierr.NotImplemented("cloning on this process")
	}

	connection, found, err := s.Get(ctx, projectID)
	if err != nil {
		return Checkout{}, err
	}
	if !found {
		return Checkout{}, apierr.NoRepositoryConnected()
	}

	options, err := s.CloneOptionsFor(ctx, projectID)
	if err != nil {
		return Checkout{}, err
	}

	if connection.Provider != ProviderArchive && s.cloner.Target != nil {
		// The same allowlist and address rules a run's target goes through: a worker
		// cloning a URL a user supplied is an SSRF primitive (BE-4.7).
		if _, err := s.cloner.Target.CheckURL(ctx, projectID, connection.URL); err != nil {
			return Checkout{}, err
		}
	}

	fetcher, known := s.cloner.Fetchers[connection.Provider]
	if !known || fetcher == nil {
		return Checkout{}, apierr.NotImplemented(
			fmt.Sprintf("%s as a repository provider on this worker", connection.Provider))
	}

	space, err := workspace.New(projectID.String(), workspace.Options{
		Parent:     s.cloner.Root,
		QuotaBytes: options.Quota,
	})
	if err != nil {
		return Checkout{}, apierr.Internal(err)
	}

	// Every failure past this point removes the directory it created, because a
	// half-cloned workspace nobody owns is a leak (BE-6.2.1).
	discard := func(cause error) (Checkout, error) {
		if closeErr := space.Close(); closeErr != nil {
			slog.WarnContext(ctx, "remove a failed workspace",
				"project_id", projectID, "error", closeErr)
		}
		return Checkout{}, cause
	}

	if ref == "" {
		ref = connection.DefaultBranch
	}

	request := sourceprovider.Request{
		ProjectID: projectID,
		URL:       connection.URL,
		Ref:       ref,
	}
	if connection.Provider == ProviderArchive {
		archive, hasArchive, err := s.LatestArchive(ctx, projectID)
		if err != nil {
			return discard(err)
		}
		if !hasArchive {
			return discard(apierr.Validation(
				"This project is set to use an uploaded archive, and none has been uploaded yet.",
				nil))
		}
		request.StorageKey = archive.StorageKey
		request.Filename = archive.Filename
	}

	result, err := fetcher.Fetch(ctx, request, space.Root())
	if err != nil {
		return discard(apierr.RepositoryUnreachable(err))
	}
	if err := space.EnsureWithinQuota(); err != nil {
		return discard(apierr.RepositoryTooLarge(err.Error()))
	}

	stack, err := Detect(space.Root())
	if err != nil {
		return discard(apierr.Internal(fmt.Errorf("read the repository manifests: %w", err)))
	}

	return Checkout{Space: space, Commit: result.Ref, Stack: stack}, nil
}

// MapInput is a comprehension map being stored.
type MapInput struct {
	ProjectID uuid.UUID
	Commit    string
	Stack     string

	// Document is the map. Typed as any so this package does not depend on the
	// comprehension package, which depends on it.
	Document any

	Steps    int
	CutShort bool

	ModelName string
	JobID     *uuid.UUID
}

// StoreMap writes one exploration's result.
func (s *Service) StoreMap(ctx context.Context, input MapInput) (uuid.UUID, error) {
	document, err := json.Marshal(input.Document)
	if err != nil {
		return uuid.Nil, apierr.Internal(fmt.Errorf("encode the repository map: %w", err))
	}

	row, err := s.db.Queries().CreateRepoMap(ctx, dbgen.CreateRepoMapParams{
		ProjectID: input.ProjectID,
		CommitSha: input.Commit,
		Stack:     input.Stack,
		Document:  document,
		Steps:     int32(input.Steps), //nolint:gosec // Bounded by the exploration budget.
		CutShort:  input.CutShort,
		ModelName: input.ModelName,
		JobID:     input.JobID,
	})
	if err != nil {
		return uuid.Nil, apierr.Internal(fmt.Errorf("store the repository map: %w", err))
	}
	return row.ID, nil
}

// StoredMap is a map as it was stored.
type StoredMap struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	Commit    string
	Stack     string

	// Document is the raw map, handed to the API's mapper without this package
	// learning its shape.
	Document json.RawMessage

	Steps     int
	CutShort  bool
	ModelName string
	CreatedAt time.Time
}

// LatestMap is the newest map for a project.
func (s *Service) LatestMap(ctx context.Context, projectID uuid.UUID) (StoredMap, bool, error) {
	row, err := s.db.Queries().LatestRepoMap(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StoredMap{}, false, nil
		}
		return StoredMap{}, false, apierr.Internal(fmt.Errorf("read the repository map: %w", err))
	}

	return StoredMap{
		ID:        row.ID,
		ProjectID: row.ProjectID,
		Commit:    row.CommitSha,
		Stack:     row.Stack,
		Document:  row.Document,
		Steps:     int(row.Steps),
		CutShort:  row.CutShort,
		ModelName: row.ModelName,
		CreatedAt: row.CreatedAt,
	}, true, nil
}

// RecordDetection stores what detection found.
func (s *Service) RecordDetection(ctx context.Context, projectID uuid.UUID, stack Stack) error {
	encoded, err := json.Marshal(stack)
	if err != nil {
		return apierr.Internal(fmt.Errorf("encode the detected stack: %w", err))
	}

	if err := s.db.Queries().RecordRepoDetection(ctx, dbgen.RecordRepoDetectionParams{
		ProjectID: projectID, Detected: encoded,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("store the detected stack: %w", err))
	}
	return nil
}

// DetectedStack reads back what the last sync found, plus any override.
//
// The override wins, and it is applied here rather than at each call site so every
// consumer sees the same answer (BE-6.3.2).
func (s *Service) DetectedStack(ctx context.Context, projectID uuid.UUID) (Stack, bool, error) {
	connection, found, err := s.Get(ctx, projectID)
	if err != nil || !found {
		return Stack{}, false, err
	}

	stack := connection.Detected
	if override, err := s.stackOverride(ctx, projectID); err != nil {
		return Stack{}, false, err
	} else if override != "" {
		stack.Framework = override
		stack.Confident = true
		stack.Inspected = append(stack.Inspected, "settings: repo.stack_override")
	}

	return stack, connection.DetectedAt != nil || stack.Framework != "", nil
}

func short(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "an unknown revision"
	}
	return commit
}
