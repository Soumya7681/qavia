package sourceprovider

import (
	"context"
	"io"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
)

// Request is where a provider should get the source from.
//
// One shape serves both the built-in and the connected providers: an archive
// upload names a stored object, and a repository provider names a URL and a
// reference. Fixing the shape now is what lets clone-by-URL (BE-6.1) slot in
// without changing a caller.
type Request struct {
	ProjectID uuid.UUID

	// StorageKey names an uploaded archive in the object store.
	StorageKey string
	Filename   string

	// URL and Ref name a repository. Empty for the built-in.
	URL string
	Ref string
}

// Result describes what landed in the workspace.
type Result struct {
	// Root is the directory the source was materialised into. It is per job, never
	// shared, so two jobs cannot see each other's files.
	Root string

	Files int
	Bytes int64

	// Ref is what was actually fetched, for a repository provider: the resolved
	// commit rather than the branch name, so a rerun is reproducible.
	Ref string
}

// Provider materialises source code into a workspace.
type Provider interface {
	ID() string
	Available(ctx context.Context) bool

	// Fetch writes the source into workspace and reports what it wrote. The
	// caller owns the directory and removes it when the job ends.
	Fetch(ctx context.Context, req Request, workspace string) (Result, error)
}

// Opener is the slice of the object store this package needs, declared by the
// consumer (backend-standards.md 3).
type Opener interface {
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Registry holds every source provider. The archive provider registers first and
// is therefore the fallback.
type Registry = capability.Registry[Provider]

// NewRegistry builds the source provider registry.
func NewRegistry() *Registry { return capability.NewRegistry[Provider]("sourceprovider") }
