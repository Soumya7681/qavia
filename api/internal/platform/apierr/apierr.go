// Package apierr defines the domain error type carried across every layer.
//
// One error type carries what the HTTP layer needs, and one mapper in httpx
// translates it (backend-standards.md 5). Services return these; they never
// return an HTTP status directly, because a service must be callable from a job
// worker where HTTP is meaningless.
//
// Three rules that the type exists to enforce:
//
//   - Every error carries a stable machine-readable Code. The frontend branches
//     on the code, never on the message string.
//   - Messages are actionable. "No model assigned to the reasoning tier.
//     Configure it in Settings, AI, Tiers." rather than "tier resolution failed".
//   - No secret, credential, or raw provider response ever goes in a message. It
//     ends up in a log, a notification, and a screenshot.
package apierr

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"
)

// Error is the one domain error type.
type Error struct {
	// Code is stable and machine readable. Adding one means adding a row to
	// docs/error-codes.md in the same PR.
	Code string

	// Status is the HTTP status the mapper will use. Kept here so there is one
	// place per error rather than a translation table that drifts.
	Status int

	// Message is safe to show a user and says what to do next.
	Message string

	// Details carries field-level context for a client to render inline. It must
	// never carry a secret or a raw upstream response.
	Details map[string]any

	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause so errors.Is and errors.As work through the chain.
// The chain is what makes a 500 diagnosable.
func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches the underlying failure. The cause is logged, never
// returned to a client.
func (e *Error) WithCause(cause error) *Error {
	clone := e.clone()
	clone.cause = cause
	return clone
}

// WithDetails attaches field-level context.
func (e *Error) WithDetails(details map[string]any) *Error {
	clone := e.clone()
	if clone.Details == nil {
		clone.Details = make(map[string]any, len(details))
	}
	maps.Copy(clone.Details, details)
	return clone
}

// WithMessage replaces the message, for the cases where the caller knows more
// than the constructor did.
func (e *Error) WithMessage(message string) *Error {
	clone := e.clone()
	clone.Message = sentence(message)
	return clone
}

// sentence guarantees terminal punctuation. Several constructors interpolate a
// caller-supplied reason, and a message that trails off reads like a truncated
// log line rather than something written for a user.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	switch s[len(s)-1] {
	case '.', '?', '!':
		return s
	default:
		return s + "."
	}
}

// clone keeps the constructors safe to use as package-level values: mutating a
// returned error must not edit a shared template.
func (e *Error) clone() *Error {
	out := *e
	if e.Details != nil {
		out.Details = maps.Clone(e.Details)
	}
	return &out
}

// New builds an error. Prefer a named constructor in errors.go so that every
// code in the codebase is greppable in one file.
func New(code string, status int, message string) *Error {
	return &Error{Code: code, Status: status, Message: sentence(message)}
}

// As extracts a domain error from a wrapped chain.
func As(err error) (*Error, bool) {
	var domain *Error
	if errors.As(err, &domain) {
		return domain, true
	}
	return nil, false
}

// Is reports whether err is a domain error with the given code.
func Is(err error, code string) bool {
	domain, ok := As(err)
	return ok && domain.Code == code
}

// Status returns the HTTP status for any error. An unmapped error is a 500,
// which the mapper turns into an incident ID and nothing else.
func Status(err error) int {
	if domain, ok := As(err); ok {
		return domain.Status
	}
	return http.StatusInternalServerError
}

// CodeOf returns the stable code for any error, for the places that record why
// something was refused: an audit row, a metric label.
//
// Only the code, never the message. A message can carry a host or a filename that
// belongs in a response but not in a row somebody exports.
func CodeOf(err error) string {
	if domain, ok := As(err); ok {
		return domain.Code
	}
	return CodeInternal
}
