-- +goose Up
-- Run kind and performance metrics (BE-9.1).
--
-- A functional run, a performance run, and a security run all flow through the same
-- execution boundary — the same container rules, the same allowlist, the same reaping.
-- What differs is what they produce and how they are read, and `kind` is what lets a UI
-- ask for the performance runs without inferring it from the framework: k6 is the load
-- tool today, but the kind is the fact the platform means to keep.

CREATE TYPE run_kind AS ENUM ('functional', 'performance', 'security');

ALTER TABLE runs ADD COLUMN kind run_kind NOT NULL DEFAULT 'functional';

-- The load profile the run was generated and executed with, so a latency series can be
-- read against the concurrency and duration that produced it: p95 of 400ms at 50 users
-- is a different fact from p95 of 400ms at 5.
ALTER TABLE runs ADD COLUMN load_profile jsonb NOT NULL DEFAULT '{}'::jsonb;

-- The measured series, normalised out of k6's own summary in the image. One row per run
-- rather than a metrics table, because a run has exactly one summary and a time series
-- is not what a percentile is: percentiles are the summary, and storing raw samples
-- would be storing what the tail already told us.
CREATE TABLE run_metrics (
    run_id uuid PRIMARY KEY REFERENCES runs (id) ON DELETE CASCADE,

    requests   bigint NOT NULL DEFAULT 0,
    throughput double precision NOT NULL DEFAULT 0,
    error_rate double precision NOT NULL DEFAULT 0,

    latency_avg_ms double precision NOT NULL DEFAULT 0,
    latency_p50_ms double precision NOT NULL DEFAULT 0,
    latency_p90_ms double precision NOT NULL DEFAULT 0,
    latency_p95_ms double precision NOT NULL DEFAULT 0,
    latency_p99_ms double precision NOT NULL DEFAULT 0,
    latency_max_ms double precision NOT NULL DEFAULT 0,

    virtual_users int    NOT NULL DEFAULT 0,
    duration_ms   bigint NOT NULL DEFAULT 0,

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX runs_kind_idx ON runs (project_id, kind, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS run_metrics;
ALTER TABLE runs DROP COLUMN IF EXISTS load_profile;
ALTER TABLE runs DROP COLUMN IF EXISTS kind;
DROP TYPE IF EXISTS run_kind;
