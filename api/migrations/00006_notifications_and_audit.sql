-- +goose Up
-- In-app notifications and the audit log.

-- The built-in Notifier. Always available, zero configuration, and never blocked
-- by an unconfigured email or Slack channel (FR-8.2).
CREATE TABLE notifications (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- Kinds grow with every phase, so text with a check rather than an enum.
    kind       text        NOT NULL,

    title      text        NOT NULL,
    body       text        NOT NULL DEFAULT '',

    -- Every notification carries a deep link to the result (FR-8.5).
    link       text        NOT NULL DEFAULT '',

    read_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notifications_kind_check CHECK (kind IN (
        'job_completed', 'job_failed', 'job_needs_input',
        'run_completed', 'drift_detected', 'integration_failed', 'admin_alert'
    ))
);

-- Backs both the list and the unread badge without a second index.
CREATE INDEX notifications_user_created_idx ON notifications (user_id, created_at DESC);
CREATE INDEX notifications_user_unread_idx ON notifications (user_id) WHERE read_at IS NULL;

-- Privileged actions (F-17.1, requirements.md 8.4). Detail is a typed jsonb and
-- passes through the same redaction as the logger, so no row here holds a secret.
CREATE TABLE audit_log (
    id         bigserial PRIMARY KEY,

    -- Null for an action taken by the system, such as a scheduled drift check.
    actor_id   uuid        REFERENCES users (id) ON DELETE SET NULL,

    -- Recorded separately because the actor row may later be deleted, and an
    -- audit entry that cannot name who acted is not an audit entry.
    actor_email text       NOT NULL DEFAULT '',

    action     text        NOT NULL,
    subject    text        NOT NULL DEFAULT '',
    project_id uuid        REFERENCES projects (id) ON DELETE SET NULL,
    ip         inet,
    detail     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_at_idx ON audit_log (at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_id, at DESC);
CREATE INDEX audit_log_action_idx ON audit_log (action, at DESC);
CREATE INDEX audit_log_project_idx ON audit_log (project_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS notifications;
