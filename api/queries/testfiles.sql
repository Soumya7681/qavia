-- Generated test files.
--
-- Regeneration replaces a file at the same path rather than accumulating copies,
-- and every read of a file's content is deliberate: the browsing endpoint returns
-- the tree without it, because a 300-file suite is not something to load whole
-- to draw a sidebar (BE-3.5).

-- name: UpsertTestFile :one
INSERT INTO test_files (
    project_id, framework, path, content, test_case_ids, generated_by, size_bytes
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (project_id, framework, path) DO UPDATE
SET content       = EXCLUDED.content,
    test_case_ids = EXCLUDED.test_case_ids,
    generated_by  = EXCLUDED.generated_by,
    size_bytes    = EXCLUDED.size_bytes,
    -- A replaced file has not been validated again, so the previous verdict is
    -- cleared rather than carried over onto content it never saw.
    validated_at    = NULL,
    validation_note = '',
    updated_at    = now()
RETURNING id, project_id, framework, path, content, test_case_ids, generated_by,
          validated_at, validation_note, size_bytes, generated_at, updated_at;

-- name: GetTestFile :one
SELECT id, project_id, framework, path, content, test_case_ids, generated_by,
       validated_at, validation_note, size_bytes, generated_at, updated_at
FROM test_files
WHERE id = $1;

-- ListTestFiles is the tree query. Content is excluded on purpose: it is the one
-- column that makes this expensive, and a file browser does not need it.
-- name: ListTestFiles :many
SELECT id, project_id, framework, path, test_case_ids, generated_by,
       validated_at, validation_note, size_bytes, generated_at, updated_at
FROM test_files
WHERE project_id = $1
  AND (sqlc.narg('framework')::test_framework IS NULL
       OR framework = sqlc.narg('framework')::test_framework)
  AND (sqlc.narg('cursor')::text IS NULL OR path > sqlc.narg('cursor')::text)
ORDER BY path
LIMIT sqlc.arg('page_size');

-- ListTestFilesForExport streams the whole suite, content included. Ordered by
-- path so the zip is deterministic: two exports of the same suite produce the
-- same archive.
-- name: ListTestFilesForExport :many
SELECT id, project_id, framework, path, content, test_case_ids, generated_by,
       validated_at, validation_note, size_bytes, generated_at, updated_at
FROM test_files
WHERE project_id = $1
  AND (sqlc.narg('framework')::test_framework IS NULL
       OR framework = sqlc.narg('framework')::test_framework)
ORDER BY path;

-- FindTestFilesForCase answers "where is this case implemented", which is the
-- traceability half of F-6.11.
-- name: FindTestFilesForCase :many
SELECT id, project_id, framework, path, test_case_ids, generated_by,
       validated_at, validation_note, size_bytes, generated_at, updated_at
FROM test_files
WHERE project_id = $1 AND $2::uuid = ANY(test_case_ids)
ORDER BY path;

-- name: SetTestFileValidation :exec
UPDATE test_files
SET validated_at = now(), validation_note = $2, updated_at = now()
WHERE id = $1;

-- name: CountTestFiles :one
SELECT count(*) FROM test_files WHERE project_id = $1;

-- name: CountTestFilesByFramework :many
SELECT framework, count(*)::bigint AS total, sum(size_bytes)::bigint AS bytes
FROM test_files
WHERE project_id = $1
GROUP BY framework
ORDER BY framework;

-- name: DeleteTestFilesForFramework :execrows
DELETE FROM test_files WHERE project_id = $1 AND framework = $2;

-- ListApprovedTestCases feeds code generation: approved cases only, because
-- generating from a draft somebody is still editing wastes the call (BE-3.2).
-- name: ListApprovedTestCases :many
SELECT t.id, t.project_id, t.requirement_id, t.title, t.preconditions, t.steps,
       t.expected, t.priority, t.category, t.status, t.fingerprint, t.endpoint,
       t.superseded_by, t.generated_by, t.created_by, t.created_at, t.updated_at,
       -- The case's own endpoint wins: a requirement may cover the whole API,
       -- and the case knows which operation it calls. The join is the fallback
       -- for a case written before the column existed.
       coalesce(nullif(split_part(t.endpoint, ' ', 1), ''), e.method, '') AS method,
       coalesce(nullif(split_part(t.endpoint, ' ', 2), ''), e.path, '')   AS endpoint_path
FROM test_cases t
LEFT JOIN requirements r ON r.id = t.requirement_id
LEFT JOIN endpoints e ON e.id = r.endpoint_id
WHERE t.project_id = $1
  AND t.superseded_by IS NULL
  AND t.status = 'approved'
ORDER BY endpoint_path, method, t.created_at;
