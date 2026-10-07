-- +goose Up
-- Projects, membership, and uploaded artifacts.

CREATE TABLE projects (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    description text        NOT NULL DEFAULT '',
    owner_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,

    -- Enabled test types, chosen at creation and editable in project settings
    -- (FR-1.2). Types not yet implemented are returned as unavailable with a
    -- reason so the UI can disable rather than hide them.
    test_types  text[]      NOT NULL DEFAULT '{}',

    -- Server-side gate for data residency (F-16.13, requirements.md 8.1). A
    -- project without this may only be assigned providers marked local, checked
    -- before any AI job is enqueued.
    external_ai_approved boolean NOT NULL DEFAULT false,

    -- Soft delete, because history is a requirement here. Everything else in this
    -- schema hard-deletes.
    archived_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX projects_owner_idx ON projects (owner_id);
CREATE INDEX projects_created_at_idx ON projects (created_at DESC);

CREATE TABLE project_members (
    project_id uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       user_role   NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX project_members_user_idx ON project_members (user_id);

-- Artifacts are uploaded or connected inputs: a spec file, a repository archive,
-- a pasted requirement.
--
-- sha256 and version carry more weight than they look (requirements.md 7). They
-- are what make the entire maintenance module possible, and they ship now
-- because retrofitting them later is a rewrite.
CREATE TABLE artifacts (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid        NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- Values may grow (openapi, postman, requirement_text, source_archive,
    -- sql_dump, pdf), so text with a check rather than an enum.
    kind        text        NOT NULL,

    filename    text        NOT NULL,
    storage_key text        NOT NULL,
    content_type text       NOT NULL DEFAULT '',
    size_bytes  bigint      NOT NULL,

    sha256      bytea       NOT NULL,

    -- Monotonic per (project, logical artifact). A changed file for the same
    -- logical input increments this and keeps the previous row, which is what
    -- phase 11 diffs against.
    version     integer     NOT NULL DEFAULT 1,

    -- Groups the versions of one logical input. Set to the first version's id.
    lineage_id  uuid        NOT NULL,

    uploaded_by uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT artifacts_kind_check CHECK (kind IN (
        'openapi', 'postman', 'requirement_text', 'source_archive', 'sql_dump', 'document'
    )),
    CONSTRAINT artifacts_size_check CHECK (size_bytes >= 0),
    CONSTRAINT artifacts_sha256_length CHECK (octet_length(sha256) = 32)
);

-- Identical re-upload does not re-run generation (FR-1.4). The uniqueness is
-- enforced here rather than by a check-then-write in the service.
CREATE UNIQUE INDEX artifacts_project_sha256_key ON artifacts (project_id, sha256);

CREATE UNIQUE INDEX artifacts_lineage_version_key ON artifacts (lineage_id, version);
CREATE INDEX artifacts_project_created_idx ON artifacts (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS project_members;
DROP TABLE IF EXISTS projects;
