-- +goose Up
-- Repository connections (BE-6.1).
--
-- Two ways in, and only two in this phase: an archive somebody uploaded, which
-- already works, and a clone by URL with an optional token. OAuth against GitHub or
-- GitLab is phase 10 and optional, which is the point — a client who will not grant
-- an OAuth app access to their monorepo can still hand over a URL and a read-only
-- token, or a zip.
--
-- The token is not in this table. It lives in the settings store, encrypted with the
-- platform's key like every other secret, and `credential_ref` records which key
-- holds it. A credential column here would be a second place secrets live, with its
-- own rotation story and its own way of ending up in a database dump
-- (requirements.md 5.1).

CREATE TYPE repo_provider AS ENUM ('git', 'github', 'gitlab', 'bitbucket', 'archive');

CREATE TABLE repo_connections (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    provider repo_provider NOT NULL DEFAULT 'git',

    -- The clone URL as an operator entered it. Checked against the SSRF rules before
    -- any clone, the same way a run's target is: a repository URL is a URL a user
    -- supplied, and this platform fetches it from a worker inside the network
    -- (BE-4.7).
    repo_url text NOT NULL,

    default_branch text NOT NULL DEFAULT '',

    -- The settings key holding the access token, or empty for a public repository.
    -- A name, not a secret.
    credential_ref text NOT NULL DEFAULT '',

    -- What the last clone actually fetched, so a comprehension map or a coverage
    -- number can say which commit it describes.
    last_commit    text NOT NULL DEFAULT '',
    last_fetched_at timestamptz,
    last_error     text NOT NULL DEFAULT '',

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One connection per project for now. A project with two repositories is a real
-- thing, and it is also a thing to design deliberately rather than to allow by
-- omission: every consumer in this phase asks "the repository for this project".
CREATE UNIQUE INDEX repo_connections_project_key ON repo_connections (project_id);

-- +goose Down
DROP TABLE IF EXISTS repo_connections;
DROP TYPE IF EXISTS repo_provider;
