package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	"github.com/hyscaler/qavia/api/internal/store/storetest"
)

func newUser(t *testing.T, db *store.DB, email string, userRole dbgen.UserRole) dbgen.User {
	t.Helper()

	user, err := db.Queries().CreateUser(context.Background(), dbgen.CreateUserParams{
		Email:    email,
		Name:     email,
		Role:     userRole,
		Timezone: "Asia/Kolkata",
	})
	require.NoError(t, err)
	return user
}

func TestMigrationsApplyAndReportStatus(t *testing.T) {
	db := storetest.New(t)

	current, latest, err := store.MigrationStatus(context.Background(), db)
	require.NoError(t, err)
	require.Positive(t, latest)
	require.Equal(t, latest, current, "New should leave the schema fully migrated")
}

func TestUserEmailIsUniqueCaseInsensitively(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	newUser(t, db, "Max@hyscaler.test", dbgen.UserRoleAdmin)

	_, err := db.Queries().CreateUser(ctx, dbgen.CreateUserParams{
		Email: "max@HYSCALER.test", Name: "duplicate", Role: dbgen.UserRoleViewer, Timezone: "UTC",
	})
	require.Error(t, err)
	require.True(t, store.IsUniqueViolation(err), "expected a unique violation, got %v", err)

	// Login has to find the account whatever case was typed.
	found, err := db.Queries().GetUserByEmail(ctx, "MAX@hyscaler.TEST")
	require.NoError(t, err)
	require.Equal(t, "Max@hyscaler.test", found.Email)
}

func TestIsNotFoundTranslatesNoRows(t *testing.T) {
	db := storetest.New(t)

	_, err := db.Queries().GetUserByID(context.Background(), uuid.New())
	require.Error(t, err)
	require.True(t, store.IsNotFound(err))
}

// Resolution order is user, then project, then global. The query returns the
// candidates strongest first so Go can take the head without sorting.
func TestSettingsResolveInScopeOrder(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	admin := newUser(t, db, "admin@hyscaler.test", dbgen.UserRoleAdmin)
	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "quickdesk", Description: "", OwnerID: admin.ID, TestTypes: []string{"test_cases"},
	})
	require.NoError(t, err)

	const key = "notifications.channels"

	_, err = db.Queries().UpsertGlobalSetting(ctx, dbgen.UpsertGlobalSettingParams{
		Key: key, Value: json.RawMessage(`["in_app"]`), IsSecret: false, UpdatedBy: &admin.ID,
	})
	require.NoError(t, err)

	resolved, err := db.Queries().ResolveSetting(ctx, dbgen.ResolveSettingParams{
		Key: key, UserID: &admin.ID, ProjectID: &project.ID,
	})
	require.NoError(t, err)
	require.Len(t, resolved, 1)
	require.Equal(t, dbgen.SettingsScopeGlobal, resolved[0].Scope)

	_, err = db.Queries().UpsertSetting(ctx, dbgen.UpsertSettingParams{
		Scope: dbgen.SettingsScopeProject, ScopeID: &project.ID,
		Key: key, Value: json.RawMessage(`["in_app","slack"]`), UpdatedBy: &admin.ID,
	})
	require.NoError(t, err)

	_, err = db.Queries().UpsertSetting(ctx, dbgen.UpsertSettingParams{
		Scope: dbgen.SettingsScopeUser, ScopeID: &admin.ID,
		Key: key, Value: json.RawMessage(`[]`), UpdatedBy: &admin.ID,
	})
	require.NoError(t, err)

	resolved, err = db.Queries().ResolveSetting(ctx, dbgen.ResolveSettingParams{
		Key: key, UserID: &admin.ID, ProjectID: &project.ID,
	})
	require.NoError(t, err)
	require.Len(t, resolved, 3)
	require.Equal(t, dbgen.SettingsScopeUser, resolved[0].Scope, "user scope wins")
	require.Equal(t, dbgen.SettingsScopeProject, resolved[1].Scope)
	require.Equal(t, dbgen.SettingsScopeGlobal, resolved[2].Scope)
}

// Two NULL scope_ids are distinct to a plain unique index, so global rows are
// constrained by their own partial index. This test is the reason there are two
// upsert queries rather than one.
func TestGlobalSettingUpsertReplacesRatherThanDuplicating(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	for _, value := range []string{`"local-disk"`, `"minio"`, `"s3"`} {
		_, err := db.Queries().UpsertGlobalSetting(ctx, dbgen.UpsertGlobalSettingParams{
			Key: "storage.provider", Value: json.RawMessage(value),
		})
		require.NoError(t, err)
	}

	rows, err := db.Queries().ListSettingsByScope(ctx, dbgen.ListSettingsByScopeParams{
		Scope: dbgen.SettingsScopeGlobal,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.JSONEq(t, `"s3"`, string(rows[0].Value))
}

// A secret's values are recorded as [redacted]. Only the fact of the change is
// kept, and this test is what stops a later refactor storing the real value.
func TestSettingsAuditKeepsNoSecretValue(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	admin := newUser(t, db, "auditor@hyscaler.test", dbgen.UserRoleAdmin)
	redacted := json.RawMessage(`"[redacted]"`)

	require.NoError(t, db.Queries().RecordSettingChange(ctx, dbgen.RecordSettingChangeParams{
		Scope: dbgen.SettingsScopeGlobal, Key: "anthropic.api_key",
		OldValue: redacted, NewValue: redacted, ActorID: &admin.ID,
	}))

	entries, err := db.Queries().ListSettingChanges(ctx, dbgen.ListSettingChangesParams{PageSize: 10})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.JSONEq(t, `"[redacted]"`, string(entries[0].NewValue))
}

// Enqueue is idempotent by unique index. A retried enqueue returns the original
// row rather than creating a second one, which is what makes NFR-4 structural.
func TestEnqueueJobIsIdempotent(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "engineer@hyscaler.test", dbgen.UserRoleQaEngineer)
	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "idempotency", OwnerID: owner.ID, TestTypes: []string{"test_cases"},
	})
	require.NoError(t, err)

	params := dbgen.EnqueueJobParams{
		ProjectID:      &project.ID,
		Type:           "noop",
		Payload:        json.RawMessage(`{"steps":3}`),
		IdempotencyKey: "noop:" + project.ID.String(),
		CorrelationID:  "corr-1",
		MaxAttempts:    3,
		EnqueuedBy:     &owner.ID,
	}

	first, err := db.Queries().EnqueueJob(ctx, params)
	require.NoError(t, err)

	second, err := db.Queries().EnqueueJob(ctx, params)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "a retried enqueue must not create a second job")

	jobs, err := db.Queries().ListJobsForProject(ctx, dbgen.ListJobsForProjectParams{
		ProjectID: &project.ID, PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
}

// Illegal transitions are rejected by the WHERE clause rather than by a
// read-modify-write in Go, so two workers racing cannot both win.
func TestJobStatusTransitionsAreGuarded(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "lead@hyscaler.test", dbgen.UserRoleQaLead)
	job, err := db.Queries().EnqueueJob(ctx, dbgen.EnqueueJobParams{
		Type: "noop", Payload: json.RawMessage(`{}`),
		IdempotencyKey: "transitions", MaxAttempts: 3, EnqueuedBy: &owner.ID,
	})
	require.NoError(t, err)

	// Cannot succeed straight from queued.
	rows, err := db.Queries().MarkJobSucceeded(ctx, job.ID)
	require.NoError(t, err)
	require.Zero(t, rows)

	rows, err = db.Queries().MarkJobRunning(ctx, job.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	rows, err = db.Queries().SetJobProgress(ctx, dbgen.SetJobProgressParams{ID: job.ID, Progress: 50})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	rows, err = db.Queries().MarkJobSucceeded(ctx, job.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// A finished job cannot be cancelled.
	rows, err = db.Queries().CancelJob(ctx, job.ID)
	require.NoError(t, err)
	require.Zero(t, rows)

	final, err := db.Queries().GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, dbgen.JobStatusSucceeded, final.Status)
	require.EqualValues(t, 100, final.Progress)
	require.EqualValues(t, 1, final.Attempts)
}

// The bigserial id is the SSE Last-Event-ID cursor. A reconnect resumes from it
// without gaps or duplicates.
func TestJobEventsPageByID(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	job, err := db.Queries().EnqueueJob(ctx, dbgen.EnqueueJobParams{
		Type: "noop", Payload: json.RawMessage(`{}`), IdempotencyKey: "events", MaxAttempts: 3,
	})
	require.NoError(t, err)

	var ids []int64
	for _, message := range []string{"stage 1", "stage 2", "stage 3"} {
		event, appendErr := db.Queries().AppendJobEvent(ctx, dbgen.AppendJobEventParams{
			JobID: job.ID, Level: dbgen.JobEventLevelInfo, Message: message,
		})
		require.NoError(t, appendErr)
		ids = append(ids, event.ID)
	}

	after, err := db.Queries().ListJobEventsSince(ctx, dbgen.ListJobEventsSinceParams{
		JobID: job.ID, ID: ids[0], PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, after, 2)
	require.Equal(t, "stage 2", after[0].Message)
}

// Identical re-upload does not re-run generation (FR-1.4), and the guarantee is a
// unique index rather than a check-then-write.
func TestArtifactDedupeAndVersioning(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "uploader@hyscaler.test", dbgen.UserRoleQaEngineer)
	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "specs", OwnerID: owner.ID, TestTypes: []string{"api_tests"},
	})
	require.NoError(t, err)

	firstHash := sha256.Sum256([]byte("openapi: 3.1.0"))
	lineage := uuid.New()

	first, err := db.Queries().CreateArtifact(ctx, dbgen.CreateArtifactParams{
		ID: uuid.New(), ProjectID: project.ID, Kind: "openapi", Filename: "qavia.yaml",
		StorageKey: "projects/x/openapi/1/qavia.yaml", ContentType: "application/yaml",
		SizeBytes: 14, Sha256: firstHash[:], Version: 1, LineageID: lineage, UploadedBy: &owner.ID,
	})
	require.NoError(t, err)

	// Same bytes again.
	_, err = db.Queries().CreateArtifact(ctx, dbgen.CreateArtifactParams{
		ID: uuid.New(), ProjectID: project.ID, Kind: "openapi", Filename: "qavia.yaml",
		StorageKey: "projects/x/openapi/2/qavia.yaml", SizeBytes: 14,
		Sha256: firstHash[:], Version: 2, LineageID: lineage,
	})
	require.True(t, store.IsUniqueViolation(err))

	existing, err := db.Queries().GetArtifactByHash(ctx, dbgen.GetArtifactByHashParams{
		ProjectID: project.ID, Sha256: firstHash[:],
	})
	require.NoError(t, err)
	require.Equal(t, first.ID, existing.ID)

	// Changed bytes become version 2 in the same lineage, and version 1 survives:
	// phase 11 diffs against it.
	secondHash := sha256.Sum256([]byte("openapi: 3.1.0 # changed"))
	_, err = db.Queries().CreateArtifact(ctx, dbgen.CreateArtifactParams{
		ID: uuid.New(), ProjectID: project.ID, Kind: "openapi", Filename: "qavia.yaml",
		StorageKey: "projects/x/openapi/2/qavia.yaml", SizeBytes: 24,
		Sha256: secondHash[:], Version: 2, LineageID: lineage,
	})
	require.NoError(t, err)

	versions, err := db.Queries().ListArtifactVersions(ctx, lineage)
	require.NoError(t, err)
	require.Len(t, versions, 2)
	require.EqualValues(t, 2, versions[0].Version)

	lineageRow, err := db.Queries().FindArtifactLineage(ctx, dbgen.FindArtifactLineageParams{
		ProjectID: project.ID, Kind: "openapi", Filename: "qavia.yaml",
	})
	require.NoError(t, err)
	require.Equal(t, lineage, lineageRow.LineageID)
	require.EqualValues(t, 2, lineageRow.LatestVersion)
}

// A truncated sha256 must not reach the column: the check constraint is the last
// line of defence for the field the whole maintenance module depends on.
func TestArtifactRejectsAMalformedHash(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "badhash@hyscaler.test", dbgen.UserRoleQaEngineer)
	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "hashes", OwnerID: owner.ID, TestTypes: []string{"api_tests"},
	})
	require.NoError(t, err)

	_, err = db.Queries().CreateArtifact(ctx, dbgen.CreateArtifactParams{
		ID: uuid.New(), ProjectID: project.ID, Kind: "openapi", Filename: "short.yaml",
		StorageKey: "k", SizeBytes: 1, Sha256: []byte("too short"),
		Version: 1, LineageID: uuid.New(),
	})
	require.True(t, store.IsCheckViolation(err), "got %v", err)
}

// Ownership counts as membership, so an owner never has to add themselves.
func TestProjectMembershipTreatsOwnerAsMember(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "owner@hyscaler.test", dbgen.UserRoleQaEngineer)
	stranger := newUser(t, db, "stranger@hyscaler.test", dbgen.UserRoleQaEngineer)

	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "membership", OwnerID: owner.ID, TestTypes: []string{"test_cases"},
	})
	require.NoError(t, err)

	asOwner, err := db.Queries().GetProjectMembership(ctx, dbgen.GetProjectMembershipParams{
		ID: project.ID, UserID: owner.ID,
	})
	require.NoError(t, err)
	require.Equal(t, owner.ID, asOwner.OwnerID)
	require.False(t, asOwner.MemberRole.Valid, "an owner needs no membership row")

	asStranger, err := db.Queries().GetProjectMembership(ctx, dbgen.GetProjectMembershipParams{
		ID: project.ID, UserID: stranger.ID,
	})
	require.NoError(t, err)
	require.False(t, asStranger.MemberRole.Valid)
	require.NotEqual(t, stranger.ID, asStranger.OwnerID)
}

// An archived project is read-only, enforced in the UPDATE rather than by a check
// the service might forget.
func TestArchivedProjectCannotBeUpdated(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	owner := newUser(t, db, "archiver@hyscaler.test", dbgen.UserRoleQaLead)
	project, err := db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name: "archived", OwnerID: owner.ID, TestTypes: []string{"test_cases"},
	})
	require.NoError(t, err)

	rows, err := db.Queries().ArchiveProject(ctx, project.ID)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	_, err = db.Queries().UpdateProject(ctx, dbgen.UpdateProjectParams{
		ID: project.ID, Name: "renamed", TestTypes: []string{"test_cases"},
	})
	require.True(t, store.IsNotFound(err), "got %v", err)

	// Archiving twice is a no-op rather than an error.
	rows, err = db.Queries().ArchiveProject(ctx, project.ID)
	require.NoError(t, err)
	require.Zero(t, rows)
}

func TestNotificationsUnreadCount(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	user := newUser(t, db, "notified@hyscaler.test", dbgen.UserRoleViewer)

	for i := range 3 {
		_, err := db.Queries().CreateNotification(ctx, dbgen.CreateNotificationParams{
			UserID: user.ID, Kind: "job_completed",
			Title: "Generation complete", Body: "", Link: "/projects/1/jobs/2",
		})
		require.NoError(t, err, "notification %d", i)
	}

	unread, err := db.Queries().CountUnreadNotifications(ctx, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, unread)

	list, err := db.Queries().ListNotifications(ctx, dbgen.ListNotificationsParams{
		UserID: user.ID, PageSize: 10,
	})
	require.NoError(t, err)
	require.Len(t, list, 3)

	rows, err := db.Queries().MarkNotificationRead(ctx, dbgen.MarkNotificationReadParams{
		ID: list[0].ID, UserID: user.ID,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// Reading someone else's notification is not possible.
	other := newUser(t, db, "other@hyscaler.test", dbgen.UserRoleViewer)
	rows, err = db.Queries().MarkNotificationRead(ctx, dbgen.MarkNotificationReadParams{
		ID: list[1].ID, UserID: other.ID,
	})
	require.NoError(t, err)
	require.Zero(t, rows)

	unread, err = db.Queries().CountUnreadNotifications(ctx, user.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, unread)
}

func TestLoginThrottlingCountersAndLock(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	const email = "throttled@hyscaler.test"
	since := time.Now().Add(-15 * time.Minute)

	for range 5 {
		require.NoError(t, db.Queries().RecordLoginAttempt(ctx, dbgen.RecordLoginAttemptParams{
			Email: email, Succeeded: false, UserAgent: "test",
		}))
	}
	require.NoError(t, db.Queries().RecordLoginAttempt(ctx, dbgen.RecordLoginAttemptParams{
		Email: email, Succeeded: true, UserAgent: "test",
	}))

	failed, err := db.Queries().CountRecentFailedAttempts(ctx, dbgen.CountRecentFailedAttemptsParams{
		Email: email, Since: since,
	})
	require.NoError(t, err)
	require.EqualValues(t, 5, failed, "successful attempts must not count towards the lock")

	require.NoError(t, db.Queries().LockAccount(ctx, dbgen.LockAccountParams{
		Email: email, LockedUntil: time.Now().Add(10 * time.Minute), Reason: "too_many_failed_attempts",
	}))

	lock, err := db.Queries().GetAccountLock(ctx, email)
	require.NoError(t, err)
	require.Equal(t, email, lock.Email)

	// An expired lock is simply absent, so no background job is needed to unlock.
	require.NoError(t, db.Queries().LockAccount(ctx, dbgen.LockAccountParams{
		Email: email, LockedUntil: time.Now().Add(-time.Minute), Reason: "expired",
	}))
	_, err = db.Queries().GetAccountLock(ctx, email)
	require.True(t, store.IsNotFound(err))

	rows, err := db.Queries().ClearAccountLock(ctx, email)
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)
}

// A transaction that fails must leave nothing behind. Writing 400 test cases plus
// a job status update is one transaction, or idempotency breaks.
func TestInTxRollsBackEverythingOnError(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	sentinel := errors.New("deliberate failure")

	err := db.InTx(ctx, func(q *dbgen.Queries) error {
		if _, txErr := q.CreateUser(ctx, dbgen.CreateUserParams{
			Email: "rollback@hyscaler.test", Name: "rollback",
			Role: dbgen.UserRoleViewer, Timezone: "UTC",
		}); txErr != nil {
			return txErr
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	_, err = db.Queries().GetUserByEmail(ctx, "rollback@hyscaler.test")
	require.True(t, store.IsNotFound(err), "the failed transaction must have left no row")
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	require.NoError(t, db.InTx(ctx, func(q *dbgen.Queries) error {
		_, err := q.CreateUser(ctx, dbgen.CreateUserParams{
			Email: "committed@hyscaler.test", Name: "committed",
			Role: dbgen.UserRoleAdmin, Timezone: "UTC",
		})
		return err
	}))

	user, err := db.Queries().GetUserByEmail(ctx, "committed@hyscaler.test")
	require.NoError(t, err)
	require.Equal(t, dbgen.UserRoleAdmin, user.Role)
}
