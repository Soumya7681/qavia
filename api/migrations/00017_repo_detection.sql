-- +goose Up
-- What detection found in a checkout (BE-6.3).
--
-- Stored on the connection rather than recomputed on every read, because the answer
-- only changes when the repository does: a manifest is read once per fetch, and every
-- later stage — which image to run, which command to invoke, which coverage report to
-- parse — asks the same question.
--
-- The inspected file list is stored with it, and that is the part that matters. An
-- unknown stack is only actionable if an operator can see what the platform looked at
-- before giving up (BE-6.3.3).

ALTER TABLE repo_connections
    ADD COLUMN detected    jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN detected_at timestamptz;

-- +goose Down
ALTER TABLE repo_connections
    DROP COLUMN IF EXISTS detected_at,
    DROP COLUMN IF EXISTS detected;
