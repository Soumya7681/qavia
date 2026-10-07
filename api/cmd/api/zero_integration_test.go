package main

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Standing test (BE-0.29.5, requirements.md 9 criterion 0): a fresh install with
// no external integration configured is a complete product. No Jira, Slack, SMTP,
// GitHub, S3, or MCP server: the built-in of every capability carries the flow.
//
// It grows through the phases, and BE-10.8 is its final run. If it fails, some
// path reached past the capability registry to a vendor directly, and that is the
// bug, not this test.
func TestZeroIntegrationInstallIsComplete(t *testing.T) {
	h := newHarness(t)
	anonymous := h.client()

	// Nothing is configured, and nothing has to be.
	_, err := h.db.Pool().Exec(context.Background(), `DELETE FROM settings`)
	require.NoError(t, err)
	var mcpServers int
	require.NoError(t, h.db.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM mcp_servers`).Scan(&mcpServers))
	require.Zero(t, mcpServers)

	// The process is ready on Postgres, Redis, and local disk alone.
	resp := h.do(anonymous, http.MethodGet, "/readyz", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	// First-run setup, entirely through the API (criterion 1).
	resp = h.do(anonymous, http.MethodGet, "/api/v1/setup/status", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())
	status := decode[api.SetupStatus](t, resp)
	require.False(t, status.AdminExists)
	require.True(t, status.StorageReachable, "the built-in object store needs no configuration")

	resp = h.do(anonymous, http.MethodPost, "/api/v1/setup/admin", map[string]any{
		"email": "admin@hyscaler.test", "name": "Admin", "password": testPassword,
	})
	require.Less(t, resp.StatusCode, 300, resp.Text())

	admin := h.client()
	resp = h.login(admin, "admin@hyscaler.test")
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	// Every capability reports a built-in, and nothing external is active.
	resp = h.do(admin, http.MethodGet, "/api/v1/integrations", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())
	builtinFor := map[string]bool{}
	for _, item := range decode[api.IntegrationList](t, resp).Items {
		if item.Builtin {
			require.Equal(t, api.IntegrationState("builtin"), item.State, "%s/%s", item.Capability, item.Id)
			builtinFor[item.Capability] = true
			continue
		}
		// The signed webhook trigger is inbound: it is a URL this platform serves,
		// not a platform it calls, so it is active with nothing configured.
		if item.Capability == "runtrigger" && item.Id == "webhook" {
			continue
		}
		require.Equal(t, api.IntegrationState("not_configured"), item.State,
			"%s/%s is active on an install where nothing was configured", item.Capability, item.Id)
	}
	for _, capability := range []string{"notifier", "defecttracker", "sourceprovider", "runtrigger", "objectstore"} {
		require.True(t, builtinFor[capability], "%s has no built-in", capability)
	}

	// Create a project, upload a specification, submit work (criterion 2).
	resp = h.do(admin, http.MethodPost, "/api/v1/projects", map[string]any{"name": "zero integration"})
	require.Equal(t, http.StatusCreated, resp.StatusCode, resp.Text())
	projectID := decode[api.Project](t, resp).Id.String()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	require.NoError(t, form.WriteField("kind", "openapi"))
	part, err := form.CreateFormFile("file", "spec.yaml")
	require.NoError(t, err)
	_, err = part.Write([]byte(minimalSpec))
	require.NoError(t, err)
	require.NoError(t, form.Close())

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		h.server.URL+"/api/v1/projects/"+projectID+"/artifacts", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp = h.send(admin, req)
	require.Less(t, resp.StatusCode, 300, resp.Text())

	resp = h.do(admin, http.MethodPost, "/api/v1/projects/"+projectID+"/jobs", map[string]any{"chain": "noop"})
	require.Less(t, resp.StatusCode, 300, resp.Text())

	// The in-app channel and the built-in tracker answer with nothing configured.
	for _, path := range []string{
		"/api/v1/notifications",
		"/api/v1/projects/" + projectID + "/jobs",
		"/api/v1/projects/" + projectID + "/defects",
		"/api/v1/mcp/servers",
	} {
		resp = h.do(admin, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", path, resp.Text())
	}
}

const minimalSpec = `openapi: 3.0.3
info:
  title: Zero integration
  version: "1.0"
paths:
  /ping:
    get:
      responses:
        "200":
          description: pong
`
