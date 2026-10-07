-- +goose Up
-- The job pipeline. The user submits and leaves, and this is what makes that true.
--
-- These rows are the durable record of what ran. Redis holds only in-flight
-- coordination, so losing Redis loses in-flight jobs, which retry, and does not
-- lose data (tech-stack.md 6).

CREATE TABLE jobs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id    uuid        REFERENCES projects (id) ON DELETE CASCADE,

    -- Job type, matching the handler registration. Types grow with every phase,
    -- so text with no check: the registry in Go is the authority.
    type          text        NOT NULL,

    status        job_status  NOT NULL DEFAULT 'queued',
    progress      smallint    NOT NULL DEFAULT 0,
    attempts      smallint    NOT NULL DEFAULT 0,
    max_attempts  smallint    NOT NULL DEFAULT 3,
    error         text,

    -- Chains are declared in one pipeline file rather than spread across
    -- handlers. parent_job_id makes a chain queryable as a unit.
    parent_job_id uuid        REFERENCES jobs (id) ON DELETE CASCADE,

    payload       jsonb       NOT NULL DEFAULT '{}'::jsonb,

    -- Every handler declares one. NFR-4 is not optional: a retried job must not
    -- duplicate generated rows.
    idempotency_key text      NOT NULL,

    -- Travels on the context and into the payload, so one user action is
    -- traceable across five stages.
    correlation_id text       NOT NULL DEFAULT '',

    enqueued_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    queued_at     timestamptz NOT NULL DEFAULT now(),
    started_at    timestamptz,
    finished_at   timestamptz,

    CONSTRAINT jobs_progress_range CHECK (progress BETWEEN 0 AND 100)
);

-- The idempotency guarantee, enforced by the database rather than by a
-- check-then-write across two statements.
CREATE UNIQUE INDEX jobs_idempotency_key ON jobs (type, idempotency_key);

CREATE INDEX jobs_project_queued_idx ON jobs (project_id, queued_at DESC);
CREATE INDEX jobs_status_idx ON jobs (status) WHERE status IN ('queued', 'running');
CREATE INDEX jobs_parent_idx ON jobs (parent_job_id);

-- The live event log the SSE stream reads (F-2.3).
CREATE TABLE job_events (
    id      bigserial PRIMARY KEY,
    job_id  uuid            NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    level   job_event_level NOT NULL DEFAULT 'info',
    message text            NOT NULL,
    at      timestamptz     NOT NULL DEFAULT now()
);

-- bigserial ascending doubles as the SSE Last-Event-ID cursor, so a reconnect
-- resumes without gaps.
CREATE INDEX job_events_job_id_idx ON job_events (job_id, id);

-- +goose Down
DROP TABLE IF EXISTS job_events;
DROP TABLE IF EXISTS jobs;
