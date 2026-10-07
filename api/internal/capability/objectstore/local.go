package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalID is the built-in driver's ID.
const LocalID = "local-disk"

// Local stores objects on the filesystem.
//
// It is the built-in and the default, and it requires zero configuration: a fresh
// install with an empty settings table stores and retrieves files here. That is
// what keeps MinIO and S3 genuinely optional rather than nominally optional
// (requirements.md 5.4).
type Local struct {
	root string
}

// NewLocal resolves the root and creates it.
//
// The root is resolved to an absolute path once, at construction, because every
// later containment check compares against it. Resolving per call would let a
// symlink swapped in underneath change what "inside the root" means.
func NewLocal(root string) (*Local, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("objectstore: local storage path is empty")
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local storage path %q: %w", root, err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("create local storage path %q: %w", absolute, err)
	}

	// EvalSymlinks after the directory exists, so the root itself being a symlink
	// does not make every containment check fail.
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve local storage path %q: %w", absolute, err)
	}

	return &Local{root: resolved}, nil
}

func (l *Local) ID() string { return LocalID }

// Available reports whether the root is still a writable directory.
//
// Cheap enough to call on the readiness path: it stats the root rather than
// writing. The write-and-read-back check is Probe, which setup runs once.
func (l *Local) Available(_ context.Context) bool {
	info, err := os.Stat(l.root)
	if err != nil || !info.IsDir() {
		return false
	}
	return writable(info.Mode().Perm())
}

func writable(mode os.FileMode) bool { return mode&0o200 != 0 }

// Root is where objects land. Exposed for the readiness detail line, which names
// the directory an operator has to fix.
func (l *Local) Root() string { return l.root }

// Put streams r to the key's path.
//
// The write goes to a temporary file in the same directory and is renamed into
// place, so a crash or a cancelled upload leaves no half-written object that a
// later read would treat as complete.
func (l *Local) Put(ctx context.Context, key string, r io.Reader, opts PutOptions) (Object, error) {
	target, err := l.path(key)
	if err != nil {
		return Object{}, err
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return Object{}, fmt.Errorf("create directory for %q: %w", key, err)
	}

	temp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return Object{}, fmt.Errorf("create temporary file for %q: %w", key, err)
	}
	tempName := temp.Name()

	written, copyErr := io.Copy(temp, contextReader(ctx, r))
	if copyErr == nil {
		// Durable before the rename, so a power loss cannot leave a named object
		// pointing at unwritten data.
		copyErr = temp.Sync()
	}
	closeErr := temp.Close()

	if copyErr != nil || closeErr != nil {
		discardTemp(tempName)
		return Object{}, fmt.Errorf("write %q: %w", key, errors.Join(copyErr, closeErr))
	}

	if err := os.Chmod(tempName, 0o640); err != nil {
		discardTemp(tempName)
		return Object{}, fmt.Errorf("set permissions on %q: %w", key, err)
	}
	if err := os.Rename(tempName, target); err != nil {
		discardTemp(tempName)
		return Object{}, fmt.Errorf("publish %q: %w", key, err)
	}

	return Object{
		Key:         key,
		Size:        written,
		ContentType: opts.ContentType,
		ModTime:     time.Now().UTC(),
	}, nil
}

func (l *Local) Get(_ context.Context, key string) (io.ReadCloser, error) {
	target, err := l.path(key)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, fmt.Errorf("open %q: %w", key, err)
	}
	return file, nil
}

func (l *Local) Stat(_ context.Context, key string) (Object, error) {
	target, err := l.path(key)
	if err != nil {
		return Object{}, err
	}

	info, err := os.Stat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return Object{}, fmt.Errorf("stat %q: %w", key, err)
	}
	return Object{Key: key, Size: info.Size(), ModTime: info.ModTime().UTC()}, nil
}

// Delete removes the object. A key that is already gone is a success: delete is
// called from retention and from job retries, and both must be safe to run twice.
func (l *Local) Delete(_ context.Context, key string) error {
	target, err := l.path(key)
	if err != nil {
		return err
	}

	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete %q: %w", key, err)
	}

	// Prune the now-empty artifact directory so a project with heavy churn does not
	// accumulate thousands of empty directories. A non-empty parent errors, and that
	// is the stop condition rather than a problem.
	if err := os.Remove(filepath.Dir(target)); err != nil && !errors.Is(err, os.ErrExist) {
		slog.Debug("leave non-empty artifact directory", "key", key)
	}
	return nil
}

// discardTemp removes a half-written upload. The write it belonged to has already
// failed and the name is unique, so a failure here only leaves a file for the next
// sweep.
func discardTemp(name string) {
	if err := os.Remove(name); err != nil {
		slog.Warn("remove temporary object file", "path", name, "error", err)
	}
}

// SignedURL is not available on local disk, so the API streams the bytes itself.
func (l *Local) SignedURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return "", ErrSignedURLUnsupported
}

// path validates the key and confirms the resolved location is inside the root.
//
// Both checks are needed. ValidateKey rejects a traversal spelled in the key;
// this rejects one smuggled in through a symlink that was already on disk, by
// resolving the deepest existing ancestor and comparing against the root.
func (l *Local) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	target := filepath.Join(l.root, filepath.FromSlash(key))

	// The target itself usually does not exist yet on a write, so the check walks
	// up to the nearest ancestor that does and resolves that.
	ancestor := target
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if !l.contains(resolved) {
				return "", fmt.Errorf("objectstore: key %q resolves outside the storage root", key)
			}
			return target, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("resolve path for %q: %w", key, err)
		}

		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("objectstore: key %q has no resolvable parent", key)
		}
		ancestor = parent
	}
}

func (l *Local) contains(resolved string) bool {
	if resolved == l.root {
		return true
	}
	return strings.HasPrefix(resolved, l.root+string(os.PathSeparator))
}

// contextReader makes a long copy cancellable.
//
// io.Copy does not take a context, so an aborted upload would otherwise keep
// writing until the client's bytes ran out.
func contextReader(ctx context.Context, r io.Reader) io.Reader {
	return &cancellableReader{ctx: ctx, inner: r}
}

type cancellableReader struct {
	ctx   context.Context
	inner io.Reader
}

func (c *cancellableReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.inner.Read(p)
}
