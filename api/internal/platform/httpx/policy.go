package httpx

import (
	"context"
	"fmt"
	"slices"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
)

// Policy declares who may call one operation.
//
// backend-standards.md 11 requires role checks on route groups rather than on
// individual routes, so that a new route inherits the check and forgetting is not
// possible. The generated server registers routes flat rather than in groups, so
// the same guarantee is expressed here instead: one table keyed by operation ID,
// deny by default, plus a test asserting every operation in the spec has an entry.
//
// That is stronger than group inheritance. Group inheritance protects a new route
// silently; this fails the build when a new operation appears without a decision
// being recorded.
type Policy struct {
	// Public skips authentication entirely. Only health, login, invite
	// acceptance, the first-run setup endpoints, and inbound webhooks qualify.
	Public bool

	// Roles is the exact set allowed. It is a set, not a floor: a future role that
	// outranks QA Engineer must not silently gain access to every route that
	// happened to allow QA Engineer.
	Roles []role.Role
}

// PolicySet maps operation ID to policy, keyed by the operationId in
// qavia.yaml so the table reads like the contract.
type PolicySet map[string]Policy

// NormalizeOperationID converts the generated Go method name back to the spec's
// operationId.
//
// oapi-codegen hands its middleware the Go method name ("GetReadiness") rather
// than the operationId ("getReadiness"). Keying the table by the Go name would
// make it stop matching the contract it describes, so the one-character
// difference is undone here instead. The router test asserts every operationId in
// the spec is camelCase, which is what makes this reversible.
func NormalizeOperationID(goMethodName string) string {
	if goMethodName == "" {
		return ""
	}
	first := goMethodName[0]
	if first < 'A' || first > 'Z' {
		return goMethodName
	}
	return string(first-'A'+'a') + goMethodName[1:]
}

// Authorize applies the policy for one operation.
//
// An operation with no entry is refused. Failing closed means the worst outcome
// of forgetting an entry is a broken endpoint in review, not an unguarded one in
// production.
func (s PolicySet) Authorize(ctx context.Context, operationID string) error {
	operationID = NormalizeOperationID(operationID)

	policy, declared := s[operationID]
	if !declared {
		return apierr.Forbidden().WithCause(
			fmt.Errorf("operation %q has no authorisation policy", operationID))
	}

	if policy.Public {
		return nil
	}

	principal, ok := CurrentUser(ctx)
	if !ok {
		return apierr.Unauthenticated()
	}

	if len(policy.Roles) == 0 {
		// Authenticated is enough. Used by endpoints every signed-in user needs,
		// such as reading their own profile or their own notifications.
		return nil
	}

	if !slices.Contains(policy.Roles, principal.Role) {
		return apierr.RoleRequired(highest(policy.Roles).Label())
	}
	return nil
}

// MissingOperations returns the operation IDs in the spec that have no policy
// entry. The router test uses it so the failure names exactly what to add.
func (s PolicySet) MissingOperations(operationIDs []string) []string {
	var missing []string
	for _, id := range operationIDs {
		if _, ok := s[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// UnknownOperations returns policy entries that no longer match an operation in
// the spec. A stale entry is dead weight that makes the table stop being a
// reliable description of the API.
func (s PolicySet) UnknownOperations(operationIDs []string) []string {
	var unknown []string
	for id := range s {
		if !slices.Contains(operationIDs, id) {
			unknown = append(unknown, id)
		}
	}
	slices.Sort(unknown)
	return unknown
}
