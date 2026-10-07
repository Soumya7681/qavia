// Package role holds the four platform roles.
//
// It exists as its own package because both the HTTP middleware and every
// domain service need the type, and a platform package must not import a domain
// package to get it (backend-standards.md 2).
//
// Authorisation is applied to route groups, never inside a service
// (backend-standards.md 11). A service may still express a business rule that
// happens to involve a role, such as "only a Lead may approve", and that is a
// different thing.
package role

import "fmt"

// Role is one of exactly four values (requirements.md 3).
type Role string

const (
	// Admin can do everything, plus global settings, LLM keys, runner config,
	// user management, and host allowlists.
	Admin Role = "admin"

	// QALead can create projects, configure project settings, approve generated
	// tests, trigger runs, and view all projects.
	QALead Role = "qa_lead"

	// QAEngineer can create projects they own, and generate, review, edit, and
	// run tests on their own projects.
	QAEngineer Role = "qa_engineer"

	// Viewer has read-only access to projects, runs, and reports.
	Viewer Role = "viewer"
)

// All is every role, weakest first. Order is load-bearing for AtLeast.
var All = []Role{Viewer, QAEngineer, QALead, Admin}

var ranks = map[Role]int{
	Viewer:     0,
	QAEngineer: 1,
	QALead:     2,
	Admin:      3,
}

// Valid reports whether r is one of the four roles. An unknown role must never
// be treated as a weak one: it is a bug, and the caller rejects it.
func (r Role) Valid() bool {
	_, ok := ranks[r]
	return ok
}

// AtLeast reports whether r is at or above minimum in the hierarchy.
//
// Use this for genuinely hierarchical checks. Where a route allows a specific
// set of roles rather than a floor, list them explicitly instead: the set is
// clearer at the call site and survives a future role that does not slot neatly
// into the order.
func (r Role) AtLeast(minimum Role) bool {
	mine, ok := ranks[r]
	if !ok {
		return false
	}
	theirs, ok := ranks[minimum]
	if !ok {
		return false
	}
	return mine >= theirs
}

// Label is the human-readable name, used in error messages such as
// "This action requires the QA Lead role."
func (r Role) Label() string {
	switch r {
	case Admin:
		return "Admin"
	case QALead:
		return "QA Lead"
	case QAEngineer:
		return "QA Engineer"
	case Viewer:
		return "Viewer"
	default:
		return string(r)
	}
}

func (r Role) String() string { return string(r) }

// Parse converts stored or submitted text into a Role.
func Parse(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("unknown role %q", s)
	}
	return r, nil
}

// Labels renders a set of roles for an error message.
func Labels(roles []Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, r.Label())
	}
	return out
}
