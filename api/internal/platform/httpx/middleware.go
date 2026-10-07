package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/role"
)

// CorrelationHeader is accepted from a caller and echoed on the response, so a
// browser, a CI system, or a webhook can pin a request to its logs.
const CorrelationHeader = "X-Correlation-ID"

// CorrelationID puts a correlation ID on the context for every request.
//
// Everything downstream, including the logger and the job payload, reads it from
// the context, so nothing else has to be threaded through.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if id == "" {
			id = uuid.NewString()
		}

		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(logging.WithCorrelationID(r.Context(), id)))
	})
}

// Recoverer turns a panic into a 500 with an incident ID.
//
// It is a backstop, not control flow: no package may panic across its own
// boundary (backend-standards.md 5). It exists so one bad request cannot kill a
// process that is also running the job queue.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// A client that disconnected mid-write is not an incident, and the
			// net/http server expects this one to keep travelling.
			if err, isError := recovered.(error); isError && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}

			slog.ErrorContext(ctx, "panic recovered",
				"panic", recovered,
				"method", r.Method,
				"path", r.URL.Path,
			)
			WriteError(ctx, w, apierr.Internal(recoveredError(recovered)))
		}()

		next.ServeHTTP(w, r)
	})
}

func recoveredError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return errors.New("panic: non-error value")
}

// RequestLogger logs one line per request at info level.
//
// Levels mean something here: info is a state transition, and a completed
// request is exactly that. Client errors are already logged by WriteError, so
// this line stays quiet about them.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		slog.InfoContext(r.Context(), "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.written,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

// statusRecorder captures the status for the access log.
//
// Unwrap keeps http.ResponseController working, which is what the SSE endpoints
// use to flush. A wrapper without it silently breaks streaming.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// MaxBytes caps a request body before a handler reads it. Upload endpoints set
// their own, larger, limit from settings.
func MaxBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth rejects a request with no authenticated principal.
//
// Applied to a route group, so a route added inside that group inherits it and
// forgetting is not possible. A route registered outside a group is a review
// failure (backend-standards.md 11).
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentUser(r.Context()); !ok {
			WriteError(r.Context(), w, apierr.Unauthenticated())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole allows only the listed roles.
//
// The set is explicit rather than a floor. A future role that does not slot
// neatly into the hierarchy then cannot silently gain access to every route that
// happened to say "at least QA Engineer".
func RequireRole(allowed ...role.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := CurrentUser(r.Context())
			if !ok {
				WriteError(r.Context(), w, apierr.Unauthenticated())
				return
			}
			if !slices.Contains(allowed, principal.Role) {
				WriteError(r.Context(), w, apierr.RoleRequired(highest(allowed).Label()))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// highest picks the strongest role in a set, for the error message. Naming the
// weakest sufficient role would be more helpful, but the message is about what
// the caller lacks.
func highest(roles []role.Role) role.Role {
	best := role.Viewer
	for _, r := range roles {
		if r.AtLeast(best) {
			best = r
		}
	}
	return best
}

// ProjectAccessChecker is declared here, by the consumer, and satisfied by the
// projects service (backend-standards.md 3). The interface stays one method, and
// the test double stays one struct.
type ProjectAccessChecker interface {
	// CheckAccess returns nil when the principal may act on the project, and a
	// domain error otherwise. The owning service is where the invariants live:
	// archived state, membership, and role escalation rules.
	CheckAccess(ctx context.Context, projectID uuid.UUID, principal Principal) error
}

// ProjectIDParam is the chi URL parameter every project-scoped route uses.
const ProjectIDParam = "projectID"

// RequireProjectMembership enforces project access for a route group.
//
// This is authorisation, not a service's memory of who asked. It runs before any
// handler in the group, so a new nested route inherits it.
func RequireProjectMembership(checker ProjectAccessChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			principal, ok := CurrentUser(ctx)
			if !ok {
				WriteError(ctx, w, apierr.Unauthenticated())
				return
			}

			raw := chi.URLParam(r, ProjectIDParam)
			projectID, err := uuid.Parse(raw)
			if err != nil {
				// An unparseable ID is indistinguishable from a project the
				// caller cannot see, and must stay that way: a 400 here tells an
				// attacker which IDs are well-formed.
				WriteError(ctx, w, apierr.ProjectNotFound(placeholderID(raw)))
				return
			}

			if err := checker.CheckAccess(ctx, projectID, principal); err != nil {
				WriteError(ctx, w, err)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// placeholderID lets ProjectNotFound record what was asked for without pretending
// it was a UUID.
type placeholderID string

func (p placeholderID) String() string { return string(p) }
