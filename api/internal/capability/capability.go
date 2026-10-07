package capability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
)

// Implementation is what every capability interface embeds.
//
// ID names the implementation, not the vendor category: "local-disk", "s3",
// "in-app", "slack". Available reports whether it is usable right now, which for
// an external adapter means its settings are present and its endpoint answered.
type Implementation interface {
	ID() string
	Available(ctx context.Context) bool
}

// Reporter surfaces a degradation to a human (FR-10.6).
//
// Declared here as the smallest interface the registry needs rather than taking
// the notifications service, which would point the dependency the wrong way: a
// platform-level registry must not import a domain package.
type Reporter interface {
	Degraded(ctx context.Context, event Degradation)
}

// Degradation is one external adapter failing and the built-in taking over.
type Degradation struct {
	// Kind is the capability, for example "notifier".
	Kind string

	// FailedID is the adapter that errored, FallbackID the one that ran instead.
	FailedID   string
	FallbackID string

	Operation string
	Err       error
}

// Registry holds every implementation of one capability.
//
// The first registration is the built-in and is the fallback for every later one.
// main.go registers built-ins unconditionally and external adapters only when
// their settings are present, so main.go stays the only file that knows a vendor
// name (backend-standards.md 7).
type Registry[T Implementation] struct {
	kind string

	mu      sync.RWMutex
	byID    map[string]T
	order   []string
	builtin string
}

func NewRegistry[T Implementation](kind string) *Registry[T] {
	return &Registry[T]{kind: kind, byID: make(map[string]T)}
}

// Kind returns the capability name, for logs and degradation reports.
func (r *Registry[T]) Kind() string { return r.kind }

// Register adds an implementation. The first one registered becomes the built-in.
//
// Registering the same ID twice panics. These calls all happen in main.go before
// the server starts, so a duplicate is a wiring bug and a failed boot beats a
// process where which implementation won depends on registration order.
func (r *Registry[T]) Register(impl T) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := impl.ID()
	if id == "" {
		panic(fmt.Sprintf("capability: %s implementation has an empty ID", r.kind))
	}
	if _, duplicate := r.byID[id]; duplicate {
		panic(fmt.Sprintf("capability: %s implementation %q registered twice", r.kind, id))
	}

	r.byID[id] = impl
	r.order = append(r.order, id)
	if r.builtin == "" {
		r.builtin = id
	}
}

// Get returns one implementation by ID.
func (r *Registry[T]) Get(id string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	impl, found := r.byID[id]
	return impl, found
}

// Builtin returns the implementation that always works, which is what a
// degradation falls back to.
func (r *Registry[T]) Builtin() (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	impl, found := r.byID[r.builtin]
	return impl, found
}

// All returns every registered implementation in registration order, built-in
// first. Order is stable so a settings screen lists them the same way every time.
func (r *Registry[T]) All() []T {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]T, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

// Active returns the implementations that are usable right now.
//
// An unconfigured adapter is absent from this list rather than being an error: an
// unconfigured optional integration is the normal state of a fresh install
// (requirements.md 5.4).
func (r *Registry[T]) Active(ctx context.Context) []T {
	out := make([]T, 0, len(r.order))
	for _, impl := range r.All() {
		if impl.Available(ctx) {
			out = append(out, impl)
		}
	}
	return out
}

// IDs returns the registered IDs, built-in first.
func (r *Registry[T]) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Clone(r.order)
}

// Resolve returns the implementation for id, or the built-in when id is empty.
//
// An id that names nothing is an error rather than a silent fall through to the
// built-in: a settings value pointing at a driver that was never registered is a
// misconfiguration somebody needs to see.
func (r *Registry[T]) Resolve(id string) (T, error) {
	var zero T

	if id == "" {
		impl, found := r.Builtin()
		if !found {
			return zero, fmt.Errorf("capability: no %s implementation is registered", r.kind)
		}
		return impl, nil
	}

	impl, found := r.Get(id)
	if !found {
		return zero, fmt.Errorf("capability: %s implementation %q is not registered (have %v)",
			r.kind, id, r.IDs())
	}
	return impl, nil
}

// Attempt runs op against the chosen implementation and degrades to the built-in
// when it fails.
//
// This is the whole point of the registry: an external outage must never lose a
// notification or a defect (FR-10.6). The failure is logged, reported so an admin
// hears about it, and the work still completes.
//
// The built-in is not retried through this path when it is the one that failed:
// there is nothing further to fall back to, so the error is returned.
func (r *Registry[T]) Attempt(
	ctx context.Context,
	reporter Reporter,
	operation string,
	id string,
	op func(context.Context, T) error,
) error {
	impl, err := r.Resolve(id)
	if err != nil {
		return err
	}

	opErr := op(ctx, impl)
	if opErr == nil {
		return nil
	}

	builtin, found := r.Builtin()
	if !found || builtin.ID() == impl.ID() {
		return fmt.Errorf("%s %s via %s: %w", r.kind, operation, impl.ID(), opErr)
	}

	slog.WarnContext(ctx, "capability degraded to built-in",
		"capability", r.kind,
		"failed", impl.ID(),
		"fallback", builtin.ID(),
		"operation", operation,
		"error", opErr,
	)

	if reporter != nil {
		reporter.Degraded(ctx, Degradation{
			Kind:       r.kind,
			FailedID:   impl.ID(),
			FallbackID: builtin.ID(),
			Operation:  operation,
			Err:        opErr,
		})
	}

	if fallbackErr := op(ctx, builtin); fallbackErr != nil {
		// Both failed. The original error is the interesting one, so it stays in
		// the chain rather than being replaced by the fallback's.
		return fmt.Errorf("%s %s: %s failed and built-in %s also failed: %w",
			r.kind, operation, impl.ID(), builtin.ID(), errors.Join(opErr, fallbackErr))
	}
	return nil
}
