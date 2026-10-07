-- Ingest and generation: endpoints, requirements, and test cases.
--
-- Two ideas run through this file. Re-running work is free, because the unique
-- indexes turn a repeat into a conflict rather than a duplicate. And bulk writes
-- are one statement: 400 test cases is a CopyFrom, not 400 round trips inside a
-- transaction that is now long (backend-standards.md 8).

-- UpsertEndpoint makes re-parsing an artifact idempotent. A changed spec updates
-- the row it already had rather than leaving two versions of one endpoint.
-- name: UpsertEndpoint :one
INSERT INTO endpoints (
    project_id, artifact_id, method, path, operation_id, summary, description,
    parameters, request, responses, security, source_ref
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (artifact_id, method, path) DO UPDATE
SET operation_id = EXCLUDED.operation_id,
    summary      = EXCLUDED.summary,
    description  = EXCLUDED.description,
    parameters   = EXCLUDED.parameters,
    request      = EXCLUDED.request,
    responses    = EXCLUDED.responses,
    security     = EXCLUDED.security,
    source_ref   = EXCLUDED.source_ref
RETURNING id, project_id, artifact_id, method, path, operation_id, summary, description,
          parameters, request, responses, security, source_ref, created_at;

-- name: ListEndpoints :many
SELECT id, project_id, artifact_id, method, path, operation_id, summary, description,
       parameters, request, responses, security, source_ref, created_at
FROM endpoints
WHERE project_id = $1
  AND (sqlc.narg('artifact_id')::uuid IS NULL OR artifact_id = sqlc.narg('artifact_id')::uuid)
ORDER BY path, method;

-- name: CountEndpoints :one
SELECT count(*) FROM endpoints WHERE project_id = $1;

-- name: DeleteEndpointsForArtifact :execrows
DELETE FROM endpoints WHERE artifact_id = $1;

-- CreateRequirement conflicts on the fingerprint, so re-running extract on an
-- unchanged artifact writes nothing new and returns what was already there.
-- name: CreateRequirement :one
INSERT INTO requirements (
    project_id, artifact_id, endpoint_id, kind, title, body, source_ref, fingerprint, generated_by
)
VALUES (
    $1, sqlc.narg('artifact_id')::uuid, sqlc.narg('endpoint_id')::uuid,
    $2, $3, $4, $5, $6, $7
)
ON CONFLICT (project_id, fingerprint) DO UPDATE
SET title = EXCLUDED.title, body = EXCLUDED.body
RETURNING id, project_id, artifact_id, endpoint_id, kind, title, body, source_ref,
          fingerprint, generated_by, created_at;

-- name: GetRequirement :one
SELECT id, project_id, artifact_id, endpoint_id, kind, title, body, source_ref,
       fingerprint, generated_by, created_at
FROM requirements
WHERE id = $1;

-- name: ListRequirements :many
SELECT id, project_id, artifact_id, endpoint_id, kind, title, body, source_ref,
       fingerprint, generated_by, created_at
FROM requirements
WHERE project_id = $1
  AND (sqlc.narg('artifact_id')::uuid IS NULL OR artifact_id = sqlc.narg('artifact_id')::uuid)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: CountRequirements :one
SELECT count(*) FROM requirements WHERE project_id = $1;

-- RequirementCoverage answers "which requirements have test cases" as one indexed
-- query, which is what BE-2.1 exists to make possible. Passing coverage arrives
-- with runs in phase 4; this is the generation half (FR-7.2 keeps the two numbers
-- separate).
-- name: RequirementCoverage :many
SELECT r.id, r.kind, r.title, r.source_ref,
       count(t.id) FILTER (WHERE t.superseded_by IS NULL)::bigint AS test_case_count
FROM requirements r
LEFT JOIN test_cases t ON t.requirement_id = r.id
WHERE r.project_id = $1
GROUP BY r.id, r.kind, r.title, r.source_ref
ORDER BY test_case_count, r.title;

-- name: CountCoveredRequirements :one
SELECT count(DISTINCT r.id)
FROM requirements r
JOIN test_cases t ON t.requirement_id = r.id AND t.superseded_by IS NULL
WHERE r.project_id = $1;

-- CreateTestCase is the single-row path, used by manual authoring and by the
-- dedupe pass. Bulk generation uses CopyFrom instead.
-- name: CreateTestCase :one
INSERT INTO test_cases (
    project_id, requirement_id, title, preconditions, steps, expected,
    priority, category, status, fingerprint, endpoint, generated_by, created_by
)
VALUES (
    $1, sqlc.narg('requirement_id')::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
    sqlc.narg('created_by')::uuid
)
RETURNING id, project_id, requirement_id, title, preconditions, steps, expected,
          priority, category, status, fingerprint, superseded_by, generated_by,
          created_by, created_at, updated_at, endpoint;

-- name: GetTestCase :one
SELECT id, project_id, requirement_id, title, preconditions, steps, expected,
       priority, category, status, fingerprint, superseded_by, generated_by,
       created_by, created_at, updated_at, endpoint
FROM test_cases
WHERE id = $1;

-- name: UpdateTestCase :one
UPDATE test_cases
SET title = $2, preconditions = $3, steps = $4, expected = $5,
    priority = $6, category = $7, status = $8, updated_at = now()
WHERE id = $1 AND superseded_by IS NULL
RETURNING id, project_id, requirement_id, title, preconditions, steps, expected,
          priority, category, status, fingerprint, superseded_by, generated_by,
          created_by, created_at, updated_at, endpoint;

-- SetTestCaseStatus backs bulk approve and reject. The batch is capped by the
-- service and runs in one transaction.
-- name: SetTestCaseStatus :execrows
UPDATE test_cases
SET status = $2, updated_at = now()
WHERE id = ANY(sqlc.arg('ids')::uuid[]) AND project_id = $1 AND superseded_by IS NULL;

-- SupersedeTestCase records a merge rather than deleting the loser, so "why is
-- this case gone" has an answer.
-- name: SupersedeTestCase :execrows
UPDATE test_cases
SET superseded_by = $2, updated_at = now()
WHERE id = $1 AND superseded_by IS NULL;

-- name: DeleteTestCase :execrows
DELETE FROM test_cases WHERE id = $1;

-- ListTestCasesByFingerprint is how the generation pass learns what it already
-- has before writing, so an unchanged spec produces zero new rows.
-- name: ListTestCasesByFingerprint :many
SELECT id, fingerprint
FROM test_cases
WHERE project_id = $1 AND superseded_by IS NULL
  AND fingerprint = ANY(sqlc.arg('fingerprints')::bytea[]);

-- ListTestCasesForRequirement is the narrow candidate set the AI dedupe pass
-- compares against, which is what keeps that cost bounded (F-5.4).
-- name: ListTestCasesForRequirement :many
SELECT id, project_id, requirement_id, title, preconditions, steps, expected,
       priority, category, status, fingerprint, superseded_by, generated_by,
       created_by, created_at, updated_at, endpoint
FROM test_cases
WHERE requirement_id = $1 AND superseded_by IS NULL
ORDER BY created_at;

-- name: CountTestCases :one
SELECT count(*) FROM test_cases WHERE project_id = $1 AND superseded_by IS NULL;

-- name: CountTestCasesByStatus :many
SELECT status, count(*)::bigint AS total
FROM test_cases
WHERE project_id = $1 AND superseded_by IS NULL
GROUP BY status;

-- CreateTestCasesBulk is the generation write path: 400 cases is one statement
-- rather than 400 round trips inside a transaction that is now long
-- (backend-standards.md 8).
-- name: CreateTestCasesBulk :copyfrom
INSERT INTO test_cases (
    project_id, requirement_id, title, preconditions, steps, expected,
    priority, category, status, fingerprint, endpoint, generated_by
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);
