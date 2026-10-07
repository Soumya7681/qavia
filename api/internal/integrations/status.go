// Package integrations reports the health of every optional integration, so an admin can
// see at a glance which are configured, which are active, and which have degraded
// (BE-10.6.3, FR-10.6).
//
// The report is an aggregation across the capability registries, not a new source of
// truth: each adapter already knows whether it is configured (its Available), and the
// registries already know which is the built-in fallback. This package asks them and
// renders the answer. That is the whole design — an integration's health is the
// adapter's own fact, surfaced, never a status this package computes on its own and
// could let drift.
package integrations

import (
	"context"

	"github.com/hyscaler/qavia/api/internal/capability/defecttracker"
	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/capability/sourceprovider"
)

// State is one integration's health.
type State string

const (
	// StateBuiltin is the always-on default: the in-app channel, the internal tracker,
	// the archive provider. Never absent, so an install with nothing configured still
	// reads as working rather than broken (requirements.md 5.4).
	StateBuiltin State = "builtin"

	// StateActive is an external adapter that is configured and answering.
	StateActive State = "active"

	// StateNotConfigured is an external adapter with no settings. The normal state of a
	// fresh install, and reported as such rather than as an error.
	StateNotConfigured State = "not_configured"
)

// Integration is one adapter's status line.
type Integration struct {
	// Capability is the registry it belongs to: notifier, defecttracker,
	// sourceprovider, runtrigger, objectstore.
	Capability string

	// ID is the adapter's own ID: "in-app", "smtp", "slack", "internal", "jira", …
	ID string

	// Builtin is true for the fallback each registry registers first.
	Builtin bool

	State State
}

// Registries is what a status report reads. Declared as the concrete registry types
// because that is what main holds; a status report is inherently a view over all of
// them.
type Registries struct {
	Notifiers      *notifier.Registry
	Trackers       *defecttracker.Registry
	SourceProvider *sourceprovider.Registry
	Triggers       *runtrigger.Registry
	ObjectStore    objectstore.Store
}

// Report lists every registered integration and its health.
func Report(ctx context.Context, registries Registries) []Integration {
	var integrations []Integration

	if registries.Notifiers != nil {
		integrations = append(integrations, statesFor(ctx, "notifier",
			registries.Notifiers.All(), registries.Notifiers.IDs(), func(n notifier.Notifier) (string, bool) {
				return n.ID(), n.Available(ctx)
			})...)
	}
	if registries.Trackers != nil {
		integrations = append(integrations, statesFor(ctx, "defecttracker",
			registries.Trackers.All(), registries.Trackers.IDs(), func(t defecttracker.Tracker) (string, bool) {
				return t.ID(), t.Available(ctx)
			})...)
	}
	if registries.SourceProvider != nil {
		integrations = append(integrations, statesFor(ctx, "sourceprovider",
			registries.SourceProvider.All(), registries.SourceProvider.IDs(), func(p sourceprovider.Provider) (string, bool) {
				return p.ID(), p.Available(ctx)
			})...)
	}
	if registries.Triggers != nil {
		integrations = append(integrations, statesFor(ctx, "runtrigger",
			registries.Triggers.All(), registries.Triggers.IDs(), func(t runtrigger.Trigger) (string, bool) {
				return t.ID(), t.Available(ctx)
			})...)
	}
	if registries.ObjectStore != nil {
		// The object store is a single implementation rather than a registry, but its
		// health belongs on the same screen: an unreachable bucket is an integration
		// failure like any other.
		state := StateBuiltin
		if err := objectstore.Probe(ctx, registries.ObjectStore); err != nil {
			state = StateNotConfigured
		}
		integrations = append(integrations, Integration{
			Capability: "objectstore",
			ID:         registries.ObjectStore.ID(),
			Builtin:    true,
			State:      state,
		})
	}

	return integrations
}

// statesFor turns a registry's implementations into status lines. The first ID is the
// built-in; everything else is active when its Available reports so, and not-configured
// otherwise.
func statesFor[T any](
	_ context.Context,
	capability string,
	implementations []T,
	ids []string,
	inspect func(T) (string, bool),
) []Integration {
	builtinID := ""
	if len(ids) > 0 {
		builtinID = ids[0]
	}

	lines := make([]Integration, 0, len(implementations))
	for _, implementation := range implementations {
		id, available := inspect(implementation)

		line := Integration{Capability: capability, ID: id, Builtin: id == builtinID}
		switch {
		case line.Builtin:
			line.State = StateBuiltin
		case available:
			line.State = StateActive
		default:
			line.State = StateNotConfigured
		}
		lines = append(lines, line)
	}
	return lines
}
