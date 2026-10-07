-- Users, invitations, and login throttling.
--
-- Explicit column lists everywhere. SELECT * is a review failure: an added column
-- must not silently change the API surface or pull a credential into memory.

-- name: CreateUser :one
INSERT INTO users (email, name, role, timezone)
VALUES ($1, $2, $3, $4)
RETURNING id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at;

-- name: GetUserByID :one
SELECT id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at
FROM users
WHERE lower(email) = lower($1);

-- name: ListUsers :many
SELECT id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at
FROM users
WHERE (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- ListUsersByRole backs admin fan-out: a degraded integration raises an alert for
-- every admin, and a disabled account is not somebody who will read it.
-- name: ListUsersByRole :many
SELECT id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at
FROM users
WHERE role = $1 AND disabled_at IS NULL
ORDER BY created_at;

-- CountUsers backs the first-run check: the setup endpoint is available only while
-- no user exists, then permanently 404.
-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: SetUserPassword :exec
UPDATE users
SET password_hash = $2, updated_at = now()
WHERE id = $1;

-- name: SetUserRole :exec
UPDATE users
SET role = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users
SET name = $2, timezone = $3, updated_at = now()
WHERE id = $1
RETURNING id, email, name, role, password_hash, timezone, disabled_at, last_login_at, created_at, updated_at;

-- name: SetUserDisabled :exec
UPDATE users
SET disabled_at = $2, updated_at = now()
WHERE id = $1;

-- name: TouchUserLogin :exec
UPDATE users
SET last_login_at = now()
WHERE id = $1;

-- name: CreateInvitation :one
INSERT INTO user_invitations (user_id, token_hash, expires_at, invited_by)
VALUES ($1, $2, $3, $4)
RETURNING id, user_id, token_hash, expires_at, accepted_at, invited_by, created_at;

-- GetInvitationByTokenHash looks up by hash, never by the token itself: a leaked
-- database must not yield working invitation links.
-- name: GetInvitationByTokenHash :one
SELECT id, user_id, token_hash, expires_at, accepted_at, invited_by, created_at
FROM user_invitations
WHERE token_hash = $1;

-- name: AcceptInvitation :execrows
UPDATE user_invitations
SET accepted_at = now()
WHERE id = $1 AND accepted_at IS NULL AND expires_at > now();

-- name: DeleteExpiredInvitations :execrows
DELETE FROM user_invitations
WHERE accepted_at IS NULL AND expires_at < now();

-- name: RecordLoginAttempt :exec
INSERT INTO login_attempts (email, ip, succeeded, user_agent)
VALUES ($1, $2, $3, $4);

-- name: CountRecentFailedAttempts :one
SELECT count(*)
FROM login_attempts
WHERE lower(email) = lower(sqlc.arg('email')::text)
  AND succeeded = false
  AND attempted_at > sqlc.arg('since')::timestamptz;

-- name: CountRecentFailedAttemptsByIP :one
SELECT count(*)
FROM login_attempts
WHERE ip = sqlc.arg('ip')::inet
  AND succeeded = false
  AND attempted_at > sqlc.arg('since')::timestamptz;

-- name: LockAccount :exec
INSERT INTO account_locks (email, locked_until, reason)
VALUES (lower(sqlc.arg('email')::text), sqlc.arg('locked_until')::timestamptz, sqlc.arg('reason')::text)
ON CONFLICT (email) DO UPDATE
SET locked_at = now(), locked_until = EXCLUDED.locked_until, reason = EXCLUDED.reason;

-- name: GetAccountLock :one
SELECT email, locked_at, locked_until, reason
FROM account_locks
WHERE email = lower(sqlc.arg('email')::text) AND locked_until > now();

-- name: ClearAccountLock :execrows
DELETE FROM account_locks WHERE email = lower(sqlc.arg('email')::text);

-- name: DeleteStaleLoginAttempts :execrows
DELETE FROM login_attempts WHERE attempted_at < sqlc.arg('before')::timestamptz;
