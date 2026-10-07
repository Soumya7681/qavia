package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/artifacts"
	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/jobs/jobstest"
	"github.com/hyscaler/qavia/api/internal/projects"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store/storetest"
)

// A retention sweep redelivered after it already deleted everything must succeed
// and change nothing further: the object is already gone and so is the row.
func TestRetentionSweepIsIdempotent(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()

	objects, err := objectstore.NewLocal(t.TempDir())
	require.NoError(t, err)

	recorder := audit.NewRecorder(db)
	cipher, err := settings.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	settingsService := settings.NewService(db, settings.Default(), cipher, recorder, settings.DefaultCacheTTL)
	service := artifacts.NewService(db, objects, projects.NewService(db, recorder), settingsService, recorder)

	var owner, project uuid.UUID
	require.NoError(t, db.Pool().QueryRow(ctx,
		`INSERT INTO users (email, name, role, timezone) VALUES ('owner@hyscaler.test', 'Owner', 'admin', 'UTC')
		 RETURNING id`).Scan(&owner))
	require.NoError(t, db.Pool().QueryRow(ctx,
		`INSERT INTO projects (name, owner_id) VALUES ('retention', $1) RETURNING id`, owner).Scan(&project))

	// One artifact well past the default retention, one fresh. Only the first may go.
	insert := func(key string, age time.Duration) {
		_, err := objects.Put(ctx, key, bytes.NewReader([]byte(key)), objectstore.PutOptions{})
		require.NoError(t, err)

		sum := sha256.Sum256([]byte(key))
		lineage := uuid.New()
		_, err = db.Pool().Exec(ctx, `
			INSERT INTO artifacts (id, project_id, kind, filename, storage_key, size_bytes, sha256, lineage_id, created_at)
			VALUES ($1, $2, 'openapi', 'spec.yaml', $3, $4, $5, $1, $6)`,
			lineage, project, key, len(key), sum[:], time.Now().Add(-age))
		require.NoError(t, err)
	}
	insert("old/spec.yaml", 10*365*24*time.Hour)
	insert("new/spec.yaml", time.Hour)

	state := func() any {
		var keys []string
		rows, err := db.Pool().Query(ctx, `SELECT storage_key FROM artifacts ORDER BY storage_key`)
		require.NoError(t, err)
		for rows.Next() {
			var key string
			require.NoError(t, rows.Scan(&key))
			_, statErr := objects.Stat(ctx, key)
			require.NoError(t, statErr, "a surviving row must still have its object")
			keys = append(keys, key)
		}
		require.NoError(t, rows.Err())
		return keys
	}

	after := jobstest.RunTwice[artifacts.RetentionPayload](t,
		artifacts.NewRetentionHandler(service), artifacts.RetentionPayload{}, state)
	require.Equal(t, []string{"new/spec.yaml"}, after)

	_, err = objects.Stat(ctx, "old/spec.yaml")
	require.Error(t, err, "the expired object is deleted along with its row")
}
