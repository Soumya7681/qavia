-- +goose Up
-- Execution: runs, their per-test results, and the command log.
--
-- This phase executes model-generated code derived from files a client uploaded,
-- so the schema is written for after-the-fact attribution as much as for the
-- dashboard. Every run records the target it was pointed at and the image that
-- served it, every result records which attempt produced it, and every command the
-- runner executed is kept.

CREATE TYPE run_status AS ENUM (
    'queued', 'running', 'passed', 'failed', 'cancelled', 'errored'
);

-- flaky and skipped are first-class, not derived at read time. A test whose result
-- changes between attempts is a different thing from one that failed, and
-- collapsing the two is how a suite loses its credibility (F-7.11).
CREATE TYPE run_result_status AS ENUM (
    'passed', 'failed', 'flaky', 'skipped', 'errored'
);

CREATE TYPE run_trigger AS ENUM ('manual', 'schedule', 'webhook', 'ci');

CREATE TABLE runs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- The job that drove it, so a run and its stages are one story.
    job_id uuid REFERENCES jobs (id) ON DELETE SET NULL,

    -- Where it actually ran against. Stored rather than resolved later: the
    -- allowlist and the target settings can both change, and "what did this run
    -- touch" has to stay answerable (BE-4.7).
    target_url text NOT NULL DEFAULT '',

    trigger run_trigger NOT NULL DEFAULT 'manual',
    status  run_status  NOT NULL DEFAULT 'queued',

    framework test_framework NOT NULL DEFAULT 'supertest',

    -- Which image served the run, by digest. A suite that behaved differently last
    -- week may have run on a different image, and a tag would not say so.
    image text NOT NULL DEFAULT '',

    -- Counts, maintained when results are written so the dashboard is one read.
    total   integer NOT NULL DEFAULT 0,
    passed  integer NOT NULL DEFAULT 0,
    failed  integer NOT NULL DEFAULT 0,
    flaky   integer NOT NULL DEFAULT 0,
    skipped integer NOT NULL DEFAULT 0,

    duration_ms integer NOT NULL DEFAULT 0,

    -- Why a run errored, as opposed to a test failing: an image that would not
    -- pull, a limit that was hit, a target that stopped answering.
    error text NOT NULL DEFAULT '',

    -- The whole run's log, in object storage. The live tail is ephemeral; this is
    -- what remains afterwards (BE-4.11).
    log_key text NOT NULL DEFAULT '',

    triggered_by uuid REFERENCES users (id) ON DELETE SET NULL,
    started_at   timestamptz,
    finished_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT runs_counts_nonnegative CHECK (
        total >= 0 AND passed >= 0 AND failed >= 0 AND flaky >= 0 AND skipped >= 0
    )
);

CREATE INDEX runs_project_started_idx ON runs (project_id, created_at DESC);
CREATE INDEX runs_status_idx ON runs (status) WHERE status IN ('queued', 'running');
CREATE INDEX runs_job_idx ON runs (job_id);

CREATE TABLE run_results (
    id     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,

    -- Null when a reported test could not be matched to a case: a suite somebody
    -- edited by hand, or a case deleted since. Keeping the result is better than
    -- dropping it, because the run did execute it.
    test_case_id uuid REFERENCES test_cases (id) ON DELETE SET NULL,
    test_file_id uuid REFERENCES test_files (id) ON DELETE SET NULL,

    -- What the reporter called it, kept verbatim so an unmatched result is still
    -- identifiable.
    name text NOT NULL DEFAULT '',

    status      run_result_status NOT NULL,
    duration_ms integer           NOT NULL DEFAULT 0,

    -- Which attempt produced this row. Flake detection re-runs failures, and every
    -- attempt is its own row so they are individually inspectable (BE-4.13).
    attempt integer NOT NULL DEFAULT 1,

    failure_message text NOT NULL DEFAULT '',

    -- Artifact keys in object storage rather than blobs in the database. A run with
    -- videos would otherwise make this table the largest thing in the system.
    log_key        text NOT NULL DEFAULT '',
    screenshot_key text NOT NULL DEFAULT '',
    video_key      text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT run_results_attempt_positive CHECK (attempt >= 1)
);

-- The dashboard's counts are one query over this index.
CREATE INDEX run_results_run_status_idx ON run_results (run_id, status);
CREATE INDEX run_results_case_idx ON run_results (test_case_id, created_at DESC);
CREATE INDEX run_results_file_idx ON run_results (test_file_id);

-- Every command the runner executed (F-17.2).
--
-- A separate table rather than a column on runs: a run is a handful of commands,
-- each with its own exit code and timing, and "reconstruct this run" means reading
-- them in order.
CREATE TABLE run_commands (
    id     bigserial PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,

    -- The command as executed, already redacted: it passes through the same
    -- handler as every log line, so a token in an argument never lands here.
    command text NOT NULL,

    exit_code   integer,
    duration_ms integer NOT NULL DEFAULT 0,

    -- Short excerpt for the audit view. The whole output is in object storage.
    output_excerpt text NOT NULL DEFAULT '',

    at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX run_commands_run_idx ON run_commands (run_id, id);

-- +goose Down
DROP TABLE IF EXISTS run_commands;
DROP TABLE IF EXISTS run_results;
DROP TABLE IF EXISTS runs;
DROP TYPE IF EXISTS run_trigger;
DROP TYPE IF EXISTS run_result_status;
DROP TYPE IF EXISTS run_status;
