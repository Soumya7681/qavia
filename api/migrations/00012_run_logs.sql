-- +goose Up
-- Live runner output (BE-4.11).
--
-- The full log ends up in object storage, so this table is not the archive: it is
-- the tail a browser reads while a run is still going, and the history a viewer who
-- joined late needs in order to see anything at all.
--
-- The id is a bigserial for the same reason job_events uses one: it is both the
-- ordering and the SSE `Last-Event-ID` resume cursor, so a reconnect asks for
-- "everything after 4172" rather than "everything since a timestamp" and cannot
-- miss a line written in the same millisecond.
--
-- Bounded twice. The driver truncates a run's output at its own ceiling, so no
-- single run can write without limit, and the rows are deleted once the run
-- finishes and its log is safely stored. If storage failed, the rows stay: they are
-- then the only copy.

CREATE TABLE run_log_lines (
    id      bigserial PRIMARY KEY,
    run_id  uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    line    text NOT NULL,
    at      timestamptz NOT NULL DEFAULT now()
);

-- The only access pattern: this run's lines after this id, in order.
CREATE INDEX run_log_lines_run_id_idx ON run_log_lines (run_id, id);

-- +goose Down
DROP TABLE IF EXISTS run_log_lines;
