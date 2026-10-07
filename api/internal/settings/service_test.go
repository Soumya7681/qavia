package settings_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	"github.com/hyscaler/qavia/api/internal/store/storetest"
)

// Settings resolution, caching, and invalidation are tested against a real Postgres.
// Partial unique indexes, ON CONFLICT behaviour, and LISTEN/NOTIFY are exactly where
// a mock would agree with itself and the database would not
// (backend-standards.md 14).

type fixture struct {
	t       *testing.T
	db      *store.DB
	service *settings.Service
	admin   settings.Actor
	viewer  settings.Actor
	project uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	db := storetest.New(t)

	cipher, err := settings.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	service := settings.NewService(
		db, settings.Default(), cipher, audit.NewRecorder(db), settings.DefaultCacheTTL)

	ctx := context.Background()
	adminRow, err := db.Queries().CreateUser(ctx, dbgen.CreateUserParams{
		Email: "admin@hyscaler.test", Name: "Admin", Role: dbgen.UserRoleAdmin, Timezone: "UTC",
	})
	require.NoError(t, err)

	viewerRow, err := db.Queries().CreateUser(ctx, dbgen.CreateUserParams{
		Email: "viewer@hyscaler.test", Name: "Viewer", Role: dbgen.UserRoleViewer, Timezone: "UTC",
	})
	require.NoError(t, err)

	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "quickdesk", OwnerID: adminRow.ID, TestTypes: []string{"test_cases"},
	})
	require.NoError(t, err)

	return &fixture{
		t:       t,
		db:      db,
		service: service,
		admin:   settings.Actor{UserID: adminRow.ID, Email: adminRow.Email, Role: role.Admin},
		viewer:  settings.Actor{UserID: viewerRow.ID, Email: viewerRow.Email, Role: role.Viewer},
		project: project.ID,
	}
}

func (f *fixture) write(actor settings.Actor, key string, scope settings.Scope, scopeID *uuid.UUID, value any) error {
	f.t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(f.t, err)

	_, err = f.service.Write(context.Background(), actor, settings.WriteRequest{
		Key: key, Scope: scope, ScopeID: scopeID, Raw: raw,
	})
	return err
}

// An unset setting resolves to its declared default. This is the whole reason a
// missing row is never a zero value.
func TestUnsetSettingResolvesToItsDefault(t *testing.T) {
	f := newFixture(t)

	value, err := f.service.Resolve(context.Background(), "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.True(t, value.FromDefault)
	require.JSONEq(t, `90`, string(value.Raw))

	days, err := f.service.Int(context.Background(), "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.Equal(t, 90, days)
}

// An undeclared key is an error, never a zero value. A zero that quietly means "off"
// is the failure the registry exists to prevent.
func TestUndeclaredKeyIsAnError(t *testing.T) {
	f := newFixture(t)

	_, err := f.service.Resolve(context.Background(), "runner.does_not_exist", settings.Target{})
	require.True(t, apierr.Is(err, apierr.CodeSettingUnknownKey), "got %v", err)

	_, err = f.service.Int(context.Background(), "runner.does_not_exist", settings.Target{})
	require.True(t, apierr.Is(err, apierr.CodeSettingUnknownKey))
}

func TestResolutionFollowsUserThenProjectThenGlobal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const key = "preferences.notification_channels"
	target := settings.Target{UserID: &f.viewer.UserID, ProjectID: &f.project}

	// Nothing stored: the declared default.
	value, err := f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.True(t, value.FromDefault)

	require.NoError(t, f.write(f.admin, key, settings.ScopeGlobal, nil, []string{"in_app"}))
	value, err = f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeGlobal, value.Source)
	require.False(t, value.FromDefault)

	require.NoError(t, f.write(f.admin, key, settings.ScopeProject, &f.project, []string{"in_app", "slack"}))
	value, err = f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeProject, value.Source)

	require.NoError(t, f.write(f.viewer, key, settings.ScopeUser, &f.viewer.UserID, []string{}))
	value, err = f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeUser, value.Source, "the user's own value wins")

	channels, err := f.service.StringList(ctx, key, target)
	require.NoError(t, err)
	require.Empty(t, channels)

	// A different user still sees the project value: resolution is per target.
	other := settings.Target{UserID: &f.admin.UserID, ProjectID: &f.project}
	value, err = f.service.Resolve(ctx, key, other)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeProject, value.Source)
}

// Clearing an override falls through to the next scope rather than to the zero value.
func TestClearingAnOverrideFallsThrough(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const key = "preferences.notification_channels"
	target := settings.Target{UserID: &f.viewer.UserID}

	require.NoError(t, f.write(f.admin, key, settings.ScopeGlobal, nil, []string{"in_app", "slack"}))
	require.NoError(t, f.write(f.viewer, key, settings.ScopeUser, &f.viewer.UserID, []string{}))

	value, err := f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeUser, value.Source)

	require.NoError(t, f.service.Clear(ctx, f.viewer, key, settings.ScopeUser, &f.viewer.UserID))

	value, err = f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.Equal(t, settings.ScopeGlobal, value.Source)

	require.NoError(t, f.service.Clear(ctx, f.admin, key, settings.ScopeGlobal, nil))

	value, err = f.service.Resolve(ctx, key, target)
	require.NoError(t, err)
	require.True(t, value.FromDefault)
}

// Writing the same key repeatedly must replace the row rather than accumulate rows,
// which is the partial-unique-index behaviour a mock cannot check.
func TestRepeatedGlobalWritesReplaceTheRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for _, days := range []int{30, 60, 120} {
		require.NoError(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, days))
	}

	rows, err := f.db.Queries().ListSettingsByScope(ctx, dbgen.ListSettingsByScopeParams{
		Scope: dbgen.SettingsScopeGlobal,
	})
	require.NoError(t, err)

	matching := 0
	for _, row := range rows {
		if row.Key == "storage.retention_days" {
			matching++
			require.JSONEq(t, `120`, string(row.Value))
		}
	}
	require.Equal(t, 1, matching)
}

func TestWriteRejectsAValueThatFailsItsDeclaredValidation(t *testing.T) {
	f := newFixture(t)

	err := f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, 99999)
	require.True(t, apierr.Is(err, apierr.CodeSettingInvalid), "got %v", err)

	domain, _ := apierr.As(err)
	require.Contains(t, domain.Message, "between 1 and 3650")
	require.Equal(t, "storage.retention_days", domain.Details["key"])

	// The rejected write must have left nothing behind.
	value, err := f.service.Resolve(context.Background(), "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.True(t, value.FromDefault)
}

func TestWriteEnforcesTheMinimumRole(t *testing.T) {
	f := newFixture(t)

	err := f.write(f.viewer, "storage.retention_days", settings.ScopeGlobal, nil, 30)
	require.True(t, apierr.Is(err, apierr.CodeSettingRoleTooLow), "got %v", err)

	domain, _ := apierr.As(err)
	require.Equal(t, "Admin", domain.Details["requiredRole"])
}

// A Viewer may always write their own preferences. The scope, not the role, is what
// stops one user editing another's.
func TestAnyRoleMayWriteTheirOwnPreferences(t *testing.T) {
	f := newFixture(t)

	require.NoError(t, f.write(f.viewer, "preferences.theme", settings.ScopeUser, &f.viewer.UserID, "dark"))

	theme, err := f.service.String(context.Background(), "preferences.theme",
		settings.Target{UserID: &f.viewer.UserID})
	require.NoError(t, err)
	require.Equal(t, "dark", theme)
}

func TestWriteRejectsAScopeTheSettingDoesNotAllow(t *testing.T) {
	f := newFixture(t)

	// storage.provider is global only.
	err := f.write(f.admin, "storage.provider", settings.ScopeProject, &f.project, "s3")
	require.True(t, apierr.Is(err, apierr.CodeSettingScope), "got %v", err)

	domain, _ := apierr.As(err)
	require.Contains(t, domain.Message, "global")
}

func TestProjectScopedWriteNeedsAnOwner(t *testing.T) {
	f := newFixture(t)

	err := f.write(f.admin, "jobs.ai_fan_out_limit", settings.ScopeProject, nil, 8)
	require.True(t, apierr.Is(err, apierr.CodeValidation), "got %v", err)
}

// ---------------------------------------------------------------- secrets

func TestSecretRoundTripsWithoutEverBeingReturned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const token = "xoxb-DO-NOT-LOG-ME-0123456789"
	require.NoError(t, f.write(f.admin, "notifications.slack_token", settings.ScopeGlobal, nil, token))

	// The stored row holds no plaintext.
	rows, err := f.db.Queries().ListSettingsByScope(ctx, dbgen.ListSettingsByScopeParams{
		Scope: dbgen.SettingsScopeGlobal,
	})
	require.NoError(t, err)
	for _, row := range rows {
		require.NotContains(t, string(row.Value), token)
		if row.Key == "notifications.slack_token" {
			require.True(t, row.IsSecret)
		}
	}

	// Decryption happens only at the point of use.
	plaintext, found, err := f.service.Secret(ctx, "notifications.slack_token", settings.Target{})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, token, plaintext)
}

// An unconfigured optional integration is normal, not an error
// (requirements.md 5.4).
func TestUnsetSecretIsNotAnError(t *testing.T) {
	f := newFixture(t)

	plaintext, found, err := f.service.Secret(context.Background(), "notifications.slack_token", settings.Target{})
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, plaintext)
}

func TestSecretAccessorRejectsANonSecretKey(t *testing.T) {
	f := newFixture(t)

	_, _, err := f.service.Secret(context.Background(), "storage.bucket", settings.Target{})
	require.ErrorContains(t, err, "is not a secret")
}

// The audit trail records that a secret changed and nothing about its value
// (requirements.md 5.3).
func TestSecretAuditKeepsNoValue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const first = "xoxb-first-DO-NOT-LOG-0123456789"
	const second = "xoxb-second-DO-NOT-LOG-987654321"

	require.NoError(t, f.write(f.admin, "notifications.slack_token", settings.ScopeGlobal, nil, first))
	require.NoError(t, f.write(f.admin, "notifications.slack_token", settings.ScopeGlobal, nil, second))

	entries, err := f.db.Queries().ListSettingChanges(ctx, dbgen.ListSettingChangesParams{PageSize: 50})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	for _, entry := range entries {
		require.NotContains(t, string(entry.NewValue), "xoxb")
		require.NotContains(t, string(entry.OldValue), "xoxb")
		require.JSONEq(t, `"[redacted]"`, string(entry.NewValue))
	}

	// The second write recorded a previous value, redacted.
	require.JSONEq(t, `"[redacted]"`, string(entries[0].OldValue))
}

// A non-secret change records both values, which is what makes the trail useful.
func TestNonSecretAuditRecordsBothValues(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	require.NoError(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, 30))
	require.NoError(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, 60))

	entries, err := f.db.Queries().ListSettingChanges(ctx, dbgen.ListSettingChangesParams{
		Key: strPtr("storage.retention_days"), PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	require.JSONEq(t, `60`, string(entries[0].NewValue))
	require.JSONEq(t, `30`, string(entries[0].OldValue))
	require.Equal(t, &f.admin.UserID, entries[0].ActorID)
}

// A rejected write must leave no audit row claiming it happened, which is why the
// audit insert shares the write's transaction.
func TestRejectedWriteLeavesNoAuditRow(t *testing.T) {
	f := newFixture(t)

	require.Error(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, -5))

	entries, err := f.db.Queries().ListSettingChanges(context.Background(),
		dbgen.ListSettingChangesParams{PageSize: 10})
	require.NoError(t, err)
	require.Empty(t, entries)
}

// ------------------------------------------------------- cache and invalidation

func TestResolveIsCachedAndAWriteInvalidatesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	_, err := f.service.Resolve(ctx, "storage.retention_days", settings.Target{})
	require.NoError(t, err)

	// A write behind the service's back is not seen, which proves the cache is real.
	_, err = f.db.Queries().UpsertGlobalSetting(ctx, dbgen.UpsertGlobalSettingParams{
		Key: "storage.retention_days", Value: json.RawMessage(`45`),
	})
	require.NoError(t, err)

	days, err := f.service.Int(ctx, "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.Equal(t, 90, days, "the cached default should still be in force")

	// Going through the service invalidates, so the next read is current.
	require.NoError(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, 60))

	days, err = f.service.Int(ctx, "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.Equal(t, 60, days)
}

// The API and the worker are separate processes. A change in one has to reach the
// other, or a worker runs on stale configuration until its cache expires. Two
// services over one database stand in for the two processes.
func TestInvalidationCrossesProcesses(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	cipher, err := settings.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	// The second "process": its own service, its own cache, same database.
	worker := settings.NewService(
		f.db, settings.Default(), cipher, audit.NewRecorder(f.db), time.Hour)

	listenerCtx, stopListener := context.WithCancel(ctx)
	defer stopListener()

	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		require.NoError(t, worker.Listen(listenerCtx))
	}()

	// Warm the worker's cache with the default.
	days, err := worker.Int(ctx, "storage.retention_days", settings.Target{})
	require.NoError(t, err)
	require.Equal(t, 90, days)

	// The API process writes.
	require.NoError(t, f.write(f.admin, "storage.retention_days", settings.ScopeGlobal, nil, 15))

	// The worker must pick it up without waiting out its one-hour TTL.
	require.Eventually(t, func() bool {
		current, readErr := worker.Int(ctx, "storage.retention_days", settings.Target{})
		return readErr == nil && current == 15
	}, 5*time.Second, 25*time.Millisecond,
		"the worker never saw the change, so it would run on stale settings")

	stopListener()
	<-listenerDone
}

// ListForTarget warms every key in one round trip, which is what makes the settings
// screen a single request rather than one query per setting.
func TestListForTargetCoversEveryDeclaredSetting(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	require.NoError(t, f.write(f.admin, "storage.bucket", settings.ScopeGlobal, nil, "qavia-prod"))

	values, err := f.service.ListForTarget(ctx, f.admin, settings.Target{UserID: &f.admin.UserID})
	require.NoError(t, err)
	require.Len(t, values, settings.Default().Len())

	byKey := make(map[string]settings.Value, len(values))
	for _, value := range values {
		byKey[value.Key] = value
	}

	require.JSONEq(t, `"qavia-prod"`, string(byKey["storage.bucket"].Raw))
	require.False(t, byKey["storage.bucket"].FromDefault)
	require.True(t, byKey["storage.region"].FromDefault)
}

// Every declared setting has to survive a real round trip through its own validation.
// This is the test that would have caught a default that the entry's own rules reject.
func TestEveryDeclaredDefaultIsWritableAndReadable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for _, entry := range settings.Default().Entries() {
		if entry.IsSecret() || !entry.AllowsScope(settings.ScopeGlobal) {
			continue
		}

		t.Run(entry.Key, func(t *testing.T) {
			require.NoError(t, f.write(f.admin, entry.Key, settings.ScopeGlobal, nil, entry.Default))

			value, err := f.service.Resolve(ctx, entry.Key, settings.Target{})
			require.NoError(t, err)
			require.False(t, value.FromDefault)

			expected, err := entry.DefaultJSON()
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(value.Raw))
		})
	}
}

func strPtr(value string) *string { return &value }
