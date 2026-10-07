-- Sessions.
--
-- The first three columns are the shape alexedwards/scs expects. user_id is ours:
-- scs has no notion of an owner, and without it "revoke every session for this
-- user" would mean decoding every blob.

-- name: FindSession :one
SELECT token, data, expiry, user_id
FROM sessions
WHERE token = $1 AND expiry > now();

-- name: UpsertSession :exec
INSERT INTO sessions (token, data, expiry)
VALUES ($1, $2, $3)
ON CONFLICT (token) DO UPDATE
SET data = EXCLUDED.data, expiry = EXCLUDED.expiry;

-- name: DeleteSession :execrows
DELETE FROM sessions WHERE token = $1;

-- name: ListAllSessions :many
SELECT token, data, expiry, user_id
FROM sessions
WHERE expiry > now();

-- SetSessionUser runs immediately after authentication, because scs writes the
-- row before anyone knows who is signing in.
-- name: SetSessionUser :execrows
UPDATE sessions SET user_id = $2 WHERE token = $1;

-- DeleteSessionsForUser is the immediate-revocation path: offboarding, disabling an
-- account, or a password change.
-- name: DeleteSessionsForUser :execrows
DELETE FROM sessions WHERE user_id = $1;

-- name: DeleteOtherSessionsForUser :execrows
DELETE FROM sessions WHERE user_id = $1 AND token <> $2;

-- name: CountSessionsForUser :one
SELECT count(*) FROM sessions WHERE user_id = $1 AND expiry > now();

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expiry < now();
