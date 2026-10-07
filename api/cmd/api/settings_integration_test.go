package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// The registry endpoint is seam item S2: the settings UI renders entirely from it, so
// it has to carry everything a form needs. A field missing here means a screen that
// cannot be built without hardcoding knowledge of a key.
func TestRegistryEndpointCarriesEverythingAFormNeeds(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodGet, "/api/v1/settings/registry", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	body := decode[api.SettingsRegistry](t, resp)
	require.NotEmpty(t, body.Entries)

	for _, entry := range body.Entries {
		require.NotEmpty(t, entry.Key)
		require.NotEmpty(t, entry.Category, entry.Key)
		require.NotEmpty(t, entry.Label, entry.Key)
		require.NotEmpty(t, entry.Kind, entry.Key)
		require.NotEmpty(t, entry.Scopes, entry.Key)
		require.NotEmpty(t, entry.MinRole, entry.Key)

		// The schema is what the client converts to its own validator, so it must be
		// present and typed for every entry.
		require.NotEmpty(t, entry.Schema["type"], entry.Key)

		if entry.IsSecret {
			require.Nil(t, entry.Default, "%s is a secret and must ship no default", entry.Key)
		}
	}
}

// A Viewer sees their own preferences and nothing they could not write anyway.
func TestRegistryHidesEntriesAboveTheCallersRole(t *testing.T) {
	h := newHarness(t)

	adminEntries := decode[api.SettingsRegistry](t,
		h.do(h.signedIn("admin@hyscaler.test", role.Admin),
			http.MethodGet, "/api/v1/settings/registry", nil)).Entries

	viewerEntries := decode[api.SettingsRegistry](t,
		h.do(h.signedIn("viewer@hyscaler.test", role.Viewer),
			http.MethodGet, "/api/v1/settings/registry", nil)).Entries

	require.Less(t, len(viewerEntries), len(adminEntries))

	for _, entry := range viewerEntries {
		// Everything a Viewer can see is either theirs to set or within their role.
		userScoped := false
		for _, scope := range entry.Scopes {
			if scope == api.SettingScopeUser {
				userScoped = true
			}
		}
		require.True(t, userScoped || entry.MinRole == api.Viewer, entry.Key)
	}
}

// Adding a setting on the server must need no frontend change. The strongest form of
// that claim: every declared entry appears in the response, so nothing is reachable
// only through knowledge a client would have to hardcode.
func TestEveryDeclaredSettingIsServedToAnAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	body := decode[api.SettingsRegistry](t,
		h.do(admin, http.MethodGet, "/api/v1/settings/registry", nil))

	served := make(map[string]bool, len(body.Entries))
	for _, entry := range body.Entries {
		served[entry.Key] = true
	}

	for _, key := range settings.Default().Keys() {
		require.True(t, served[key], "%s is declared but not served", key)
	}
}

func TestGetSettingsReportsResolvedValuesAndTheirSource(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodGet, "/api/v1/settings", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	values := byKey(decode[api.SettingValues](t, resp))
	require.True(t, values["storage.retention_days"].FromDefault)
	require.EqualValues(t, 90, values["storage.retention_days"].Value)

	// Write, then confirm the source changes from default to global.
	resp = h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.retention_days", "scope": "global", "value": 30},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	values = byKey(decode[api.SettingValues](t, h.do(admin, http.MethodGet, "/api/v1/settings", nil)))
	require.False(t, values["storage.retention_days"].FromDefault)
	require.Equal(t, api.SettingScopeGlobal, values["storage.retention_days"].Source)
	require.EqualValues(t, 30, values["storage.retention_days"].Value)
}

// The whole point of the framework: a value written through the API is the value the
// process then uses, with no restart and no deploy.
func TestAWriteTakesEffectImmediately(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "uploads.max_bytes", "scope": "global", "value": 1024},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	current, err := h.settings.Int64(context.Background(), "uploads.max_bytes", settings.Target{})
	require.NoError(t, err)
	require.EqualValues(t, 1024, current)
}

func TestSecretIsWrittenAndReadBackAsAHintOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	const token = "xoxb-DO-NOT-LOG-ME-0123456789"

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "notifications.slack_token", "scope": "global", "value": token},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())
	require.NotContains(t, resp.Text(), token, "the write response must not echo the secret")

	resp = h.do(admin, http.MethodGet, "/api/v1/settings", nil)
	require.NotContains(t, resp.Text(), token)

	value := byKey(decode[api.SettingValues](t, resp))["notifications.slack_token"]
	require.Nil(t, value.Value, "a secret never carries its value")
	require.NotNil(t, value.Secret)
	require.True(t, value.Secret.IsSet)
	require.NotNil(t, value.Secret.Hint)
	require.Equal(t, "xoxb…6789", *value.Secret.Hint)

	// The plaintext is still usable server-side, at the point of use.
	plaintext, found, err := h.settings.Secret(context.Background(),
		"notifications.slack_token", settings.Target{})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, token, plaintext)
}

func TestUnsetSecretReportsNotSetRatherThanErroring(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	value := byKey(decode[api.SettingValues](t,
		h.do(admin, http.MethodGet, "/api/v1/settings", nil)))["notifications.slack_token"]

	require.NotNil(t, value.Secret)
	require.False(t, value.Secret.IsSet)
	require.True(t, value.FromDefault)
}

func TestWriteRejectsAnInvalidValueWithAnActionableMessage(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.retention_days", "scope": "global", "value": 99999},
		},
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	env := decode[httpx.Envelope](t, resp)
	require.Equal(t, apierr.CodeSettingInvalid, env.Code)
	require.Contains(t, env.Message, "between 1 and 3650")
	require.Equal(t, "storage.retention_days", env.Details["key"])
}

func TestWriteRejectsAnUnknownKey(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "runner.invented_key", "scope": "global", "value": 1},
		},
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, apierr.CodeSettingUnknownKey, decode[httpx.Envelope](t, resp).Code)
}

// One invalid value rejects the whole request. Applying half a form leaves an admin
// guessing which half took effect.
func TestABatchIsRejectedWholesale(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.bucket", "scope": "global", "value": "qavia-prod"},
			{"key": "storage.retention_days", "scope": "global", "value": "not a number"},
		},
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// The valid change in the same request must not have been applied.
	bucket, err := h.settings.String(context.Background(), "storage.bucket", settings.Target{})
	require.NoError(t, err)
	require.Equal(t, "qavia", bucket, "the default, so nothing from the batch was written")
}

func TestViewerCannotWriteAnAdminSetting(t *testing.T) {
	h := newHarness(t)
	viewer := h.signedIn("viewer@hyscaler.test", role.Viewer)

	resp := h.do(viewer, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.retention_days", "scope": "global", "value": 30},
		},
	})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	env := decode[httpx.Envelope](t, resp)
	require.Equal(t, apierr.CodeSettingRoleTooLow, env.Code)
	require.Equal(t, "Admin", env.Details["requiredRole"])
}

// Any role may set their own preferences, which is why the endpoint is open to every
// signed-in user and the per-key minimum role is enforced inside the service.
func TestViewerCanSetTheirOwnPreference(t *testing.T) {
	h := newHarness(t)
	viewer := h.signedIn("viewer@hyscaler.test", role.Viewer)

	resp := h.do(viewer, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "preferences.theme", "scope": "user", "value": "dark"},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Text())

	values := byKey(decode[api.SettingValues](t, h.do(viewer, http.MethodGet, "/api/v1/settings", nil)))
	require.Equal(t, api.SettingScopeUser, values["preferences.theme"].Source)
	require.EqualValues(t, "dark", values["preferences.theme"].Value)
}

// A user-scoped write defaults to the caller, so the common case cannot accidentally
// target somebody else.
func TestUserScopedWriteDefaultsToTheCaller(t *testing.T) {
	h := newHarness(t)

	victim := h.seedUser("victim@hyscaler.test", role.QAEngineer)
	viewer := h.signedIn("viewer@hyscaler.test", role.Viewer)

	resp := h.do(viewer, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "preferences.theme", "scope": "user", "value": "light"},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// The other user is untouched.
	theme, err := h.settings.String(context.Background(), "preferences.theme",
		settings.Target{UserID: &victim.ID})
	require.NoError(t, err)
	require.Equal(t, "system", theme, "the default, so nobody wrote to this user")
}

func TestClearingAnOverrideThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.region", "scope": "global", "value": "eu-west-1"},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp = h.do(admin, http.MethodDelete, "/api/v1/settings/storage.region?scope=global", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, resp.Text())

	values := byKey(decode[api.SettingValues](t, h.do(admin, http.MethodGet, "/api/v1/settings", nil)))
	require.True(t, values["storage.region"].FromDefault)
	require.EqualValues(t, "ap-south-1", values["storage.region"].Value)
}

// Every settings change is audited, and a secret's audit row holds no value.
func TestSettingsChangesAreAudited(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	const token = "xoxb-audited-DO-NOT-LOG-12345678"
	h.do(admin, http.MethodPut, "/api/v1/settings", map[string]any{
		"changes": []map[string]any{
			{"key": "storage.retention_days", "scope": "global", "value": 45},
			{"key": "notifications.slack_token", "scope": "global", "value": token},
		},
	})

	entries, err := h.db.Queries().ListAuditEntries(context.Background(),
		dbgen.ListAuditEntriesParams{PageSize: 50})
	require.NoError(t, err)

	actions := map[string]int{}
	for _, entry := range entries {
		actions[entry.Action]++
		require.NotContains(t, string(entry.Detail), token)
	}
	require.Equal(t, 1, actions["setting_changed"])
	require.Equal(t, 1, actions["secret_rotated"], "a secret change is recorded as a rotation")
}

func byKey(page api.SettingValues) map[string]api.SettingValue {
	out := make(map[string]api.SettingValue, len(page.Values))
	for _, value := range page.Values {
		out[value.Key] = value
	}
	return out
}
