-- +goose Up
-- The endpoint a test case asserts against.
--
-- Not a duplicate of the requirement's endpoint: a case knows what it calls, and a
-- requirement may cover the whole API. "Bearer token required" is one auth rule
-- with no endpoint of its own, and the cases designed from it each target a
-- specific operation. Without this column, code generation and the Postman export
-- group those cases under nothing and emit a request to "/", which is a suite
-- nobody can run.
--
-- It is also what the fingerprint is already computed from, so storing it makes
-- the deduplication input inspectable instead of implied.
ALTER TABLE test_cases ADD COLUMN endpoint text NOT NULL DEFAULT '';

CREATE INDEX test_cases_project_endpoint_idx ON test_cases (project_id, endpoint)
    WHERE superseded_by IS NULL;

-- +goose Down
DROP INDEX IF EXISTS test_cases_project_endpoint_idx;
ALTER TABLE test_cases DROP COLUMN IF EXISTS endpoint;
