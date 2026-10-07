package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/role"
)

// captureLogs redirects the default logger so assertions can read what a
// middleware wrote, and so a test never pollutes the real output.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(logging.New(logging.Options{Writer: &buf, Level: slog.LevelDebug}))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func decodeEnvelope(t *testing.T, body []byte) Envelope {
	t.Helper()

	var env Envelope
	require.NoError(t, json.Unmarshal(body, &env))
	return env
}

func TestWriteErrorMapsADomainError(t *testing.T) {
	captureLogs(t)
	rec := httptest.NewRecorder()

	WriteError(context.Background(), rec, apierr.TargetHostNotAllowed("api.staging.example.test"))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))

	env := decodeEnvelope(t, rec.Body.Bytes())
	require.Equal(t, apierr.CodeTargetHostNotAllowed, env.Code)
	require.Contains(t, env.Message, "allowlist")
	require.Equal(t, "api.staging.example.test", env.Details["host"])
	require.Empty(t, env.IncidentID)
}

// An unmapped error returns an incident ID and nothing else. The cause stays in
// the log.
func TestWriteErrorHidesUnmappedCauses(t *testing.T) {
	logs := captureLogs(t)
	rec := httptest.NewRecorder()

	WriteError(context.Background(), rec, errors.New("pq: relation \"users\" does not exist"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)

	env := decodeEnvelope(t, rec.Body.Bytes())
	require.Equal(t, apierr.CodeInternal, env.Code)
	require.NotEmpty(t, env.IncidentID)
	require.NotContains(t, rec.Body.String(), "relation")

	// The same incident ID has to be findable in the log, or it is decoration.
	require.Contains(t, logs.String(), env.IncidentID)
	require.Contains(t, logs.String(), "relation")
}

// An internal error is an incident, not a message to show, whatever wrapped it.
func TestWriteErrorTreatsInternalErrorsAsIncidents(t *testing.T) {
	captureLogs(t)
	rec := httptest.NewRecorder()

	WriteError(context.Background(), rec, apierr.Internal(errors.New("driver failure")))

	env := decodeEnvelope(t, rec.Body.Bytes())
	require.NotEmpty(t, env.IncidentID)
	require.NotContains(t, rec.Body.String(), "driver failure")
}

// A deliberate 5xx keeps its code and its message. 501 and 503 are decisions with
// something actionable to say, and an incident ID would say nothing.
func TestWriteErrorKeepsDeliberateFiveHundreds(t *testing.T) {
	captureLogs(t)

	for _, err := range []error{
		apierr.NotImplemented("Test generation"),
		apierr.StorageUnreachable(errors.New("dial tcp: connection refused")),
	} {
		rec := httptest.NewRecorder()
		WriteError(context.Background(), rec, err)

		domain, ok := apierr.As(err)
		require.True(t, ok)

		env := decodeEnvelope(t, rec.Body.Bytes())
		require.Equal(t, domain.Status, rec.Code)
		require.Equal(t, domain.Code, env.Code)
		require.Equal(t, domain.Message, env.Message)
		require.Empty(t, env.IncidentID)

		// The cause still stays on this side of the boundary.
		require.NotContains(t, rec.Body.String(), "connection refused")
	}
}

func TestWriteErrorSetsRetryAfter(t *testing.T) {
	captureLogs(t)
	rec := httptest.NewRecorder()

	WriteError(context.Background(), rec, apierr.AccountLocked(900))

	require.Equal(t, http.StatusLocked, rec.Code)
	require.Equal(t, "900", rec.Header().Get("Retry-After"))
}

func TestCorrelationIDIsGeneratedAndEchoed(t *testing.T) {
	var seen string
	handler := CorrelationID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.CorrelationID(r.Context())
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	require.NotEmpty(t, seen)
	require.Equal(t, seen, rec.Header().Get(CorrelationHeader))
}

func TestCorrelationIDIsAcceptedFromTheCaller(t *testing.T) {
	var seen string
	handler := CorrelationID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = logging.CorrelationID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(CorrelationHeader, "from-ci-run-42")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, "from-ci-run-42", seen)
	require.Equal(t, "from-ci-run-42", rec.Header().Get(CorrelationHeader))
}

func TestRecovererTurnsAPanicIntoAnIncident(t *testing.T) {
	captureLogs(t)

	handler := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	})

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotEmpty(t, decodeEnvelope(t, rec.Body.Bytes()).IncidentID)
}

// http.ErrAbortHandler is the server's own signal and must keep travelling.
func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	captureLogs(t)

	handler := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	require.Panics(t, func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func TestRequestLoggerRecordsStatusAndKeepsFlushWorking(t *testing.T) {
	logs := captureLogs(t)

	handler := RequestLogger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("queued"))

		// SSE depends on this working through the wrapper.
		require.NoError(t, http.NewResponseController(w).Flush())
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil))

	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Contains(t, logs.String(), `"status":202`)
	require.Contains(t, logs.String(), `"bytes":6`)
	require.Contains(t, logs.String(), `"path":"/api/v1/jobs"`)
}

func TestRequireAuth(t *testing.T) {
	captureLogs(t)

	handler := RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, apierr.CodeUnauthenticated, decodeEnvelope(t, rec.Body.Bytes()).Code)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: uuid.New(), Email: "max@hyscaler.test", Role: role.Viewer,
	}))

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRequireRole(t *testing.T) {
	captureLogs(t)

	handler := RequireRole(role.QALead, role.Admin)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

	tests := map[role.Role]int{
		role.Admin:      http.StatusNoContent,
		role.QALead:     http.StatusNoContent,
		role.QAEngineer: http.StatusForbidden,
		role.Viewer:     http.StatusForbidden,
	}

	for r, wantStatus := range tests {
		t.Run(string(r), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req = req.WithContext(WithPrincipal(req.Context(), Principal{
				UserID: uuid.New(), Role: r,
			}))

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, wantStatus, rec.Code)
		})
	}
}

// The role set is explicit, not a floor, so a hypothetical future role cannot
// inherit access by outranking QA Engineer.
func TestRequireRoleIsASetNotAFloor(t *testing.T) {
	captureLogs(t)

	handler := RequireRole(role.QAEngineer)(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req = req.WithContext(WithPrincipal(req.Context(), Principal{
		UserID: uuid.New(), Role: role.Admin,
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code,
		"listing QAEngineer alone must not implicitly admit Admin")
}

type fakeAccessChecker struct {
	allow map[uuid.UUID]bool
}

func (f fakeAccessChecker) CheckAccess(_ context.Context, projectID uuid.UUID, _ Principal) error {
	if f.allow[projectID] {
		return nil
	}
	return apierr.NotProjectMember()
}

func TestRequireProjectMembership(t *testing.T) {
	captureLogs(t)

	allowed := uuid.New()
	denied := uuid.New()
	checker := fakeAccessChecker{allow: map[uuid.UUID]bool{allowed: true}}

	router := chi.NewRouter()
	router.Route("/projects/{projectID}", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := WithPrincipal(req.Context(), Principal{UserID: uuid.New(), Role: role.QAEngineer})
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Use(RequireProjectMembership(checker))
		r.Get("/test-cases", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	tests := map[string]struct {
		path       string
		wantStatus int
		wantCode   string
	}{
		"member": {
			path: "/projects/" + allowed.String() + "/test-cases", wantStatus: http.StatusNoContent,
		},
		"not a member": {
			path:       "/projects/" + denied.String() + "/test-cases",
			wantStatus: http.StatusForbidden, wantCode: apierr.CodeNotProjectMember,
		},
		// A malformed ID must look exactly like a project you cannot see.
		"malformed id": {
			path:       "/projects/not-a-uuid/test-cases",
			wantStatus: http.StatusNotFound, wantCode: apierr.CodeProjectNotFound,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			require.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantCode != "" {
				require.Equal(t, tc.wantCode, decodeEnvelope(t, rec.Body.Bytes()).Code)
			}
		})
	}
}

func TestMaxBytes(t *testing.T) {
	handler := MaxBytes(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("tiny"))))
	require.Equal(t, http.StatusNoContent, rec.Code)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/",
		bytes.NewReader(bytes.Repeat([]byte("x"), 64))))
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestMustCurrentUserPanicsWithoutAPrincipal(t *testing.T) {
	require.Panics(t, func() { MustCurrentUser(context.Background()) })

	p := Principal{UserID: uuid.New(), Role: role.Admin}
	require.Equal(t, p, MustCurrentUser(WithPrincipal(context.Background(), p)))
}
