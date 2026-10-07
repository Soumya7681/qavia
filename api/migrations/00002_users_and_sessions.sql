-- +goose Up
-- Users, invitations, sessions, and login throttling.

CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text        NOT NULL,
    name            text        NOT NULL DEFAULT '',
    role            user_role   NOT NULL,

    -- Null until the invitation is accepted. There is no self-registration, so a
    -- user exists before they have a password (requirements.md 3).
    password_hash   text,

    timezone        text        NOT NULL DEFAULT 'UTC',
    disabled_at     timestamptz,
    last_login_at   timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- Email is the login identity, matched case-insensitively.
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE user_invitations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- The token is stored hashed. A leaked database must not yield working
    -- invitation links.
    token_hash   bytea       NOT NULL,

    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz,
    invited_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX user_invitations_token_hash_key ON user_invitations (token_hash);
CREATE INDEX user_invitations_user_id_idx ON user_invitations (user_id);

-- Sessions are server-side so revocation is immediate: an offboarded employee's
-- access ends when the row is deleted, with no token TTL to wait out
-- (tech-stack.md 10).
--
-- The first three columns are the shape alexedwards/scs expects from its
-- Postgres store. user_id is ours: scs has no notion of an owner, and without it
-- "revoke every session for this user" would mean decoding every blob. It is
-- nullable because scs writes the row before we know who is logging in, and it
-- is filled immediately after authentication.
CREATE TABLE sessions (
    token   text        PRIMARY KEY,
    data    bytea       NOT NULL,
    expiry  timestamptz NOT NULL,
    user_id uuid        REFERENCES users (id) ON DELETE CASCADE
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- Login attempts back the per-account and per-IP rate limit (F-1.2). Redis holds
-- the hot counters; this table is the durable record an admin can inspect and the
-- audit trail survives a Redis flush.
CREATE TABLE login_attempts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text        NOT NULL,
    ip            inet,
    succeeded     boolean     NOT NULL,
    user_agent    text        NOT NULL DEFAULT '',
    attempted_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX login_attempts_email_idx ON login_attempts (lower(email), attempted_at DESC);
CREATE INDEX login_attempts_ip_idx ON login_attempts (ip, attempted_at DESC);

-- An account lock is durable state, not a Redis key, so a restart cannot unlock
-- an account that repeated failures locked.
CREATE TABLE account_locks (
    email       text        PRIMARY KEY,
    locked_at   timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz NOT NULL,
    reason      text        NOT NULL DEFAULT 'too_many_failed_attempts'
);

-- +goose Down
DROP TABLE IF EXISTS account_locks;
DROP TABLE IF EXISTS login_attempts;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS user_invitations;
DROP TABLE IF EXISTS users;
