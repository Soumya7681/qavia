-- The built-in defect tracker (BE-5.5).
--
-- It ships before any external tracker exists, because bug tracking has to work on
-- an installation with no Jira and no GitHub. The external_ref column is where a
-- tracker somebody else owns gets recorded later; on a zero-integration install it
-- stays empty and nothing here changes.
--
-- The filtered list is built with squirrel in the owning store rather than generated
-- here: five optional filters is thirty-two queries or one unindexable one
-- (backend-standards.md 9).

-- name: CreateDefect :one
INSERT INTO defects (
    project_id, run_result_id, test_case_id, requirement_id, analysis_id,
    title, description, severity, status, assignee_id, root_cause_key, created_by,
    test_name
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
          title, description, severity, status, assignee_id, duplicate_of,
          root_cause_key, external_ref, created_by, created_at, updated_at,
          resolved_at, test_name;

-- name: GetDefect :one
SELECT id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
       title, description, severity, status, assignee_id, duplicate_of,
       root_cause_key, external_ref, created_by, created_at, updated_at,
       resolved_at, test_name
FROM defects
WHERE id = $1;

-- DefectForResult is the promotion guard: promoting the same failure twice returns
-- the defect that already exists (BE-5.6.3).
-- name: DefectForResult :one
SELECT id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
       title, description, severity, status, assignee_id, duplicate_of,
       root_cause_key, external_ref, created_by, created_at, updated_at,
       resolved_at, test_name
FROM defects
WHERE run_result_id = $1;

-- OpenDefectWithRootCause is duplicate detection, and it is a lookup rather than a
-- model call: the same test case with the same normalised root cause is the same
-- defect (BE-5.7.2). Closed defects are excluded, because a failure recurring after
-- a fix is a regression and deserves its own row.
-- name: OpenDefectWithRootCause :one
SELECT id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
       title, description, severity, status, assignee_id, duplicate_of,
       root_cause_key, external_ref, created_by, created_at, updated_at,
       resolved_at, test_name
FROM defects
WHERE project_id = $1
  AND test_case_id = $2
  AND root_cause_key = $3
  AND status IN ('open', 'acknowledged', 'in_progress')
  AND duplicate_of IS NULL
ORDER BY created_at
LIMIT 1;

-- OpenDefectWithRootCauseByName is the fallback when a result could not be mapped to
-- exactly one test case. The test's name plus the normalised root cause is still
-- "this test, failing this way" (BE-5.7.2).
-- name: OpenDefectWithRootCauseByName :one
SELECT id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
       title, description, severity, status, assignee_id, duplicate_of,
       root_cause_key, external_ref, created_by, created_at, updated_at,
       resolved_at, test_name
FROM defects
WHERE project_id = $1
  AND test_name = $2
  AND root_cause_key = $3
  AND status IN ('open', 'acknowledged', 'in_progress')
  AND duplicate_of IS NULL
ORDER BY created_at
LIMIT 1;

-- name: UpdateDefect :one
UPDATE defects
SET title = coalesce(sqlc.narg('title'), title),
    description = coalesce(sqlc.narg('description'), description),
    severity = coalesce(sqlc.narg('severity')::defect_severity, severity),
    status = coalesce(sqlc.narg('status')::defect_status, status),
    assignee_id = CASE WHEN sqlc.narg('clear_assignee')::bool THEN NULL
                       ELSE coalesce(sqlc.narg('assignee_id'), assignee_id) END,
    -- resolved_at follows the status rather than being set by a caller: a defect that
    -- is fixed but has no resolution time is a defect nobody can measure.
    resolved_at = CASE
        WHEN coalesce(sqlc.narg('status')::defect_status, status)
             IN ('fixed', 'wont_fix', 'duplicate') THEN coalesce(resolved_at, now())
        ELSE NULL
    END,
    updated_at = now()
WHERE id = $1
RETURNING id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
          title, description, severity, status, assignee_id, duplicate_of,
          root_cause_key, external_ref, created_by, created_at, updated_at,
          resolved_at, test_name;

-- LinkDuplicate is reversible: passing null unlinks, because the platform proposes
-- and a person decides (BE-5.7.3).
-- name: LinkDuplicate :one
UPDATE defects
SET duplicate_of = sqlc.narg('duplicate_of'),
    status = CASE WHEN sqlc.narg('duplicate_of')::uuid IS NULL THEN 'open'::defect_status
                  ELSE 'duplicate'::defect_status END,
    resolved_at = CASE WHEN sqlc.narg('duplicate_of')::uuid IS NULL THEN NULL
                       ELSE coalesce(resolved_at, now()) END,
    updated_at = now()
WHERE id = $1
RETURNING id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
          title, description, severity, status, assignee_id, duplicate_of,
          root_cause_key, external_ref, created_by, created_at, updated_at,
          resolved_at, test_name;

-- name: SetDefectExternalRef :exec
UPDATE defects
SET external_ref = $2, updated_at = now()
WHERE id = $1;

-- ListDuplicatesOf is how one defect shows its recurrences: three runs failing the
-- same way is one defect with three linked occurrences (BE-5.7).
-- name: ListDuplicatesOf :many
SELECT id, project_id, run_result_id, test_case_id, requirement_id, analysis_id,
       title, description, severity, status, assignee_id, duplicate_of,
       root_cause_key, external_ref, created_by, created_at, updated_at,
       resolved_at, test_name
FROM defects
WHERE duplicate_of = $1
ORDER BY created_at;

-- name: CountDefectsByStatus :many
SELECT status, count(*)::bigint AS total
FROM defects
WHERE project_id = $1
GROUP BY status;

-- name: CreateDefectComment :one
INSERT INTO defect_comments (defect_id, author_id, body, system)
VALUES ($1, $2, $3, $4)
RETURNING id, defect_id, author_id, body, system, created_at;

-- name: ListDefectComments :many
SELECT id, defect_id, author_id, body, system, created_at
FROM defect_comments
WHERE defect_id = $1
  AND (sqlc.narg('after')::timestamptz IS NULL OR created_at > sqlc.narg('after')::timestamptz)
ORDER BY created_at
LIMIT sqlc.arg('page_size');

-- TouchDefect keeps updated_at meaningful when the change was a comment rather than
-- a field: a thread that moved is a defect that moved.
-- name: TouchDefect :exec
UPDATE defects SET updated_at = now() WHERE id = $1;
