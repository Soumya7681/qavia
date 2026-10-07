-- +goose Up
-- The trace key on a result (BE-7.5.2).
--
-- The row already carried a screenshot and a video. A Playwright trace is the third
-- and, for a UI failure, usually the most useful of the three: it is the one that
-- shows what the page looked like at each step, what the network did, and where the
-- click landed. Keeping it in its own column rather than folding all three into a
-- jsonb blob means a query can ask "which failures have a trace" without parsing.

ALTER TABLE run_results ADD COLUMN trace_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE run_results DROP COLUMN IF EXISTS trace_key;
