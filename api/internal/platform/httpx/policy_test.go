package httpx

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
)

func ctxAs(r role.Role) context.Context {
	return WithPrincipal(context.Background(), Principal{UserID: uuid.New(), Role: r})
}

// An operation with no entry is refused. The worst outcome of forgetting an entry
// is a broken endpoint caught in review, not an unguarded one in production.
func TestAuthorizeFailsClosedForAnUndeclaredOperation(t *testing.T) {
	policies := PolicySet{"getLiveness": {Public: true}}

	err := policies.Authorize(ctxAs(role.Admin), "deleteEverything")
	require.Error(t, err)
	require.True(t, apierr.Is(err, apierr.CodeForbidden))
	require.ErrorContains(t, err, "no authorisation policy")
}

func TestAuthorizePublicOperationNeedsNoPrincipal(t *testing.T) {
	policies := PolicySet{"getLiveness": {Public: true}}

	require.NoError(t, policies.Authorize(context.Background(), "getLiveness"))
}

func TestAuthorizeRequiresASession(t *testing.T) {
	policies := PolicySet{"listProjects": {}}

	err := policies.Authorize(context.Background(), "listProjects")
	require.True(t, apierr.Is(err, apierr.CodeUnauthenticated))
}

// An empty role list means "any authenticated user", used by endpoints such as
// reading your own profile.
func TestAuthorizeEmptyRoleListAllowsAnySignedInUser(t *testing.T) {
	policies := PolicySet{"getCurrentUser": {}}

	for _, r := range role.All {
		require.NoError(t, policies.Authorize(ctxAs(r), "getCurrentUser"), r)
	}
}

func TestAuthorizeChecksTheRoleSet(t *testing.T) {
	policies := PolicySet{
		"updateGlobalSettings": {Roles: []role.Role{role.Admin}},
		"approveTestCases":     {Roles: []role.Role{role.QALead, role.Admin}},
	}

	require.NoError(t, policies.Authorize(ctxAs(role.Admin), "updateGlobalSettings"))
	require.True(t, apierr.Is(
		policies.Authorize(ctxAs(role.QALead), "updateGlobalSettings"), apierr.CodeRoleRequired))

	require.NoError(t, policies.Authorize(ctxAs(role.QALead), "approveTestCases"))
	require.True(t, apierr.Is(
		policies.Authorize(ctxAs(role.Viewer), "approveTestCases"), apierr.CodeRoleRequired))
}

// The role list is a set, not a floor. A future role that outranks QA Engineer
// must not silently gain access to every route that allowed QA Engineer.
func TestAuthorizeRoleListIsASetNotAFloor(t *testing.T) {
	policies := PolicySet{"createOwnProject": {Roles: []role.Role{role.QAEngineer}}}

	require.True(t, apierr.Is(
		policies.Authorize(ctxAs(role.Admin), "createOwnProject"), apierr.CodeRoleRequired))
}

func TestMissingAndUnknownOperations(t *testing.T) {
	policies := PolicySet{
		"getLiveness":    {Public: true},
		"removedLongAgo": {Roles: []role.Role{role.Admin}},
	}
	inSpec := []string{"getLiveness", "getReadiness"}

	require.Equal(t, []string{"getReadiness"}, policies.MissingOperations(inSpec))
	require.Equal(t, []string{"removedLongAgo"}, policies.UnknownOperations(inSpec))
}
