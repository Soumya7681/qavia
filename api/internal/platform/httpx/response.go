// Package httpx holds HTTP middleware and the domain-error to HTTP mapper.
//
// This is the only package that knows about status codes. Services return
// domain errors, because a service must be callable from a job worker where HTTP
// is meaningless (backend-standards.md 5).
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// Envelope is the single error shape every failed request returns
// (backend-standards.md 11). It is produced here and nowhere else.
type Envelope struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`

	// IncidentID appears only on a 500. It is the correlation handle between what
	// the user saw and the full wrapped cause in the log.
	IncidentID string `json:"incidentId,omitempty"`
}

// WriteJSON writes a success body. Handlers built from the generated interface
// rarely need it; setup, SSE, and file endpoints do.
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status is already on the wire, so there is nothing to correct. Log
		// it: a truncated response body is worth knowing about.
		slog.ErrorContext(ctx, "encode response body", "error", err)
	}
}

// WriteError maps any error to the envelope.
//
// Three behaviours worth knowing:
//
//   - A domain error returns its own code, status, message, and details.
//   - An unmapped error returns a 500 carrying a generated incident ID and
//     nothing else. The full wrapped chain is logged against that ID, so a
//     support conversation starts with one short string.
//   - Details are copied as-is, so a constructor that puts a secret in details
//     would leak. The redacting logger cannot protect a response body, which is
//     why the rule lives in apierr and is covered by its tests.
func WriteError(ctx context.Context, w http.ResponseWriter, err error) {
	domain, ok := apierr.As(err)
	if !ok {
		writeInternal(ctx, w, err)
		return
	}

	// Only an internal failure is reduced to an incident ID. A deliberate 5xx is
	// not the same thing: "this feature is not delivered yet" (501) and "file
	// storage is not reachable, check Settings" (503) are decisions with an
	// actionable message, and replacing them with an incident ID would tell the
	// user nothing and the operator nothing either.
	if domain.Code == apierr.CodeInternal {
		writeInternal(ctx, w, err)
		return
	}

	// A deliberate 5xx still needs a human, so it is logged at error level with the
	// wrapped chain. Everything below 500 is the API working as designed, and
	// logging that at error level trains people to ignore errors.
	level := slog.LevelWarn
	if domain.Status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	slog.Log(ctx, level, "request rejected",
		"code", domain.Code,
		"status", domain.Status,
		"error", err.Error(),
	)

	if retry, found := domain.Details["retryAfterSeconds"]; found {
		if seconds, isInt := retry.(int); isInt {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
	}

	WriteJSON(ctx, w, domain.Status, Envelope{
		Code:    domain.Code,
		Message: domain.Message,
		Details: domain.Details,
	})
}

func writeInternal(ctx context.Context, w http.ResponseWriter, err error) {
	incident := uuid.NewString()[:8]

	slog.ErrorContext(ctx, "unhandled error",
		"incident_id", incident,
		// The wrapped chain is what makes a 500 diagnosable, and it stays on this
		// side of the boundary.
		"error", err.Error(),
	)

	WriteJSON(ctx, w, http.StatusInternalServerError, Envelope{
		Code:       apierr.CodeInternal,
		Message:    "Something went wrong on our side.",
		IncidentID: incident,
	})
}
