// Package storetest brings up a real Postgres for integration tests.
//
// Stores are never tested against a mocked database. Settings resolution,
// idempotency, partial unique indexes, and ON CONFLICT behaviour are precisely
// where a mock lies (backend-standards.md 14), and every one of those is
// load-bearing here.
package storetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hyscaler/qavia/api/internal/store"
)

// Tables truncated between tests, children first so foreign keys do not object.
// A new table added without a line here will leak rows into the next test.
var tables = []string{
	"audit_log",
	"notifications",
	"job_events",
	"jobs",
	"artifacts",
	"project_members",
	"projects",
	"settings_audit",
	"settings",
	"account_locks",
	"login_attempts",
	"sessions",
	"user_invitations",
	"users",
}

type shared struct {
	url string
	err error
}

var (
	once      sync.Once
	container *shared
)

// New returns a migrated database, empty of rows.
//
// One container is started per test binary and shared: a container per test would
// dominate the runtime, while a truncate between tests costs nothing and gives the
// same isolation.
func New(t *testing.T) *store.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping container-backed integration test in -short mode")
	}

	once.Do(func() { container = start() })
	require.NoError(t, container.err, "start postgres container")

	ctx := context.Background()

	db, err := store.Open(ctx, store.Options{DatabaseURL: container.url, MaxConns: 5})
	require.NoError(t, err)
	t.Cleanup(db.Close)

	require.NoError(t, store.Migrate(ctx, db))
	Truncate(t, db)

	return db
}

// Truncate empties every table. Called by New, and available to a test that wants
// a clean slate mid-way.
func Truncate(t *testing.T, db *store.DB) {
	t.Helper()

	ctx := context.Background()
	for _, table := range tables {
		_, err := db.Pool().Exec(ctx, "TRUNCATE TABLE "+table+" CASCADE")
		require.NoError(t, err, "truncate %s", table)
	}
}

func start() *shared {
	ctx := context.Background()

	// Pinned by tag rather than digest here on purpose: this image never runs in
	// production, and a digest would need updating in a place nobody looks. Runner
	// images, which do face untrusted code, are pinned by digest (F-7.13).
	postgres, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("qavia_test"),
		tcpostgres.WithUsername("qavia"),
		tcpostgres.WithPassword("qavia"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		return &shared{err: err}
	}

	url, err := postgres.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return &shared{err: err}
	}
	return &shared{url: url}
}
