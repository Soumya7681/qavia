-- +goose Up
-- Extensions and the closed-set enums.
--
-- Convention (backend-standards.md 9): a Postgres enum for a genuinely closed
-- set, text with a check constraint where the values may grow. Roles and job
-- statuses are closed. Artifact kinds and notification kinds are not.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- The four platform roles (requirements.md 3). Adding a fifth is a product
-- decision, not a config change, so an enum is the right shape.
CREATE TYPE user_role AS ENUM ('admin', 'qa_lead', 'qa_engineer', 'viewer');

-- Settings resolve user -> project -> global -> code default (requirements.md 5.3).
CREATE TYPE settings_scope AS ENUM ('global', 'project', 'user');

-- Job lifecycle. Illegal transitions are rejected in one place in Go; the enum
-- stops an unknown value reaching the column at all.
CREATE TYPE job_status AS ENUM ('queued', 'running', 'succeeded', 'failed', 'cancelled');

CREATE TYPE job_event_level AS ENUM ('debug', 'info', 'warn', 'error');

-- +goose Down
DROP TYPE IF EXISTS job_event_level;
DROP TYPE IF EXISTS job_status;
DROP TYPE IF EXISTS settings_scope;
DROP TYPE IF EXISTS user_role;
DROP EXTENSION IF EXISTS pgcrypto;
