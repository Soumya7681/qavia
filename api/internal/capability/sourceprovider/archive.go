package sourceprovider

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// ArchiveID is the built-in provider's ID.
const ArchiveID = "archive-upload"

// Limits bound what an archive may expand to.
//
// All three are needed, because they catch different attacks. A ratio catches the
// classic zip bomb, where a few kilobytes expand to gigabytes. A total size
// catches a merely enormous archive that is honest about it. An entry count
// catches the millions-of-empty-files variant, where no single file is large and
// no ratio looks wrong, but the extraction exhausts inodes.
type Limits struct {
	MaxRatio   int64
	MaxBytes   int64
	MaxEntries int
}

func (l Limits) withDefaults() Limits {
	if l.MaxRatio <= 0 {
		l.MaxRatio = 100
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = 2 << 30
	}
	if l.MaxEntries <= 0 {
		l.MaxEntries = 50_000
	}
	return l
}

// Archive is the built-in SourceProvider: a zip, tar, or tar.gz that was
// uploaded.
//
// It needs no configuration and no external platform, which is what makes GitHub
// and GitLab genuinely optional rather than nominally optional
// (requirements.md 5.4).
type Archive struct {
	store  Opener
	limits Limits
}

func NewArchive(store Opener, limits Limits) *Archive {
	return &Archive{store: store, limits: limits.withDefaults()}
}

func (a *Archive) ID() string { return ArchiveID }

// Available is unconditionally true: the object store is always present, because
// its own built-in is local disk.
func (a *Archive) Available(_ context.Context) bool { return true }

// Fetch downloads the uploaded archive and extracts it into the workspace.
func (a *Archive) Fetch(ctx context.Context, req Request, workspace string) (Result, error) {
	if req.StorageKey == "" {
		return Result{}, apierr.Validation(
			"This project has no uploaded archive to work from. Upload one first.", nil)
	}

	body, err := a.store.Get(ctx, req.StorageKey)
	if err != nil {
		return Result{}, fmt.Errorf("open archive %s: %w", req.StorageKey, err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			slog.WarnContext(ctx, "close archive body", "key", req.StorageKey, "error", err)
		}
	}()

	return Extract(ctx, body, req.Filename, workspace, a.limits)
}

// Extract unpacks an archive into root, refusing anything that would write
// outside it.
//
// The checks are not a formality. An archive is untrusted input from a user, and
// in later phases from a model: an entry named ../../etc/passwd, a symlink
// pointing at /, and a 10,000:1 compression ratio are all things this has to stop,
// and each is rejected with a message that says which one it was.
func Extract(
	ctx context.Context,
	source io.Reader,
	filename string,
	root string,
	limits Limits,
) (Result, error) {
	limits = limits.withDefaults()

	if err := os.MkdirAll(root, 0o750); err != nil {
		return Result{}, fmt.Errorf("create workspace %s: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve workspace %s: %w", root, err)
	}

	// The archive is spooled to disk first. Both readers need either random access
	// (zip) or a rewind after format sniffing (tar), and buffering an upload in
	// memory is exactly what BE-0.26 forbids.
	spool, compressed, err := spoolToDisk(ctx, source, limits.MaxBytes)
	if err != nil {
		return Result{}, err
	}
	defer discardSpool(spool)

	extractor := &extractor{
		root:       resolvedRoot,
		limits:     limits,
		compressed: compressed,
	}

	switch detectFormat(filename, spool) {
	case formatZip:
		err = extractor.zip(ctx, spool, compressed)
	case formatTarGz:
		err = extractor.tarGz(ctx, spool)
	case formatTar:
		err = extractor.tar(ctx, spool)
	default:
		return Result{}, apierr.ArchiveRejected(
			"it is not a zip, tar, or tar.gz archive")
	}
	if err != nil {
		return Result{}, err
	}

	return Result{Root: resolvedRoot, Files: extractor.entries, Bytes: extractor.written}, nil
}

type format int

const (
	formatUnknown format = iota
	formatZip
	formatTar
	formatTarGz
)

// detectFormat sniffs the magic bytes, and falls back to the filename only where
// the magic is inconclusive.
//
// Content first, extension second: an extension is a claim by whoever uploaded
// the file, and the checks below have to apply to what the bytes actually are.
func detectFormat(filename string, file *os.File) format {
	header := make([]byte, 512)
	n, err := file.ReadAt(header, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return formatUnknown
	}
	header = header[:n]

	switch {
	case len(header) >= 4 && header[0] == 'P' && header[1] == 'K' &&
		(header[2] == 3 || header[2] == 5 || header[2] == 7):
		return formatZip
	case len(header) >= 2 && header[0] == 0x1f && header[1] == 0x8b:
		return formatTarGz
	case len(header) >= 265 && string(header[257:262]) == "ustar":
		return formatTar
	}

	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return formatZip
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return formatTarGz
	case strings.HasSuffix(lower, ".tar"):
		return formatTar
	default:
		return formatUnknown
	}
}

type extractor struct {
	root       string
	limits     Limits
	compressed int64

	entries int
	written int64
}

func (e *extractor) zip(ctx context.Context, file *os.File, size int64) error {
	reader, err := zip.NewReader(file, size)
	if err != nil {
		return apierr.ArchiveRejected("it is not a readable zip archive")
	}

	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}

		info := entry.FileInfo()
		switch {
		case info.IsDir():
			if _, err := e.target(entry.Name); err != nil {
				return err
			}
			continue
		case !info.Mode().IsRegular():
			// Symlinks, devices, and sockets. A symlink is the standard way to make
			// a later write land outside the root, and nothing in a test suite needs
			// one, so the whole class is refused rather than resolved.
			return apierr.ArchiveRejected(
				fmt.Sprintf("it contains %q, which is not a regular file", entry.Name))
		}

		if err := e.admit(entry.Name, int64(entry.UncompressedSize64)); err != nil {
			return err
		}

		target, err := e.target(entry.Name)
		if err != nil {
			return err
		}

		body, err := entry.Open()
		if err != nil {
			return fmt.Errorf("read archive entry %q: %w", entry.Name, err)
		}
		err = e.write(target, body, info.Mode().Perm())
		closeErr := body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return fmt.Errorf("close archive entry %q: %w", entry.Name, closeErr)
		}
	}
	return nil
}

func (e *extractor) tarGz(ctx context.Context, file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind archive: %w", err)
	}

	gz, err := gzip.NewReader(file)
	if err != nil {
		return apierr.ArchiveRejected("it is not a readable gzip archive")
	}
	defer func() {
		if err := gz.Close(); err != nil {
			slog.Warn("close gzip reader", "error", err)
		}
	}()

	return e.readTar(ctx, tar.NewReader(gz))
}

func (e *extractor) tar(ctx context.Context, file *os.File) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind archive: %w", err)
	}
	return e.readTar(ctx, tar.NewReader(file))
}

func (e *extractor) readTar(ctx context.Context, reader *tar.Reader) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return apierr.ArchiveRejected("it is not a readable tar archive")
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := e.target(header.Name); err != nil {
				return err
			}
			continue
		case tar.TypeReg:
		default:
			return apierr.ArchiveRejected(
				fmt.Sprintf("it contains %q, which is not a regular file", header.Name))
		}

		if err := e.admit(header.Name, header.Size); err != nil {
			return err
		}

		target, err := e.target(header.Name)
		if err != nil {
			return err
		}
		if err := e.write(target, reader, os.FileMode(header.Mode).Perm()); err != nil {
			return err
		}
	}
}

// admit applies the limits before an entry is written, so a bomb is refused
// rather than half-extracted.
func (e *extractor) admit(name string, size int64) error {
	e.entries++
	if e.entries > e.limits.MaxEntries {
		return apierr.ArchiveRejected(
			fmt.Sprintf("it holds more than %d files", e.limits.MaxEntries))
	}

	if size < 0 {
		return apierr.ArchiveRejected(fmt.Sprintf("entry %q declares a negative size", name))
	}
	if e.written+size > e.limits.MaxBytes {
		return apierr.ArchiveRejected(
			fmt.Sprintf("it expands to more than %d MB", e.limits.MaxBytes/(1<<20)))
	}
	if e.compressed > 0 && (e.written+size)/max(e.compressed, 1) > e.limits.MaxRatio {
		return apierr.ArchiveRejected(
			fmt.Sprintf("it expands by more than %d times its own size, which is how a "+
				"compression bomb behaves", e.limits.MaxRatio))
	}
	return nil
}

// target resolves an entry name inside the root, or refuses it.
//
// filepath.Clean then a prefix check on the cleaned absolute path. The entry name
// is never joined without cleaning first, because "a/../../b" joins to something
// inside the root only after cleaning, and a prefix check on an uncleaned path is
// not a control (backend-standards.md 13).
func (e *extractor) target(name string) (string, error) {
	if strings.ContainsRune(name, 0) {
		return "", apierr.ArchiveRejected("an entry name contains a null byte")
	}

	cleaned := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", apierr.ArchiveRejected(
			fmt.Sprintf("it contains %q, which would write outside the workspace", name))
	}

	target := filepath.Join(e.root, cleaned)
	if target != e.root && !strings.HasPrefix(target, e.root+string(os.PathSeparator)) {
		return "", apierr.ArchiveRejected(
			fmt.Sprintf("it contains %q, which would write outside the workspace", name))
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return "", fmt.Errorf("create directory for %q: %w", name, err)
	}
	return target, nil
}

// write copies one entry, counting bytes as it goes.
//
// The copy is bounded by the remaining byte allowance rather than trusting the
// declared size: a tar header can claim one size and deliver another, and the
// limit has to hold against the bytes that actually arrive.
func (e *extractor) write(target string, body io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o640
	}
	// Never executable. Nothing in an ingested archive is run directly, and the
	// runner phase mounts its own toolchain.
	mode &^= 0o111

	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return apierr.ArchiveRejected(
				fmt.Sprintf("it contains %q more than once", filepath.Base(target)))
		}
		return fmt.Errorf("create %s: %w", target, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			slog.Warn("close extracted file", "path", target, "error", err)
		}
	}()

	remaining := e.limits.MaxBytes - e.written
	written, err := io.Copy(file, io.LimitReader(body, remaining+1))
	e.written += written
	if err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	if written > remaining {
		return apierr.ArchiveRejected(
			fmt.Sprintf("it expands to more than %d MB", e.limits.MaxBytes/(1<<20)))
	}
	return nil
}

// spoolToDisk copies the archive to a temporary file and reports its size.
func spoolToDisk(ctx context.Context, source io.Reader, maxBytes int64) (*os.File, int64, error) {
	file, err := os.CreateTemp("", "qavia-archive-*")
	if err != nil {
		return nil, 0, fmt.Errorf("create temporary file: %w", err)
	}

	written, err := io.Copy(file, io.LimitReader(&cancellable{ctx: ctx, inner: source}, maxBytes+1))
	if err != nil {
		discardSpool(file)
		return nil, 0, fmt.Errorf("read archive: %w", err)
	}
	if written > maxBytes {
		discardSpool(file)
		return nil, 0, apierr.ArchiveRejected(
			fmt.Sprintf("it is larger than %d MB", maxBytes/(1<<20)))
	}
	return file, written, nil
}

// discardSpool closes and removes the spooled archive. The extraction it belonged
// to is over either way, so a failure here is logged rather than returned.
func discardSpool(file *os.File) {
	name := file.Name()
	if err := file.Close(); err != nil {
		slog.Warn("close archive spool", "path", name, "error", err)
	}
	if err := os.Remove(name); err != nil {
		slog.Warn("remove archive spool", "path", name, "error", err)
	}
}

// cancellable makes a long copy honour cancellation, which io.Copy cannot do on
// its own.
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
