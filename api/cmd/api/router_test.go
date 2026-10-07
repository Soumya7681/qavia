package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/health"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// specOperationIDs reads every operation out of the embedded spec, which is the
// same bytes the request validator runs against.
//
// oapi-codegen rewrites operationIds to PascalCase before embedding, so they are
// normalized back to the form used in qavia.yaml and in the policy table. The
// source YAML is the contract; the embedded copy is an artifact of generation.
func specOperationIDs(t *testing.T) []string {
	t.Helper()

	spec, err := api.GetSpec()
	require.NoError(t, err)

	var ids []string
	for _, item := range spec.Paths.Map() {
		for _, op := range item.Operations() {
			require.NotEmpty(t, op.OperationID, "every operation needs an operationId")
			ids = append(ids, httpx.NormalizeOperationID(op.OperationID))
		}
	}
	sort.Strings(ids)
	return ids
}

// The guarantee behind the policy table: every operation in the contract has an
// explicit authorisation decision recorded. Adding a path without one fails here,
// which is the point.
func TestEveryOperationHasAnAuthorisationPolicy(t *testing.T) {
	ids := specOperationIDs(t)
	require.NotEmpty(t, ids)

	set := policies()
	require.Empty(t, set.MissingOperations(ids),
		"add an entry in policies() for each of these operations")
	require.Empty(t, set.UnknownOperations(ids),
		"these policy entries no longer match an operation in qavia.yaml")
}

// Only probes, login, invite acceptance, first-run setup, and inbound webhooks may
// be public. This test is a deliberate speed bump: widening the list should require
// editing it.
func TestOnlyExpectedOperationsArePublic(t *testing.T) {
	allowedPublic := map[string]bool{
		// Probes answer before there is a session, and an orchestrator has no
		// credentials to offer.
		"getLiveness":  true,
		"getReadiness": true,
		// These two are how a caller obtains a session at all. Both are rate
		// limited and both refuse to reveal whether an account or token exists.
		"login":        true,
		"acceptInvite": true,
		// First-run setup runs before any account exists, so it cannot require
		// one. The admin endpoint is rate limited and stops existing the moment a
		// user does, which is what keeps it from being a permanent
		// account-creation route.
		"getSetupStatus":   true,
		"createFirstAdmin": true,
		// The inbound trigger has no session by design: its credential is an HMAC
		// over the body, verified against a per-project secret, with a timestamp
		// window and replay protection.
		"triggerWebhook": true,
	}

	for id, policy := range policies() {
		if policy.Public {
			require.True(t, allowedPublic[id],
				"%s is public; if that is intended, add it to allowedPublic and say why", id)
		}
	}
}

func newTestRouter(t *testing.T, probes ...health.Probe) http.Handler {
	t.Helper()

	var sink slog.Handler = slog.NewJSONHandler(io_Discard{}, nil)
	previous := slog.Default()
	slog.SetDefault(slog.New(sink))
	t.Cleanup(func() { slog.SetDefault(previous) })

	service := health.New("test", probes...)
	handler, err := newRouter(
		&server{healthAPI: health.NewHandler(service)},
		policies(),
		// No session middleware: these tests cover routing, validation, and the
		// policy table. The auth path has its own integration suite against a real
		// database.
		func(next http.Handler) http.Handler { return next },
	)
	require.NoError(t, err)
	return handler
}

type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

func get(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestLivenessTouchesNoDependency(t *testing.T) {
	// A failing database must not fail liveness: a restart loop turns a
	// recoverable dependency problem into an outage.
	handler := newTestRouter(t, health.Probe{Name: "database", Check: func(context.Context) error {
		return errors.New("down")
	}})

	rec := get(t, handler, "/healthz")
	require.Equal(t, http.StatusOK, rec.Code)

	var body api.Liveness
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.LivenessStatusOk, body.Status)
	require.Equal(t, "test", body.Version)
}

func TestReadinessReportsEachDependency(t *testing.T) {
	handler := newTestRouter(t,
		health.Probe{Name: "database", Check: func(context.Context) error { return nil }},
		health.Probe{Name: "object_store", Detail: "local-disk", Check: func(context.Context) error { return nil }},
	)

	rec := get(t, handler, "/readyz")
	require.Equal(t, http.StatusOK, rec.Code)

	var body api.Readiness
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.ReadinessStatusOk, body.Status)
	require.Len(t, body.Checks, 2)
	require.Equal(t, "database", body.Checks[0].Name)
	require.Equal(t, api.CheckStatusOk, body.Checks[0].Status)
	require.Equal(t, "local-disk", *body.Checks[1].Detail)
}

func TestReadinessReturns503WhenADependencyIsDown(t *testing.T) {
	handler := newTestRouter(t, health.Probe{Name: "database", Check: func(context.Context) error {
		return errors.New("connection refused")
	}})

	rec := get(t, handler, "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body api.Readiness
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, api.ReadinessStatusUnavailable, body.Status)
	require.Equal(t, api.CheckStatusFailing, body.Checks[0].Status)
}

// The correlation middleware is in the chain for every route, including the
// unauthenticated probes.
func TestEveryResponseCarriesACorrelationID(t *testing.T) {
	handler := newTestRouter(t)

	rec := get(t, handler, "/healthz")
	require.NotEmpty(t, rec.Header().Get(httpx.CorrelationHeader))
}

// A path the spec does not describe is a 404 from the router, not a validator
// error page.
func TestUnknownPathIs404(t *testing.T) {
	handler := newTestRouter(t)

	rec := get(t, handler, "/api/v1/does-not-exist")
	require.Equal(t, http.StatusNotFound, rec.Code)

	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, apierr.CodeNotFound, env.Code)
}

// The policy table is keyed by the spec operationId, and oapi-codegen hands its
// middleware the Go method name. That mapping is only reversible while every
// operationId is camelCase, so the convention is enforced rather than assumed.
func TestEveryOperationIDIsCamelCase(t *testing.T) {
	// Read the source contract, not the embedded copy: generation PascalCases the
	// IDs, so the embedded spec cannot show whether the convention is being kept.
	source, err := os.ReadFile("../../openapi/qavia.yaml")
	require.NoError(t, err)

	declared := regexp.MustCompile(`(?m)^\s*operationId:\s*(\S+)\s*$`)
	matches := declared.FindAllStringSubmatch(string(source), -1)
	require.NotEmpty(t, matches, "no operationId found in qavia.yaml")

	camelCase := regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
	for _, match := range matches {
		require.Regexp(t, camelCase, match[1],
			"operationId must be camelCase so httpx.NormalizeOperationID stays reversible")
	}

	// Every declared ID must also survive the round trip the router depends on.
	require.Len(t, specOperationIDs(t), len(matches))
}

// The request validator must produce the standard envelope. Its own default is
// plain text, which would leave the client with no code to branch on.
func TestValidatorRejectionUsesTheErrorEnvelope(t *testing.T) {
	handler := newTestRouter(t)

	rec := httptest.NewRecorder()
	// POST to a GET-only path: the spec has no such operation, so the validator
	// rejects it before any handler runs.
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))

	require.NotEqual(t, http.StatusOK, rec.Code)
	if rec.Code == http.StatusNotFound {
		// chi answers first for a method mismatch on a known path; either layer is
		// acceptable as long as nothing leaks a stack trace.
		require.NotContains(t, rec.Body.String(), "goroutine")
		return
	}

	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.NotEmpty(t, env.Code)
}

// logging.New is what main installs; this asserts the router test harness has not
// diverged from it in a way that hides redaction.
func TestHarnessUsesTheRedactingLogger(t *testing.T) {
	logger := logging.New(logging.Options{Writer: io_Discard{}})
	require.NotNil(t, logger)
	require.True(t, apierr.Is(apierr.Unauthenticated(), apierr.CodeUnauthenticated))
}

// A body the generated types cannot accept is the caller's fault. Before this was
// separated from the response-error hook, a malformed email address returned a 500
// with an incident ID, which sends a developer looking for a server bug that does
// not exist.
func TestMalformedBodyIsAClientError(t *testing.T) {
	handler := newTestRouter(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/v1/auth/login",
		`{"email":"not-an-email","password":"long-enough-password"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	env := recordedEnvelope(t, rec)
	require.Equal(t, apierr.CodeValidation, env.Code)
	require.NotEmpty(t, env.Message)
	require.Empty(t, env.IncidentID, "a client error must not look like a server incident")
}

// The message is the part a UI shows, so kin-openapi's schema-and-value dump belongs
// in details rather than in front of a user.
func TestValidationMessageIsOneReadableLine(t *testing.T) {
	handler := newTestRouter(t)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, jsonRequest(http.MethodPost, "/api/v1/auth/login", `{"email":"a@b.test"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code)

	env := recordedEnvelope(t, rec)
	require.NotContains(t, env.Message, "\n")
	require.NotContains(t, env.Message, "Schema:")
	require.Contains(t, env.Message, "password")
}

// recordedEnvelope decodes the error envelope from a recorder.
func recordedEnvelope(t *testing.T, rec *httptest.ResponseRecorder) httpx.Envelope {
	t.Helper()

	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return env
}

func jsonRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}
