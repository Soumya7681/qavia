// Package workspace owns the per-job directories source code is materialised into
// (BE-6.2).
//
// It exists because of one specific threat: from this phase on, an agent asks for
// files by path, and the agent's input is a repository it was pointed at. A path is
// therefore untrusted data even when it looks like `src/app/controller.ts`, and the
// two ways it goes wrong are traversal (`../../../etc/passwd`) and symlinks (a link
// inside the repository pointing at `/etc`).
//
// The rule the package enforces:
//
//	clean the path, join it to the root, resolve every symlink on it, and confirm
//	the result is still inside the root — then open it.
//
// A prefix check on an uncleaned path is not a control, and neither is a prefix check
// after cleaning but before resolving: `repo/link/passwd` cleans to itself and sits
// inside the root, and `link` is a symlink to `/etc` (BE-6.2.2).
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideWorkspace is returned for any path that resolves outside the root. It is
// its own error so a caller can tell a refusal from a missing file: one is a bug or
// an attack, the other is a normal answer to a question about a repository.
var ErrOutsideWorkspace = errors.New("workspace: path resolves outside the workspace")

// Workspace is one job's directory.
//
// The root is resolved once, at creation, so every later comparison is between two
// fully resolved paths. Comparing a resolved candidate against an unresolved root
// would fail on any machine whose temporary directory is itself a symlink, which is
// every macOS machine.
type Workspace struct {
	root string

	// quotaBytes bounds what may accumulate. A runaway clone must not fill the host
	// (BE-6.2.3).
	quotaBytes int64
}

// Options configure a workspace.
type Options struct {
	// Parent is where job directories are created. Empty means the OS temporary
	// directory, which is correct for a developer and wrong for a runner host, so
	// deployments set it.
	Parent string

	// QuotaBytes is the ceiling for everything inside. Zero means the default.
	QuotaBytes int64
}

// defaultQuota is what one repository may occupy. Two gigabytes is a large
// repository and a small disk problem; past it, something is wrong with the clone
// rather than with the limit.
const defaultQuota = 2 << 30

// New creates a fresh workspace directory.
//
// The caller closes it, including on failure. That is not politeness: a job that
// leaves its clone behind turns a disk into a slow leak, and a workspace shared
// between jobs is a workspace one job can read out of another.
func New(prefix string, options Options) (*Workspace, error) {
	quota := options.QuotaBytes
	if quota <= 0 {
		quota = defaultQuota
	}

	parent := options.Parent
	if parent != "" {
		if err := os.MkdirAll(parent, 0o750); err != nil {
			return nil, fmt.Errorf("create the workspace parent: %w", err)
		}
	}

	dir, err := os.MkdirTemp(parent, "qavia-"+sanitisePrefix(prefix)+"-")
	if err != nil {
		return nil, fmt.Errorf("create the workspace: %w", err)
	}

	// Resolved now, so every later check compares like with like.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve the workspace: %w", err)
	}

	return &Workspace{root: resolved, quotaBytes: quota}, nil
}

// Root is the absolute directory. Handed to a clone or an extraction, never to a
// container: a container gets files copied in, not a host path mounted (BE-4.9).
func (w *Workspace) Root() string { return w.root }

// Close removes the directory and everything in it.
func (w *Workspace) Close() error {
	if w.root == "" {
		return nil
	}
	if err := os.RemoveAll(w.root); err != nil {
		return fmt.Errorf("remove the workspace: %w", err)
	}
	return nil
}

// Resolve turns a repository-relative path into an absolute one, or refuses it.
//
// The order is the whole point:
//
//  1. reject an absolute path outright, because a caller asking for `/etc/passwd` is
//     not asking about this repository;
//  2. clean it, which collapses `..` textually;
//  3. join it to the resolved root;
//  4. resolve symlinks on the result, which is what catches a link inside the
//     repository pointing outside it;
//  5. confirm the resolved result is still under the root.
//
// A path that does not exist yet is resolved as far as it can be, so this works for
// a file about to be written as well as one about to be read.
func (w *Workspace) Resolve(relative string) (string, error) {
	if relative == "" {
		return w.root, nil
	}
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("%w: %q is absolute", ErrOutsideWorkspace, relative)
	}
	// A Windows-style drive or UNC path is absolute on the machine that wrote it and
	// merely odd here; either way it is not a repository-relative path.
	if strings.ContainsRune(relative, ':') || strings.HasPrefix(relative, `\\`) {
		return "", fmt.Errorf("%w: %q is not a repository-relative path", ErrOutsideWorkspace, relative)
	}

	cleaned := filepath.Clean(relative)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q climbs out", ErrOutsideWorkspace, relative)
	}

	candidate := filepath.Join(w.root, cleaned)

	resolved, err := resolveExisting(candidate)
	if err != nil {
		return "", err
	}
	if !within(w.root, resolved) {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrOutsideWorkspace, relative, resolved)
	}

	return candidate, nil
}

// Open reads a file from the workspace after checking its path.
func (w *Workspace) Open(relative string) (*os.File, error) {
	absolute, err := w.Resolve(relative)
	if err != nil {
		return nil, err
	}

	// The file itself is checked too: Resolve proved the path is inside, and this
	// proves the thing at the end of it is a regular file. A named pipe would block
	// a reader forever, and a device would be worse.
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("workspace: %q is not a regular file", relative)
	}

	return os.Open(absolute) //nolint:gosec // Resolve proved the path is inside the workspace.
}

// Usage totals what the workspace currently holds.
//
// Walked rather than tracked, because the things that write into a workspace are a
// clone and an extraction, and neither reports its own size honestly enough to bill
// against a quota.
func (w *Workspace) Usage() (int64, error) {
	var total int64

	err := filepath.WalkDir(w.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure the workspace: %w", err)
	}
	return total, nil
}

// EnsureWithinQuota fails when the workspace has outgrown its ceiling.
//
// Checked after a clone rather than during it, because git does not offer a byte
// budget: the honest control is to let it write, measure, and refuse the job before
// anything reads the result.
func (w *Workspace) EnsureWithinQuota() error {
	used, err := w.Usage()
	if err != nil {
		return err
	}
	if used > w.quotaBytes {
		return fmt.Errorf(
			"workspace: the source is %d MiB and the limit is %d MiB",
			used>>20, w.quotaBytes>>20)
	}
	return nil
}

// resolveExisting resolves symlinks on the longest existing prefix of a path.
//
// A path that does not exist yet cannot be resolved whole, and refusing every such
// path would make this unusable for writes. Resolving the deepest existing ancestor
// is the check that matters anyway: a symlink can only be a component that exists.
func resolveExisting(candidate string) (string, error) {
	remainder := ""
	current := candidate

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			if remainder == "" {
				return resolved, nil
			}
			return filepath.Join(resolved, remainder), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %q: %w", candidate, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Walked to the filesystem root without finding anything that exists. The
			// candidate is unusable, and treating it as fine would be the wrong way to
			// fail.
			return "", fmt.Errorf("resolve %q: no existing ancestor", candidate)
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// within reports whether path is the root or inside it.
//
// The separator matters: without it `/tmp/qavia-1` would count as inside
// `/tmp/qavia-12`, which is exactly the kind of near-miss a prefix check gets wrong.
func within(root, path string) bool {
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// sanitisePrefix keeps a job or project ID usable as a directory name without
// trusting it to be one.
func sanitisePrefix(prefix string) string {
	var out strings.Builder
	for _, character := range prefix {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-':
			out.WriteRune(character)
		default:
			out.WriteRune('-')
		}
		if out.Len() >= 32 {
			break
		}
	}
	if out.Len() == 0 {
		return "job"
	}
	return out.String()
}
