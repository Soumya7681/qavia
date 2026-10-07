-- +goose Up
-- The failing test's name on the defect (BE-5.7.2).
--
-- Duplicate detection matches "this test, failing this way", and the test case ID
-- alone cannot express that: a generated file covering several cases produces results
-- the platform deliberately leaves unmapped, because a wrong mapping puts a passing
-- result on a case that never ran. Those failures still recur, and they still deserve
-- one defect rather than one per run.
--
-- So the name is stored too, and it is the fallback half of the match. It is stable
-- for the same reason it is useful in a report: it is the test case's title, carried
-- through generation into the framework's own output.

ALTER TABLE defects ADD COLUMN test_name text NOT NULL DEFAULT '';

-- The fallback match, partial on open work for the same reason as the test-case
-- index: a failure recurring after a fix is a regression and deserves its own row.
CREATE INDEX defects_test_name_open_idx
    ON defects (project_id, test_name, root_cause_key)
    WHERE status IN ('open', 'acknowledged', 'in_progress')
      AND root_cause_key <> '' AND test_name <> '';

-- +goose Down
DROP INDEX IF EXISTS defects_test_name_open_idx;
ALTER TABLE defects DROP COLUMN IF EXISTS test_name;
