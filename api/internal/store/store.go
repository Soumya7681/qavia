// Package store owns the pgx pool and the sqlc-generated queries.
//
// The pool is injected into stores only. No pgxpool handle reaches a service, a
// handler, or a job handler (backend-standards.md 9). Domain packages take the
// *DB, ask for Queries, and never see the pool itself.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	shopspring "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Options configures the pool. Every value except the URL is a settings-backed
// number with a working default, so a fresh install needs none of them.
type Options struct {
	DatabaseURL string

	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	ConnectTimeout    time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxConns == 0 {
		// The platform targets a single VM for up to ten concurrent users
		// (NFR-9), and the worker runs as a second process against the same
		// database, so a modest ceiling per process is deliberate.
		o.MaxConns = 10
	}
	if o.MinConns == 0 {
		o.MinConns = 2
	}
	if o.MaxConnLifetime == 0 {
		o.MaxConnLifetime = time.Hour
	}
	if o.MaxConnIdleTime == 0 {
		o.MaxConnIdleTime = 30 * time.Minute
	}
	if o.HealthCheckPeriod == 0 {
		o.HealthCheckPeriod = time.Minute
	}
	if o.ConnectTimeout == 0 {
		o.ConnectTimeout = 10 * time.Second
	}
	return o
}

// DB is the handle every store takes as a constructor argument.
type DB struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// Open builds the pool and verifies it is reachable.
//
// It fails rather than returning a lazily-connecting handle: a database that is
// not there should stop the process at boot, not surface as a 500 on the first
// request.
func Open(ctx context.Context, opts Options) (*DB, error) {
	opts = opts.withDefaults()

	cfg, err := pgxpool.ParseConfig(opts.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}

	cfg.MaxConns = opts.MaxConns
	cfg.MinConns = opts.MinConns
	cfg.MaxConnLifetime = opts.MaxConnLifetime
	cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	cfg.HealthCheckPeriod = opts.HealthCheckPeriod
	cfg.ConnConfig.ConnectTimeout = opts.ConnectTimeout

	// Every query becomes a span, so "why did this take 11 minutes" stays
	// answerable from a trace (tech-stack.md 12).
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()

	// Money is numeric in the schema and decimal.Decimal in the generated code
	// (backend-standards.md 9). The driver needs to be told how to move between
	// the two, and it is told once, here, rather than at every call site.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		shopspring.Register(conn.TypeMap())
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	return &DB{pool: pool, queries: dbgen.New(pool)}, nil
}

// Queries returns the generated query set bound to the pool.
func (db *DB) Queries() *dbgen.Queries { return db.queries }

// Pool is available to stores that need squirrel for a dynamic query, and to the
// LISTEN/NOTIFY subscriber. Nothing above the store layer calls it.
func (db *DB) Pool() *pgxpool.Pool { return db.pool }

// Ping backs the readiness endpoint.
func (db *DB) Ping(ctx context.Context) error { return db.pool.Ping(ctx) }

func (db *DB) Close() { db.pool.Close() }

// InTx runs fn inside one transaction, committing on success and rolling back on
// any error or panic.
//
// Multi-write operations run in one transaction or idempotency breaks: writing 400
// test cases plus a job status update has to be atomic (backend-standards.md 9).
// Nothing slow belongs inside fn: never hold a transaction across a model call, an
// HTTP request, or a container run.
func (db *DB) InTx(ctx context.Context, fn func(*dbgen.Queries) error) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		// Rollback on the background context: the caller's context may already be
		// cancelled, and a rollback that cannot run leaks the connection.
		if rollbackErr := tx.Rollback(context.WithoutCancel(ctx)); rollbackErr != nil &&
			!errors.Is(rollbackErr, pgx.ErrTxClosed) {
			// Nothing to return to; the original error is already travelling.
			_ = rollbackErr
		}
	}()

	if err := fn(db.queries.WithTx(tx)); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// Postgres error codes this codebase reacts to. Comparing codes rather than
// message strings is what keeps the reaction correct across versions.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
)

// IsUniqueViolation reports whether err is a duplicate-key error.
//
// This is how idempotency is implemented: the write happens, and a conflict is a
// success signal rather than an exception, instead of a check-then-write across two
// statements.
func IsUniqueViolation(err error) bool { return hasPgCode(err, pgUniqueViolation) }

// IsForeignKeyViolation reports whether err is a missing-reference error.
func IsForeignKeyViolation(err error) bool { return hasPgCode(err, pgForeignKeyViolation) }

// IsCheckViolation reports whether err is a failed check constraint.
func IsCheckViolation(err error) bool { return hasPgCode(err, pgCheckViolation) }

// IsNotFound reports whether a single-row query returned nothing. Stores translate
// this into a domain error; it never travels further as a pgx error.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func hasPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// ConstraintName returns the constraint a Postgres error came from, so a store can
// map two different unique indexes on one table to two different domain errors.
func ConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}
