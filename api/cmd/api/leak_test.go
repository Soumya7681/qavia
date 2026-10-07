package main

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/role"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Standing test (BE-0.29.4, requirements.md 9 criterion 10): every place the API
// accepts a credential, write one, then read back every surface that could echo it.
// The harness fails the test at cleanup if any response body or log line carries
// one of them. A new credential-bearing endpoint belongs in this test.
func TestSecretsNeverLeaveThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	// Each value is shaped like the real thing, so the pattern check is exercised as
	// well as the exact match.
	planted := map[string]string{
		"notifications.smtp_password": "smtp-password-DO-NOT-LEAK-0001",
		"notifications.slack_token":   "xoxb-0000000000-DO-NOT-LEAK-0002",
		"jira.api_token":              "jira-token-DO-NOT-LEAK-0003",
		"storage.access_key":          "AKIAIOSFODNN7LEAK004",
		"storage.secret_key":          "storage-secret-DO-NOT-LEAK-0005",
	}
	projectPlanted := map[string]string{
		"targets.auth_credential": "target-credential-DO-NOT-LEAK-0006",
		"repo.access_token":       "glpat-DO-NOT-LEAK-0007-abcdefghij",
		"ui.login_password":       "ui-password-DO-NOT-LEAK-0008",
		"triggers.webhook_token":  "webhook-token-DO-NOT-LEAK-0009",
	}
	const (
		providerKey   = "sk-ant-api03-DO-NOT-LEAK-0010-abcdefghijklmnop"
		mcpCredential = "mcp-credential-DO-NOT-LEAK-0011"
		repoToken     = "ghp_DONOTLEAK0012abcdefghijklmnopqrstuv"
	)
	for _, v := range planted {
		h.secret(v)
	}
	for _, v := range projectPlanted {
		h.secret(v)
	}
	h.secret(providerKey)
	h.secret(mcpCredential)
	h.secret(repoToken)

	resp := h.do(admin, http.MethodPost, "/api/v1/projects", map[string]any{"name": "leak check"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, resp.Text())
	projectID := decode[api.Project](t, resp).Id.String()

	// Settings, global and project scoped.
	var changes []map[string]any
	for key, value := range planted {
		changes = append(changes, map[string]any{"key": key, "scope": "global", "value": value})
	}
	for key, value := range projectPlanted {
		changes = append(changes, map[string]any{
			"key": key, "scope": "project", "projectID": projectID, "value": value,
		})
	}
	resp = h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{"changes": changes})
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	// An AI provider credential.
	resp = h.do(admin, http.MethodPost, "/api/v1/ai/providers", map[string]any{
		"name": "leak check", "kind": "anthropic",
		"credentials": map[string]string{"api_key": providerKey},
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, resp.Text())

	// An MCP server credential.
	resp = h.do(admin, http.MethodPost, "/api/v1/mcp/servers", map[string]any{
		"name": "leak check", "scope": "global", "transport": "http",
		"url": "https://mcp.example.test/mcp", "credential": mcpCredential,
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, resp.Text())

	// A repository token.
	resp = h.do(admin, http.MethodPut, "/api/v1/projects/"+projectID+"/repository", map[string]any{
		"url": "https://github.com/hyscaler/example.git", "token": repoToken,
	})
	require.Less(t, resp.StatusCode, 300, resp.Text())

	// Now every surface that reads any of it back. The harness scans them all.
	for _, path := range []string{
		"/api/v1/settings",
		"/api/v1/settings?projectID=" + projectID,
		"/api/v1/settings/registry",
		"/api/v1/ai/providers",
		"/api/v1/mcp/servers",
		"/api/v1/projects/" + projectID + "/repository",
		"/api/v1/integrations",
		"/api/v1/audit",
		"/api/v1/projects/" + projectID + "/jobs",
	} {
		resp = h.do(admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", path, resp.Text())
	}
}

// The detector itself is tested, or a leak check that silently scans nothing would
// pass forever. This plants a registered secret in a real response, through the
// real router, and requires the harness to report it.
func TestLeakDetectorCatchesASecretInAResponse(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	// A user's name is not a secret, which is what makes it a safe way to put a
	// chosen value into a response body. Registering it simulates a handler that
	// returned a credential.
	const planted = "planted-secret-value-0123456789"
	user := h.seedUser("plant@hyscaler.test", role.Viewer)
	_, err := h.db.Pool().Exec(t.Context(), `UPDATE users SET name = $1 WHERE id = $2`, planted, user.ID)
	require.NoError(t, err)
	h.secret(planted)

	resp := h.do(admin, http.MethodGet, "/api/v1/users", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Text(), planted, "precondition: the response carries the planted value")

	leaks := h.leaks()
	require.NotEmpty(t, leaks, "a registered secret in a response must be reported")
	require.Contains(t, leaks[0], "response GET /api/v1/users 200")
	require.NotContains(t, leaks[0], planted, "the report must not print the secret it found")

	h.forgetCaptures()
}

// A credential nobody registered is still caught by its shape, here in a log line
// under a key the redactor does not treat as sensitive.
func TestLeakDetectorCatchesACredentialShapedValueInALog(t *testing.T) {
	h := newHarness(t)

	slog.Info("innocent looking line", "note", "sk-proj-abcdefghijklmnopqrstuvwxyz0123")

	leaks := h.leaks()
	require.NotEmpty(t, leaks)
	require.Contains(t, leaks[0], "provider API key")

	h.forgetCaptures()
}
