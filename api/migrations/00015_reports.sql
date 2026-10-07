-- +goose Up
-- Generated reports (BE-5.9).
--
-- A report is an output, not an input, which is why it does not go in the artifacts
-- table: that table means "a file a client gave us", carries a sniffed content type
-- checked against an upload allowlist, and deduplicates by hash so an identical
-- re-upload is free. None of that is true of a report, and two of the three would be
-- actively wrong: two reports with identical bytes are two reports, taken at two
-- moments, and collapsing them would lose the second one's timestamp.
--
-- The row exists before the file does, for the same reason a job row exists before
-- its task: a report whose generation failed is visible and retryable, and a file in
-- object storage that nothing points at is invisible.

CREATE TYPE report_format AS ENUM ('html', 'pdf');

CREATE TYPE report_status AS ENUM ('queued', 'running', 'ready', 'failed');

CREATE TABLE reports (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    format report_format NOT NULL,
    status report_status NOT NULL DEFAULT 'queued',

    -- The window the report covers, so two reports of the same project are
    -- distinguishable by what they contain rather than only by when they were made.
    window_days int NOT NULL DEFAULT 30,

    -- Empty until the file exists. A ready report always has one.
    storage_key text NOT NULL DEFAULT '',
    size_bytes  bigint NOT NULL DEFAULT 0,

    -- Why a failed report failed, in words a user can act on.
    error text NOT NULL DEFAULT '',

    job_id       uuid REFERENCES jobs (id) ON DELETE SET NULL,
    requested_by uuid REFERENCES users (id) ON DELETE SET NULL,

    created_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);

CREATE INDEX reports_project_idx ON reports (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS reports;
DROP TYPE IF EXISTS report_status;
DROP TYPE IF EXISTS report_format;
