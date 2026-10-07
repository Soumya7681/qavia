// Package riskcontrol is the approval gate for performance and security testing
// (BE-9.5, gate G3, F-11.5, F-11.6).
//
// These two test kinds share one property that no other work in the platform has:
// pointed at a machine you do not own, they are indistinguishable from an attack. A
// load test *is* a denial-of-service attempt; a security scan *is* an intrusion attempt.
// Whether it is testing or an attack is decided entirely by whether the person running
// it was allowed to, and this package is where that permission is checked.
//
// Three gates, each defeating a different way the wrong thing happens:
//
//  1. **Disabled by default, enabled per project by a lead.** A fresh project cannot run
//     either kind. Turning it on is a deliberate act by someone with the authority to
//     make it, so nobody enables scanning by accident (BE-9.5.1).
//  2. **The first run against a new host needs an explicit confirmation naming that
//     exact host.** Not a checkbox that persists — the host itself, typed or echoed, so
//     "I meant staging, not production" is caught before a single request goes out. The
//     confirmation is remembered per host, because a prompt on every run is a prompt
//     people learn to click through (BE-9.5.2).
//  3. **Both the enablement and the confirmation are audited**, because after a scan the
//     question is not "did it happen" but "who authorised this, against what" (BE-9.5.3).
//
// The fourth control — rejecting a host outside the allowlist before any traffic — is
// not here: it is the target check the run path already performs, and reusing it is the
// point (BE-9.5.4).
package riskcontrol

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Kind is the test kind being gated.
type Kind string

const (
	KindPerformance Kind = "performance"
	KindSecurity    Kind = "security"
)

// EnabledSetting is the settings key that turns a kind on for a project.
func (k Kind) EnabledSetting() string {
	switch k {
	case KindPerformance:
		return "performance.enabled"
	case KindSecurity:
		return "security.enabled"
	default:
		return ""
	}
}

// Settings is the slice of the settings service this package reads.
type Settings interface {
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
}

// Guard enforces the approval controls.
type Guard struct {
	db       *store.DB
	settings Settings
}

func NewGuard(db *store.DB, settingsService Settings) *Guard {
	return &Guard{db: db, settings: settingsService}
}

// Authorize checks a request to run a dangerous test kind against a host.
//
// The confirmation string is what the caller typed to name the host it intends to hit.
// A first run against a host it does not match — including an empty one — is refused
// with a code the UI turns into a "confirm you mean <host>" prompt rather than a dead
// error. A run against a host already confirmed needs no confirmation and passes
// straight through.
func (g *Guard) Authorize(
	ctx context.Context,
	projectID uuid.UUID,
	kind Kind,
	host string,
	confirmation string,
	actor httpx.Principal,
) error {
	enabled, err := g.settings.Bool(ctx, kind.EnabledSetting(),
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the %s enablement: %w", kind, err))
	}
	if !enabled {
		return apierr.TestKindDisabled(string(kind))
	}

	host = normaliseHost(host)
	if host == "" {
		return apierr.Validation(
			"There is no target host to authorise this run against.",
			map[string]any{"field": "host"})
	}

	confirmed, err := g.db.Queries().HostConfirmed(ctx, dbgen.HostConfirmedParams{
		ProjectID: projectID, Kind: string(kind), Host: host,
	})
	if err != nil {
		return apierr.Internal(fmt.Errorf("check the host confirmation: %w", err))
	}
	if confirmed {
		return nil
	}

	// A new host. The confirmation has to name it exactly — echoing the host is what
	// makes "I meant staging" catchable, and a checkbox would not (BE-9.5.2).
	if !strings.EqualFold(normaliseHost(confirmation), host) {
		return apierr.HostConfirmationRequired(host, string(kind))
	}

	actorID := actor.UserID
	if _, err := g.db.Queries().ConfirmHost(ctx, dbgen.ConfirmHostParams{
		ProjectID:   projectID,
		Kind:        string(kind),
		Host:        host,
		ConfirmedBy: &actorID,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record the host confirmation: %w", err))
	}
	return nil
}

// Confirmed reports whether a host is already confirmed for a kind, so a UI can show a
// confirm prompt only when one is needed.
func (g *Guard) Confirmed(
	ctx context.Context,
	projectID uuid.UUID,
	kind Kind,
	host string,
) (bool, error) {
	confirmed, err := g.db.Queries().HostConfirmed(ctx, dbgen.HostConfirmedParams{
		ProjectID: projectID, Kind: string(kind), Host: normaliseHost(host),
	})
	if err != nil {
		return false, apierr.Internal(fmt.Errorf("check the host confirmation: %w", err))
	}
	return confirmed, nil
}

// normaliseHost reduces a host to what the confirmation compares against: lowercased,
// without a scheme, port, or trailing slash, so confirming "https://staging.example.com/"
// and "staging.example.com:443" are the same confirmation.
func normaliseHost(value string) string {
	host := strings.ToLower(strings.TrimSpace(value))
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimSuffix(host, "/")
	if index := strings.IndexByte(host, '/'); index >= 0 {
		host = host[:index]
	}
	if index := strings.IndexByte(host, ':'); index >= 0 {
		host = host[:index]
	}
	return host
}
