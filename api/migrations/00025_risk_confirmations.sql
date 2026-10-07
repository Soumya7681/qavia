-- +goose Up
-- Confirmed hosts for performance and security testing (BE-9.5, F-11.5, F-11.6).
--
-- These two test kinds are the ones that, pointed at a machine you do not own, are
-- indistinguishable from an attack. So the platform demands an explicit confirmation
-- naming the exact host the first time either kind runs against it, and remembers that
-- confirmation so the demand is once per new host rather than once per run — a prompt on
-- every run trains people to click through it, which is the opposite of the control.
--
-- The row is the memory. It records who confirmed which host for which kind and when,
-- which is also the audit a security review asks for after the fact: not just that a
-- scan happened, but that a named person authorised it against a named target.

CREATE TABLE risk_confirmations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- performance or security. Kept as text keyed with the host so the same host can be
    -- confirmed for a load test and separately for a security scan: authorising one is
    -- not authorising the other.
    kind text NOT NULL,
    host text NOT NULL,

    confirmed_by uuid REFERENCES users (id) ON DELETE SET NULL,
    confirmed_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX risk_confirmations_unique ON risk_confirmations (project_id, kind, host);

-- +goose Down
DROP TABLE IF EXISTS risk_confirmations;
