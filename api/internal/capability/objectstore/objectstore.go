package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Get and Stat for a key that is not stored. Callers
// map it to a domain error; it never travels to a client as a driver error.
var ErrNotFound = errors.New("objectstore: object not found")

// ErrSignedURLUnsupported is returned by a driver that cannot hand out a
// time-limited URL. Local disk cannot, so the API streams the bytes itself
// instead. It is a capability difference, not a failure.
var ErrSignedURLUnsupported = errors.New("objectstore: this driver cannot sign URLs")

// Object is what a driver knows about a stored key.
type Object struct {
	Key         string
	Size        int64
	ContentType string
	ModTime     time.Time

	// ETag is the driver's own version identifier where it has one. The platform
	// identifies content by the SHA-256 it computes while streaming, so nothing
	// depends on this.
	ETag string
}

// PutOptions carries what a driver needs alongside the bytes.
type PutOptions struct {
	// ContentType is the sniffed type, never what a client claimed.
	ContentType string

	// Size is the expected length, or 0 when it is not known ahead of the stream.
	// A driver must not require it: an upload is streamed, never buffered whole
	// (BE-0.26).
	Size int64
}

// Store is the ObjectStore capability (F-13.6).
//
// Three drivers implement it: local disk, MinIO, and S3. Local disk is the
// built-in and needs zero configuration, which is what makes object storage an
// optional integration rather than a prerequisite (requirements.md 5.4).
type Store interface {
	ID() string
	Available(ctx context.Context) bool

	// Put streams r to key. It returns what was stored, so a caller can trust the
	// size the driver actually wrote rather than the one it was told.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) (Object, error)

	// Get opens key for reading. The caller closes it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (Object, error)

	// SignedURL returns a time-limited URL, or ErrSignedURLUnsupported.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Key builds a storage key.
//
// The convention is projects/{projectID}/{kind}/{artifactID}/{filename}, and it is
// built here rather than at each call site so nothing has to remember it. A
// client-supplied path is never trusted: only the base name of filename survives,
// and anything left that could escape is replaced (backend-standards.md 13).
func Key(projectID uuid.UUID, kind string, artifactID uuid.UUID, filename string) string {
	return path.Join(
		"projects", projectID.String(),
		SanitizeSegment(kind),
		artifactID.String(),
		SanitizeFilename(filename),
	)
}

// SanitizeFilename reduces a client-supplied name to something safe to join.
//
// It keeps the visible name useful in a download while making traversal
// impossible: a name is a single path segment or it is replaced.
func SanitizeFilename(name string) string {
	// Both separators, because a Windows client sends backslashes and the base of
	// "..\\..\\etc\\passwd" is the whole string on Linux.
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(path.Clean("/" + name))

	name = SanitizeSegment(name)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}

// SanitizeSegment strips anything that is not safe in one path segment.
func SanitizeSegment(segment string) string {
	var b strings.Builder
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ValidateKey rejects a key that could escape the storage root.
//
// Every driver calls it before touching anything. Agents read untrusted content
// and can be induced to name any path, so a prefix check on an uncleaned path is
// not a control (backend-standards.md 13).
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errors.New("objectstore: empty key")
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("objectstore: key %q must be relative", key)
	case strings.Contains(key, "\\"):
		return fmt.Errorf("objectstore: key %q contains a backslash", key)
	case strings.ContainsRune(key, 0):
		return fmt.Errorf("objectstore: key %q contains a null byte", key)
	}

	if cleaned := path.Clean(key); cleaned != key {
		return fmt.Errorf("objectstore: key %q is not in canonical form (%q)", key, cleaned)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("objectstore: key %q has an unsafe segment %q", key, segment)
		}
	}
	return nil
}

// probePayload is small, fixed, and recognisable in a bucket listing.
var probePayload = []byte("qavia storage probe\n")

// Probe writes an object, reads it back, compares it, and deletes it.
//
// Setup refuses to complete until this passes (BE-0.28). It is generic rather than
// a driver method on purpose: a driver that implemented its own probe could
// implement it wrongly, and "the credentials are accepted" is not the same claim
// as "a file written here can be read back".
func Probe(ctx context.Context, store Store) error {
	key := path.Join("probes", uuid.NewString())

	if _, err := store.Put(ctx, key, bytes.NewReader(probePayload), PutOptions{
		ContentType: "text/plain; charset=utf-8",
		Size:        int64(len(probePayload)),
	}); err != nil {
		return fmt.Errorf("write probe object: %w", err)
	}

	// Deleted whatever happens next, so a failed read does not leave litter behind.
	// The probe object is disposable and the caller already has the verdict, so a
	// failed cleanup is logged rather than reported over the probe's own result.
	defer func() {
		if err := store.Delete(context.WithoutCancel(ctx), key); err != nil {
			slog.WarnContext(ctx, "remove storage probe object", "key", key, "error", err)
		}
	}()

	reader, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read probe object back: %w", err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			slog.WarnContext(ctx, "close storage probe object", "key", key, "error", err)
		}
	}()

	got, err := io.ReadAll(io.LimitReader(reader, int64(len(probePayload))+1))
	if err != nil {
		return fmt.Errorf("read probe body: %w", err)
	}
	if !bytes.Equal(got, probePayload) {
		return fmt.Errorf("probe object read back as %d bytes, expected %d",
			len(got), len(probePayload))
	}
	return nil
}
