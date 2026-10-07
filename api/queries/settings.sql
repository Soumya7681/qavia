-- Settings and the settings audit trail.
--
-- Resolution is user -> project -> global -> registry default, and it happens in
-- one place in Go. These queries fetch the candidate rows; they do not decide.

-- name: GetSetting :one
SELECT id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at
FROM settings
WHERE key = $1
  AND scope = $2
  AND (scope_id IS NULL AND sqlc.narg('scope_id')::uuid IS NULL OR scope_id = sqlc.narg('scope_id')::uuid);

-- ResolveSetting returns every stored row that could satisfy one key for one
-- caller, ordered strongest scope first. Go takes the first and falls through to
-- the registry default when there is none, so a missing row is never a zero value.
-- name: ResolveSetting :many
SELECT id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at
FROM settings
WHERE key = $1
  AND (
    (scope = 'user' AND scope_id = sqlc.narg('user_id')::uuid)
    OR (scope = 'project' AND scope_id = sqlc.narg('project_id')::uuid)
    OR (scope = 'global' AND scope_id IS NULL)
  )
ORDER BY CASE scope WHEN 'user' THEN 0 WHEN 'project' THEN 1 ELSE 2 END;

-- ResolveSettingsForScope warms the whole cache in one round trip rather than one
-- query per key.
-- name: ResolveSettingsForScope :many
SELECT id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at
FROM settings
WHERE (
    (scope = 'user' AND scope_id = sqlc.narg('user_id')::uuid)
    OR (scope = 'project' AND scope_id = sqlc.narg('project_id')::uuid)
    OR (scope = 'global' AND scope_id IS NULL)
  )
ORDER BY key, CASE scope WHEN 'user' THEN 0 WHEN 'project' THEN 1 ELSE 2 END;

-- name: ListSettingsByScope :many
SELECT id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at
FROM settings
WHERE scope = $1
  AND (scope_id IS NULL AND sqlc.narg('scope_id')::uuid IS NULL OR scope_id = sqlc.narg('scope_id')::uuid)
ORDER BY key;

-- UpsertSetting is the only write path. The unique indexes make it idempotent, so
-- a retried job cannot produce a second row for the same key.
-- name: UpsertSetting :one
INSERT INTO settings (scope, scope_id, key, value, is_secret, updated_by)
VALUES ($1, sqlc.narg('scope_id')::uuid, $2, $3, $4, sqlc.narg('updated_by')::uuid)
ON CONFLICT (scope, scope_id, key) WHERE scope_id IS NOT NULL DO UPDATE
SET value = EXCLUDED.value,
    is_secret = EXCLUDED.is_secret,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at;

-- UpsertGlobalSetting exists separately because two NULL scope_ids are distinct to
-- a plain unique index, so global rows are constrained by their own partial index
-- and need their own ON CONFLICT target.
-- name: UpsertGlobalSetting :one
INSERT INTO settings (scope, scope_id, key, value, is_secret, updated_by)
VALUES ('global', NULL, $1, $2, $3, sqlc.narg('updated_by')::uuid)
ON CONFLICT (key) WHERE scope_id IS NULL DO UPDATE
SET value = EXCLUDED.value,
    is_secret = EXCLUDED.is_secret,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING id, scope, scope_id, key, value, is_secret, updated_by, created_at, updated_at;

-- name: DeleteSetting :execrows
DELETE FROM settings
WHERE key = $1
  AND scope = $2
  AND (scope_id IS NULL AND sqlc.narg('scope_id')::uuid IS NULL OR scope_id = sqlc.narg('scope_id')::uuid);

-- name: DeleteSettingsForScopeID :execrows
DELETE FROM settings WHERE scope = $1 AND scope_id = $2;

-- The audit row is written in the same transaction as the settings write. For a
-- secret both values arrive as '[redacted]': only the fact of the change is kept.
-- name: RecordSettingChange :exec
INSERT INTO settings_audit (scope, scope_id, key, old_value, new_value, actor_id)
VALUES ($1, sqlc.narg('scope_id')::uuid, $2, sqlc.narg('old_value')::jsonb, sqlc.narg('new_value')::jsonb, sqlc.narg('actor_id')::uuid);

-- name: ListSettingChanges :many
SELECT id, scope, scope_id, key, old_value, new_value, actor_id, at
FROM settings_audit
WHERE (sqlc.narg('key')::text IS NULL OR key = sqlc.narg('key')::text)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR at < sqlc.narg('cursor')::timestamptz)
ORDER BY at DESC
LIMIT sqlc.arg('page_size');
