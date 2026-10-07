package main

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

func TestLoginLifecycle(t *testing.T) {
	h := newHarness(t)
	h.seedUser("admin@hyscaler.test", role.Admin)

	client := h.client()

	// Before signing in, a protected endpoint is refused.
	resp := h.do(client, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, apierr.CodeUnauthenticated, decode[httpx.Envelope](t, resp).Code)

	resp = h.login(client, "admin@hyscaler.test")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	user := decode[api.User](t, resp)
	require.Equal(t, api.Admin, user.Role)

	resp = h.do(client, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "admin@hyscaler.test", string(decode[api.User](t, resp).Email))

	resp = h.do(client, http.MethodPost, "/api/v1/auth/logout", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// The session row is gone, so the cookie the client still holds is useless.
	resp = h.do(client, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// An unknown account and a wrong password must be indistinguishable. Anything else
// turns the login endpoint into an account enumerator.
func TestUnknownAccountAndWrongPasswordAreIdentical(t *testing.T) {
	h := newHarness(t)
	h.seedUser("real@hyscaler.test", role.QAEngineer)

	wrongPassword := h.do(h.client(), http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "real@hyscaler.test", "password": "not-the-password"})
	wrongBody := decode[httpx.Envelope](t, wrongPassword)

	unknownAccount := h.do(h.client(), http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "ghost@hyscaler.test", "password": "not-the-password"})
	unknownBody := decode[httpx.Envelope](t, unknownAccount)

	require.Equal(t, wrongPassword.StatusCode, unknownAccount.StatusCode)
	require.Equal(t, apierr.CodeInvalidCredentials, wrongBody.Code)
	require.Equal(t, wrongBody, unknownBody)
}

// Email matching is case insensitive: the address someone types is not always the
// case it was invited with.
func TestLoginIsCaseInsensitive(t *testing.T) {
	h := newHarness(t)
	h.seedUser("Mixed.Case@hyscaler.test", role.QALead)

	resp := h.login(h.client(), "mixed.case@HYSCALER.test")
	require.Equal(t, http.StatusOK, resp.StatusCode, (resp).Text())
}

func TestLockoutAfterRepeatedFailures(t *testing.T) {
	h := newHarness(t)
	h.seedUser("locked@hyscaler.test", role.QAEngineer)

	client := h.client()

	// Failures below the limit are ordinary rejections.
	for range auth.DefaultThrottlePolicy.MaxAccountFailures - 1 {
		resp := h.do(client, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"email": "locked@hyscaler.test", "password": "wrong"})
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	}

	// The failure that reaches the limit reports the lock straight away, rather than
	// locking silently and surprising the user on their next attempt.
	resp := h.do(client, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "locked@hyscaler.test", "password": "wrong"})
	require.Equal(t, http.StatusLocked, resp.StatusCode)

	// The correct password does not help while the lock stands.
	resp = h.login(client, "locked@hyscaler.test")
	require.Equal(t, http.StatusLocked, resp.StatusCode)

	envelope := decode[httpx.Envelope](t, resp)
	require.Equal(t, apierr.CodeAccountLocked, envelope.Code)
	require.NotNil(t, envelope.Details["retryAfterSeconds"])
	require.NotEmpty(t, resp.Header.Get("Retry-After"))
}

func TestAdminCanClearALockout(t *testing.T) {
	h := newHarness(t)
	locked := h.seedUser("unlockme@hyscaler.test", role.QAEngineer)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	victim := h.client()
	for range auth.DefaultThrottlePolicy.MaxAccountFailures {
		h.do(victim, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"email": "unlockme@hyscaler.test", "password": "wrong"})
	}
	require.Equal(t, http.StatusLocked, h.login(victim, "unlockme@hyscaler.test").StatusCode)

	resp := h.do(admin, http.MethodPost, "/api/v1/users/"+locked.ID.String()+"/unlock", nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	require.Equal(t, http.StatusOK, h.login(h.client(), "unlockme@hyscaler.test").StatusCode)
}

func TestInviteAndAcceptFlow(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPost, "/api/v1/users/invite", map[string]string{
		"email": "joiner@hyscaler.test", "name": "New Joiner", "role": "qa_engineer",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode, (resp).Text())

	invitation := decode[api.Invitation](t, resp)
	require.Equal(t, "joiner@hyscaler.test", string(invitation.User.Email))

	// The link is what an admin passes on, because email is optional and a
	// zero-integration install has no way to send it.
	link, err := url.Parse(invitation.AcceptUrl)
	require.NoError(t, err)
	token := link.Query().Get("token")
	require.NotEmpty(t, token)

	// The invitee cannot sign in yet: there is no password.
	require.Equal(t, http.StatusUnauthorized, h.login(h.client(), "joiner@hyscaler.test").StatusCode)

	joiner := h.client()
	resp = h.do(joiner, http.MethodPost, "/api/v1/auth/accept-invite", map[string]string{
		"token": token, "password": testPassword, "name": "Joiner",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, (resp).Text())
	require.Equal(t, "Joiner", decode[api.User](t, resp).Name)

	// Accepting signs them in immediately.
	require.Equal(t, http.StatusOK, h.do(joiner, http.MethodGet, "/api/v1/me", nil).StatusCode)

	// Single use: the same token cannot be replayed.
	resp = h.do(h.client(), http.MethodPost, "/api/v1/auth/accept-invite", map[string]string{
		"token": token, "password": testPassword,
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, apierr.CodeInviteInvalid, decode[httpx.Envelope](t, resp).Code)
}

// An unknown token and a used one return the same error, so the endpoint cannot be
// used to discover valid tokens.
func TestUnknownInviteTokenIsIndistinguishable(t *testing.T) {
	h := newHarness(t)

	resp := h.do(h.client(), http.MethodPost, "/api/v1/auth/accept-invite", map[string]string{
		"token": "not-a-real-token", "password": testPassword,
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, apierr.CodeInviteInvalid, decode[httpx.Envelope](t, resp).Code)
}

func TestAcceptInviteEnforcesThePasswordPolicy(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodPost, "/api/v1/users/invite", map[string]string{
		"email": "weak@hyscaler.test", "role": "viewer",
	})
	invitation := decode[api.Invitation](t, resp)
	link, err := url.Parse(invitation.AcceptUrl)
	require.NoError(t, err)

	resp = h.do(h.client(), http.MethodPost, "/api/v1/auth/accept-invite", map[string]string{
		"token": link.Query().Get("token"), "password": "short",
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	envelope := decode[httpx.Envelope](t, resp)
	require.Equal(t, apierr.CodePasswordWeak, envelope.Code)
	require.Contains(t, envelope.Message, "at least 12 characters")
}

func TestInviteRejectsADuplicateEmail(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)
	h.seedUser("taken@hyscaler.test", role.Viewer)

	resp := h.do(admin, http.MethodPost, "/api/v1/users/invite", map[string]string{
		"email": "taken@hyscaler.test", "role": "viewer",
	})
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.Equal(t, apierr.CodeEmailAlreadyTaken, decode[httpx.Envelope](t, resp).Code)
}

// The policy table in action: a non-admin is refused, and the refusal names the role
// that is required.
func TestRolesAreEnforcedOnUserAdministration(t *testing.T) {
	h := newHarness(t)
	target := h.seedUser("target@hyscaler.test", role.Viewer)

	for _, r := range []role.Role{role.Viewer, role.QAEngineer, role.QALead} {
		t.Run(string(r), func(t *testing.T) {
			client := h.signedIn(string(r)+"@hyscaler.test", r)

			resp := h.do(client, http.MethodGet, "/api/v1/users", nil)
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			require.Equal(t, apierr.CodeRoleRequired, decode[httpx.Envelope](t, resp).Code)

			resp = h.do(client, http.MethodPut, "/api/v1/users/"+target.ID.String()+"/role",
				map[string]string{"role": "admin"})
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		})
	}

	admin := h.signedIn("admin@hyscaler.test", role.Admin)
	require.Equal(t, http.StatusOK, h.do(admin, http.MethodGet, "/api/v1/users", nil).StatusCode)
}

func TestAdminCannotChangeTheirOwnRole(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	me := decode[api.User](t, h.do(admin, http.MethodGet, "/api/v1/me", nil))

	resp := h.do(admin, http.MethodPut, "/api/v1/users/"+me.Id.String()+"/role",
		map[string]string{"role": "viewer"})
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, apierr.CodeSelfDemotion, decode[httpx.Envelope](t, resp).Code)
}

// A role change takes effect on the next request, not at the end of a session,
// because every session the user holds is destroyed.
func TestRoleChangeEndsTheUsersSessions(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	lead := h.seedUser("lead@hyscaler.test", role.QALead)
	leadClient := h.client()
	require.Equal(t, http.StatusOK, h.login(leadClient, "lead@hyscaler.test").StatusCode)
	require.Equal(t, http.StatusOK, h.do(leadClient, http.MethodGet, "/api/v1/me", nil).StatusCode)

	resp := h.do(admin, http.MethodPut, "/api/v1/users/"+lead.ID.String()+"/role",
		map[string]string{"role": "viewer"})
	require.Equal(t, http.StatusOK, resp.StatusCode, (resp).Text())
	require.Equal(t, api.Viewer, decode[api.User](t, resp).Role)

	require.Equal(t, http.StatusUnauthorized,
		h.do(leadClient, http.MethodGet, "/api/v1/me", nil).StatusCode)
}

// This is why sessions are server-side. Offboarding ends access here, with no token
// TTL to wait out.
func TestRevokingSessionsEndsAccessImmediately(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	engineer := h.seedUser("engineer@hyscaler.test", role.QAEngineer)
	laptop := h.client()
	phone := h.client()
	require.Equal(t, http.StatusOK, h.login(laptop, "engineer@hyscaler.test").StatusCode)
	require.Equal(t, http.StatusOK, h.login(phone, "engineer@hyscaler.test").StatusCode)

	resp := h.do(admin, http.MethodDelete, "/api/v1/users/"+engineer.ID.String()+"/sessions", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.GreaterOrEqual(t, decode[struct {
		Revoked int `json:"revoked"`
	}](t, resp).Revoked, 2)

	require.Equal(t, http.StatusUnauthorized, h.do(laptop, http.MethodGet, "/api/v1/me", nil).StatusCode)
	require.Equal(t, http.StatusUnauthorized, h.do(phone, http.MethodGet, "/api/v1/me", nil).StatusCode)
}

func TestDisablingAUserEndsAccessAndBlocksLogin(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	engineer := h.seedUser("leaver@hyscaler.test", role.QAEngineer)
	client := h.client()
	require.Equal(t, http.StatusOK, h.login(client, "leaver@hyscaler.test").StatusCode)

	resp := h.do(admin, http.MethodPut, "/api/v1/users/"+engineer.ID.String()+"/status",
		map[string]bool{"disabled": true})
	require.Equal(t, http.StatusOK, resp.StatusCode, (resp).Text())
	require.True(t, decode[api.User](t, resp).Disabled)

	require.Equal(t, http.StatusUnauthorized, h.do(client, http.MethodGet, "/api/v1/me", nil).StatusCode)

	// Disabled is reported only after the password matched, so a wrong guess cannot
	// confirm the account exists.
	resp = h.login(h.client(), "leaver@hyscaler.test")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Equal(t, apierr.CodeAccountDisabled, decode[httpx.Envelope](t, resp).Code)

	// Re-enabling restores login.
	resp = h.do(admin, http.MethodPut, "/api/v1/users/"+engineer.ID.String()+"/status",
		map[string]bool{"disabled": false})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, http.StatusOK, h.login(h.client(), "leaver@hyscaler.test").StatusCode)
}

func TestChangePasswordSignsOutOtherSessionsOnly(t *testing.T) {
	h := newHarness(t)
	h.seedUser("changer@hyscaler.test", role.QAEngineer)

	laptop := h.client()
	phone := h.client()
	require.Equal(t, http.StatusOK, h.login(laptop, "changer@hyscaler.test").StatusCode)
	require.Equal(t, http.StatusOK, h.login(phone, "changer@hyscaler.test").StatusCode)

	const newPassword = "an-entirely-different-passphrase"
	resp := h.do(laptop, http.MethodPost, "/api/v1/auth/change-password", map[string]string{
		"currentPassword": testPassword, "newPassword": newPassword,
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode, (resp).Text())

	// The session that made the change survives; the other does not.
	require.Equal(t, http.StatusOK, h.do(laptop, http.MethodGet, "/api/v1/me", nil).StatusCode)
	require.Equal(t, http.StatusUnauthorized, h.do(phone, http.MethodGet, "/api/v1/me", nil).StatusCode)

	// The old password no longer works and the new one does.
	require.Equal(t, http.StatusUnauthorized, h.login(h.client(), "changer@hyscaler.test").StatusCode)
	resp = h.do(h.client(), http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": "changer@hyscaler.test", "password": newPassword,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	h := newHarness(t)
	client := h.signedIn("careful@hyscaler.test", role.QAEngineer)

	resp := h.do(client, http.MethodPost, "/api/v1/auth/change-password", map[string]string{
		"currentPassword": "not-the-current-password", "newPassword": "a-fine-new-passphrase",
	})
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Equal(t, apierr.CodeInvalidCredentials, decode[httpx.Envelope](t, resp).Code)
}

// Session fixation: the token a caller arrives with must not be the token that ends
// up authenticated.
func TestLoginRotatesTheSessionToken(t *testing.T) {
	h := newHarness(t)
	h.seedUser("rotate@hyscaler.test", role.QAEngineer)

	client := h.client()

	// Touch a public endpoint first so a pre-login session cookie exists.
	h.do(client, http.MethodGet, "/api/v1/me", nil)
	before := sessionCookie(t, client, h.server.URL)

	require.Equal(t, http.StatusOK, h.login(client, "rotate@hyscaler.test").StatusCode)
	after := sessionCookie(t, client, h.server.URL)

	require.NotEmpty(t, after)
	require.NotEqual(t, before, after, "the session token must be renewed on login")
}

func TestSessionCookieFlags(t *testing.T) {
	h := newHarness(t)
	h.seedUser("flags@hyscaler.test", role.Admin)

	resp := h.login(h.client(), "flags@hyscaler.test")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var found *http.Cookie
	for _, cookie := range resp.Cookies {
		if cookie.Name == auth.SessionCookieName {
			found = cookie
		}
	}
	require.NotNil(t, found, "login must set the session cookie")
	require.True(t, found.HttpOnly, "a script must not be able to read the session token")
	require.Equal(t, http.SameSiteLaxMode, found.SameSite)
	require.Equal(t, "/", found.Path)
	require.Zero(t, found.MaxAge, "a session cookie, not one that survives a browser restart")
}

// The generated User schema has no password field, so a hash cannot appear in a
// response even by accident. This asserts it at the wire level anyway, because that
// is the claim that matters.
func TestNoResponseContainsAPasswordHash(t *testing.T) {
	h := newHarness(t)
	seeded := h.seedUser("hashcheck@hyscaler.test", role.Admin)
	require.NotNil(t, seeded.PasswordHash)

	client := h.client()
	bodies := []string{
		(h.login(client, "hashcheck@hyscaler.test")).Text(),
		(h.do(client, http.MethodGet, "/api/v1/me", nil)).Text(),
		(h.do(client, http.MethodGet, "/api/v1/users", nil)).Text(),
	}

	for _, body := range bodies {
		require.NotContains(t, body, "argon2id")
		require.NotContains(t, body, *seeded.PasswordHash)
		require.NotContains(t, body, "passwordHash")
	}
}

func TestListUsersPaginatesByCursor(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)
	for i := range 4 {
		h.seedUser("member"+string(rune('a'+i))+"@hyscaler.test", role.Viewer)
	}

	resp := h.do(admin, http.MethodGet, "/api/v1/users?limit=2", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	first := decode[api.UserPage](t, resp)
	require.Len(t, first.Items, 2)
	cursor, err := first.NextCursor.Get()
	require.NoError(t, err, "a further page exists, so nextCursor must be set")

	resp = h.do(admin, http.MethodGet, "/api/v1/users?limit=2&cursor="+url.QueryEscape(cursor), nil)
	second := decode[api.UserPage](t, resp)
	require.Len(t, second.Items, 2)
	require.NotEqual(t, first.Items[0].Id, second.Items[0].Id, "pages must not overlap")
}

func TestListUsersRejectsAForgedCursor(t *testing.T) {
	h := newHarness(t)
	admin := h.signedIn("admin@hyscaler.test", role.Admin)

	resp := h.do(admin, http.MethodGet, "/api/v1/users?cursor=not-a-cursor", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, apierr.CodeValidation, decode[httpx.Envelope](t, resp).Code)
}

// Every audited action leaves exactly one row, and none of them holds a secret.
func TestAuthActionsAreAudited(t *testing.T) {
	h := newHarness(t)
	h.seedUser("audited@hyscaler.test", role.Admin)

	client := h.client()
	require.Equal(t, http.StatusOK, h.login(client, "audited@hyscaler.test").StatusCode)
	h.do(client, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "audited@hyscaler.test", "password": "wrong"})
	h.do(client, http.MethodPost, "/api/v1/auth/logout", nil)

	entries, err := h.db.Queries().ListAuditEntries(context.Background(),
		dbgen.ListAuditEntriesParams{PageSize: 50})
	require.NoError(t, err)

	actions := map[string]int{}
	for _, entry := range entries {
		actions[entry.Action]++
		require.NotContains(t, string(entry.Detail), "argon2id")
		require.NotContains(t, string(entry.Detail), testPassword)
	}

	require.Equal(t, 1, actions[string(audit.ActionLogin)])
	require.Equal(t, 1, actions[string(audit.ActionLoginFailed)])
	require.Equal(t, 1, actions[string(audit.ActionLogout)])
}

// A deleted user's live session must not authenticate anybody.
func TestSessionForADeletedUserIsRejected(t *testing.T) {
	h := newHarness(t)
	user := h.seedUser("ghost@hyscaler.test", role.QAEngineer)

	client := h.client()
	require.Equal(t, http.StatusOK, h.login(client, "ghost@hyscaler.test").StatusCode)

	_, err := h.db.Pool().Exec(context.Background(), "DELETE FROM users WHERE id = $1", user.ID)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, h.do(client, http.MethodGet, "/api/v1/me", nil).StatusCode)
}

func sessionCookie(t *testing.T, client *http.Client, serverURL string) string {
	t.Helper()

	parsed, err := url.Parse(serverURL)
	require.NoError(t, err)

	for _, cookie := range client.Jar.Cookies(parsed) {
		if cookie.Name == auth.SessionCookieName {
			return cookie.Value
		}
	}
	return ""
}
