-- +goose NO TRANSACTION
-- +goose Up
-- A security probe run needs its own runner image, so it needs its own framework value
-- to map to one. The framework column is how a run finds its image, and giving security
-- a value there keeps the mapping in one place rather than special-casing it (BE-9.4).
--
-- ALTER TYPE ... ADD VALUE cannot run inside a transaction block, so this migration runs
-- without one — the annotation at the top of the file.
ALTER TYPE test_framework ADD VALUE IF NOT EXISTS 'security';

-- +goose Down
-- Postgres cannot drop a value from an enum, so the down migration is a no-op: the value
-- is harmless when unused, and removing it would mean rebuilding the type.
SELECT 1;
