-- Artifacts: uploaded or connected inputs, versioned by content hash.

-- CreateArtifact relies on the unique index on (project_id, sha256): an identical
-- re-upload conflicts and the service returns the existing row with
-- deduplicated: true rather than re-running generation (FR-1.4).
--
-- The id is supplied rather than generated here because the storage key contains
-- it, and the object is written before the row exists: a key built after the
-- insert would mean either a second statement to fill it in or a row that briefly
-- points nowhere.
-- name: CreateArtifact :one
INSERT INTO artifacts (id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, sqlc.narg('uploaded_by')::uuid)
RETURNING id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by, created_at;

-- name: GetArtifact :one
SELECT id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by, created_at
FROM artifacts
WHERE id = $1;

-- name: GetArtifactByHash :one
SELECT id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by, created_at
FROM artifacts
WHERE project_id = $1 AND sha256 = $2;

-- FindArtifactLineage locates the previous versions of the same logical input, by
-- filename and kind within a project. This is what a re-upload of a changed spec
-- attaches itself to, and what phase 11 diffs against.
-- name: FindArtifactLineage :one
SELECT lineage_id, max(version)::int AS latest_version
FROM artifacts
WHERE project_id = $1 AND kind = $2 AND filename = $3
GROUP BY lineage_id
ORDER BY max(version) DESC
LIMIT 1;

-- name: ListArtifacts :many
SELECT id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by, created_at
FROM artifacts
WHERE project_id = $1
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind')::text)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: ListArtifactVersions :many
SELECT id, project_id, kind, filename, storage_key, content_type, size_bytes, sha256, version, lineage_id, uploaded_by, created_at
FROM artifacts
WHERE lineage_id = $1
ORDER BY version DESC;

-- name: DeleteArtifact :execrows
DELETE FROM artifacts WHERE id = $1;

-- ListArtifactsOlderThan drives the retention job. Retention is a setting in days
-- (F-17.5).
-- name: ListArtifactsOlderThan :many
SELECT id, project_id, storage_key
FROM artifacts
WHERE created_at < $1
ORDER BY created_at
LIMIT sqlc.arg('page_size');
