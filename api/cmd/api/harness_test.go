package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/platform/config"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	"github.com/hyscaler/qavia/api/internal/store/storetest"
)

// These are API integration tests: the router main builds, the real middleware
// chain, a real Postgres, and a real Redis. Handlers are not unit tested
// separately, because the parts most likely to break are the chain and the session
// lifecycle, and a mock of either would just agree with itself
// (backend-standards.md 14).
//
// Every harness is also a secret-leakage test (BE-0.29). It captures every response
// body and every log line the process writes, and fails the test at cleanup if any
// of them carries a secret the test planted or anything shaped like a credential.
// The check runs on every integration test rather than in one dedicated test,
// because a leak is most likely on a path nobody wrote a leak test for.

const testPassword = "correct-horse-battery-staple"

// testEncryptionKey is the bootstrap key the harness boots with. It is a secret
// like any other: it must never appear in a response or a log.
var testEncryptionKey = []byte("0123456789abcdef0123456789abcdef")

type harness struct {
	t        *testing.T
	server   *httptest.Server
	db       *store.DB
	settings *settings.Service

	capture *capture
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, capture: &capture{}}

	// Registered first so it runs last, after the server and the app have shut
	// down and written their final log lines.
	t.Cleanup(func() {
		for _, leak := range h.leaks() {
			t.Errorf("secret leak: %s", leak)
		}
	})

	h.secret(testPassword)
	h.secret(string(testEncryptionKey))
	h.secret(base64.StdEncoding.EncodeToString(testEncryptionKey))

	databaseURL := storetest.URL(t)
	redisURL := storetest.Redis(t)

	// Local disk is the default object store, and its default path is relative to
	// the working directory. Pointed at a temp dir so a test run does not leave
	// uploads in the source tree.
	seed := storetest.New(t)
	_, err := seed.Pool().Exec(context.Background(),
		`INSERT INTO settings (scope, key, value) VALUES ('global', 'storage.local_path', to_jsonb($1::text))`,
		t.TempDir())
	require.NoError(t, err)

	appURL, err := url.Parse("http://qavia.test")
	require.NoError(t, err)

	cfg := config.Config{
		DatabaseURL:   databaseURL,
		RedisURL:      redisURL,
		EncryptionKey: testEncryptionKey,
		AppURL:        appURL,
		Port:          0,
		// Development, so the session cookie is not Secure: httptest serves plain
		// HTTP, and a Secure cookie would never be sent back.
		Env: config.EnvDevelopment,
	}

	// The logger main installs, writing into the capture. Debug, so the leak check
	// sees more than production would ever print.
	level := new(slog.LevelVar)
	level.Set(slog.LevelDebug)
	logger := logging.New(logging.Options{Writer: h.capture, Level: level})
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })

	built, err := build(context.Background(), cfg, logger, level)
	require.NoError(t, err)
	t.Cleanup(built.close)

	// build reads the log level from settings. The capture wants everything.
	level.Set(slog.LevelDebug)

	srv := httptest.NewServer(h.capture.middleware(built.handler))
	t.Cleanup(srv.Close)

	h.server = srv
	h.db = built.db
	h.settings = built.settings
	return h
}

// secret registers a value that must never leave the process: not in a response
// body, not in a log line.
func (h *harness) secret(value string) {
	h.capture.addSecret(value)
}

// leaks reports every captured response and log line that carries a secret.
func (h *harness) leaks() []string {
	return h.capture.leaks()
}

// forgetCaptures drops everything captured so far. Only the test that proves the
// detector works has a reason to call it.
func (h *harness) forgetCaptures() {
	h.capture.reset()
}

// client returns an HTTP client with its own cookie jar, so two clients are two
// independent browsers.
func (h *harness) client() *http.Client {
	h.t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(h.t, err)
	return &http.Client{Jar: jar}
}

// seedUser creates a user with a usable password, bypassing the invitation flow.
func (h *harness) seedUser(email string, r role.Role) dbgen.User {
	h.t.Helper()

	hashed, err := auth.HashPassword(testPassword, auth.HashParams{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
	require.NoError(h.t, err)

	ctx := context.Background()
	user, err := h.db.Queries().CreateUser(ctx, dbgen.CreateUserParams{
		Email: email, Name: email, Role: dbgen.UserRole(r), Timezone: "UTC",
	})
	require.NoError(h.t, err)

	require.NoError(h.t, h.db.Queries().SetUserPassword(ctx, dbgen.SetUserPasswordParams{
		ID: user.ID, PasswordHash: &hashed,
	}))
	user.PasswordHash = &hashed
	return user
}

// apiResponse is a fully read response.
//
// The body is buffered once, so a test can assert on the status and then decode the
// same response. Passing an http.Response around invites reading the body twice,
// which silently yields nothing the second time.
type apiResponse struct {
	StatusCode int
	Header     http.Header
	Cookies    []*http.Cookie
	Body       []byte
}

func (r apiResponse) Text() string { return string(r.Body) }

func (h *harness) do(client *http.Client, method, path string, body any) apiResponse {
	h.t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(h.t, err)
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, h.server.URL+path, reader)
	require.NoError(h.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return h.send(client, req)
}

func (h *harness) send(client *http.Client, req *http.Request) apiResponse {
	h.t.Helper()

	resp, err := client.Do(req)
	require.NoError(h.t, err)
	defer func() { require.NoError(h.t, resp.Body.Close()) }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(h.t, err)

	// A session token is a bearer credential. It belongs in a Set-Cookie header and
	// nowhere else, so every one the server issues is registered as a secret.
	for _, cookie := range resp.Cookies() {
		if cookie.Value != "" {
			h.secret(cookie.Value)
		}
	}

	return apiResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Cookies:    resp.Cookies(),
		Body:       raw,
	}
}

func (h *harness) login(client *http.Client, email string) apiResponse {
	return h.do(client, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": testPassword})
}

// signedIn returns a client already holding a session for a seeded user.
func (h *harness) signedIn(email string, r role.Role) *http.Client {
	h.t.Helper()

	h.seedUser(email, r)
	client := h.client()
	resp := h.login(client, email)
	require.Equal(h.t, http.StatusOK, resp.StatusCode, resp.Text())
	return client
}

func decode[T any](t *testing.T, resp apiResponse) T {
	t.Helper()

	var out T
	require.NoError(t, json.Unmarshal(resp.Body, &out), resp.Text())
	return out
}

// ----------------------------------------------------------------- leak capture

// credentialShapes are values that are a credential whatever test planted them:
// provider key prefixes, cloud access key IDs, private keys, and password hashes.
// A planted value is matched exactly; these catch the secret nobody registered.
var credentialShapes = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"password hash", regexp.MustCompile(`\$argon2id\$v=\d+\$`)},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"provider API key", regexp.MustCompile(`\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}`)},
	{"AWS access key", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`)},
	{"GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`)},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`)},
	{"bearer header", regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`)},
}

// minSecretLength keeps a short registered value from matching by coincidence. A
// three-character secret would appear in half the responses by accident.
const minSecretLength = 8

type capturedResponse struct {
	method string
	path   string
	status int
	body   []byte
}

// capture is both the log writer and the response recorder.
type capture struct {
	mu        sync.Mutex
	logs      bytes.Buffer
	responses []capturedResponse
	secrets   []string
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logs.Write(p)
}

func (c *capture) addSecret(value string) {
	if len(value) < minSecretLength {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, known := range c.secrets {
		if known == value {
			return
		}
	}
	c.secrets = append(c.secrets, value)
}

func (c *capture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs.Reset()
	c.responses = nil
}

func (c *capture) record(r capturedResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.responses = append(c.responses, r)
}

// middleware tees every response body, including streamed ones, into the capture.
// It sits outside the whole chain, so an error page written by the router or the
// validator is scanned as well as a handler's response.
func (c *capture) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &teeWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		c.record(capturedResponse{
			method: r.Method, path: r.URL.Path, status: rec.status, body: rec.body.Bytes(),
		})
	})
}

func (c *capture) leaks() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var found []string
	check := func(where string, text string) {
		for i, secret := range c.secrets {
			if strings.Contains(text, secret) {
				found = append(found, fmt.Sprintf("%s contains registered secret #%d (%s)", where, i+1, hint(secret)))
			}
		}
		for _, shape := range credentialShapes {
			if match := shape.pattern.FindString(text); match != "" {
				found = append(found, fmt.Sprintf("%s contains something shaped like a %s (%s)", where, shape.name, hint(match)))
			}
		}
	}

	for _, r := range c.responses {
		check(fmt.Sprintf("response %s %s %d", r.method, r.path, r.status), string(r.body))
	}
	for i, line := range strings.Split(c.logs.String(), "\n") {
		if line != "" {
			check(fmt.Sprintf("log line %d", i+1), line)
		}
	}
	return found
}

// hint identifies a leaked value without printing it. The failure message ends up
// in CI output, and a leak report that leaks is the same incident twice.
func hint(value string) string {
	if len(value) <= 8 {
		return "len " + fmt.Sprint(len(value))
	}
	return fmt.Sprintf("%s…%s, len %d", value[:3], value[len(value)-2:], len(value))
}

// teeWriter copies the body as it is written, keeping Flush working so a server
// sent event stream still streams under test.
type teeWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (w *teeWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *teeWriter) Write(p []byte) (int, error) {
	w.wroteHeader = true
	w.body.Write(p)
	return w.ResponseWriter.Write(p)
}

func (w *teeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *teeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
