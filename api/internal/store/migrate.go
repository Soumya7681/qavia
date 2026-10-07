package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/hyscaler/qavia/api/migrations"
)

// Migrate applies every pending migration.
//
// The migrations are embedded in the binary, so a deploy carries the schema it
// needs. goose wants a database/sql handle, which pgx provides from the same pool
// rather than opening a second connection path.
func Migrate(ctx context.Context, db *DB) error {
	sqlDB := stdlib.OpenDBFromPool(db.pool)
	defer closeMigrationHandle(ctx, sqlDB)

	provider, err := newProvider(sqlDB)
	if err != nil {
		return err
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// Logged rather than discarded: "applied 3 migrations" in a deploy log is the
	// difference between knowing the schema moved and assuming it did.
	for _, result := range results {
		slog.InfoContext(ctx, "applied migration",
			"version", result.Source.Version, "path", result.Source.Path,
			"duration_ms", result.Duration.Milliseconds())
	}
	return nil
}

// MigrationStatus reports the current and latest versions, for the readiness
// endpoint and for the setup wizard.
func MigrationStatus(ctx context.Context, db *DB) (current, latest int64, err error) {
	sqlDB := stdlib.OpenDBFromPool(db.pool)
	defer closeMigrationHandle(ctx, sqlDB)

	provider, err := newProvider(sqlDB)
	if err != nil {
		return 0, 0, err
	}

	current, err = provider.GetDBVersion(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("read migration version: %w", err)
	}

	sources := provider.ListSources()
	if len(sources) > 0 {
		latest = sources[len(sources)-1].Version
	}
	return current, latest, nil
}

// closeMigrationHandle releases the database/sql wrapper around the pool. The
// failure is worth knowing about but not worth failing a migration that already
// succeeded, so it is logged rather than returned.
func closeMigrationHandle(ctx context.Context, sqlDB *sql.DB) {
	if err := sqlDB.Close(); err != nil {
		slog.WarnContext(ctx, "close migration database handle", "error", err)
	}
}

func newProvider(sqlDB *sql.DB) (*goose.Provider, error) {
	// A session lock, because the API and the worker start together and both migrate.
	// Without it the second one races the first: a migration creating a type fails
	// with a duplicate-key error on pg_type halfway through, which leaves a process
	// refusing to boot for a reason that has nothing to do with its own work.
	//
	// A Postgres advisory lock rather than a table flag: it is released when the
	// session ends, so a process killed mid-migration does not leave the lock held.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("build migration lock: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("build migration provider: %w", err)
	}
	return provider, nil
}
