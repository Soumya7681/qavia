-- Repository connections (BE-6.1).
--
-- The token is never here: `credential_ref` names a settings key, and the settings
-- store owns encryption and rotation for every secret in the platform.

-- name: UpsertRepoConnection :one
INSERT INTO repo_connections (
    project_id, provider, repo_url, default_branch, credential_ref, created_by
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (project_id) DO UPDATE
SET provider = EXCLUDED.provider,
    repo_url = EXCLUDED.repo_url,
    default_branch = EXCLUDED.default_branch,
    credential_ref = EXCLUDED.credential_ref,
    updated_at = now()
RETURNING id, project_id, provider, repo_url, default_branch, credential_ref,
          last_commit, last_fetched_at, last_error, created_by, created_at, updated_at,
          detected, detected_at;

-- name: GetRepoConnection :one
SELECT id, project_id, provider, repo_url, default_branch, credential_ref,
       last_commit, last_fetched_at, last_error, created_by, created_at, updated_at,
       detected, detected_at
FROM repo_connections
WHERE project_id = $1;

-- name: DeleteRepoConnection :execrows
DELETE FROM repo_connections WHERE project_id = $1;

-- RecordRepoFetch stores what a clone actually got. The commit rather than the
-- branch, because a branch moves and a map of a repository has to say which
-- revision it describes.
-- name: RecordRepoFetch :exec
UPDATE repo_connections
SET last_commit = $2, last_fetched_at = now(), last_error = ''
WHERE project_id = $1;

-- RecordRepoDetection stores the detected stack and the files it was read from.
-- name: RecordRepoDetection :exec
UPDATE repo_connections
SET detected = $2, detected_at = now()
WHERE project_id = $1;

-- name: RecordRepoFetchError :exec
UPDATE repo_connections
SET last_error = $2, last_fetched_at = now()
WHERE project_id = $1;

-- LatestSourceArchive is what an archive-backed sync expands. The newest upload of
-- the kind, because a project that re-uploaded its source meant the new one.
-- name: LatestSourceArchive :one
SELECT id, filename, storage_key
FROM artifacts
WHERE project_id = $1 AND kind = 'source_archive'
ORDER BY created_at DESC
LIMIT 1;

-- Comprehension maps (BE-6.4). One row per exploration: a map describes a revision,
-- and last month's map is history rather than something to overwrite.

-- name: CreateRepoMap :one
INSERT INTO repo_maps (project_id, commit_sha, stack, document, steps, cut_short, model_name, job_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, project_id, commit_sha, stack, document, steps, cut_short, model_name,
          job_id, created_at;

-- name: LatestRepoMap :one
SELECT id, project_id, commit_sha, stack, document, steps, cut_short, model_name,
       job_id, created_at
FROM repo_maps
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListRepoMaps :many
SELECT id, project_id, commit_sha, stack, steps, cut_short, model_name, created_at
FROM repo_maps
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');
