-- +goose Up
-- Generated test files (F-6.1, F-6.9, F-6.11).
--
-- test_case_ids is the column that makes traceability work: a reviewer asks
-- "where is this case implemented" and "what does this file cover" from the same
-- row, rather than inferring it from a naming convention that will drift.

CREATE TYPE test_framework AS ENUM (
    'supertest', 'postman', 'jest', 'vitest', 'playwright', 'cypress', 'k6', 'pytest'
);

CREATE TABLE test_files (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    framework test_framework NOT NULL,

    -- Repository-relative, always forward-slashed, and validated before it is
    -- stored: a model wrote it, so a path that escapes the suite root is a real
    -- possibility rather than a theoretical one.
    path text NOT NULL,

    content text NOT NULL,

    -- Which cases this file implements. An array rather than a join table because
    -- it is read as a whole every time and written once per generation, and
    -- Postgres indexes it well enough for the two questions asked of it.
    test_case_ids uuid[] NOT NULL DEFAULT '{}',

    -- The model that produced it, so a suite's quality traces back to what wrote
    -- it (ai-architecture.md 3.5).
    generated_by text NOT NULL DEFAULT '',

    -- Set when static validation ran in the runner. Null means it has not been
    -- checked yet, which is different from having passed (BE-3.4).
    validated_at    timestamptz,
    validation_note text NOT NULL DEFAULT '',

    size_bytes   integer     NOT NULL DEFAULT 0,
    generated_at timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT test_files_path_not_empty CHECK (length(trim(path)) > 0)
);

-- Regenerating a suite replaces the file at the same path rather than adding a
-- second copy of it.
CREATE UNIQUE INDEX test_files_project_framework_path_key
    ON test_files (project_id, framework, path);

CREATE INDEX test_files_project_framework_idx ON test_files (project_id, framework);

-- Answers "which file covers this case" without scanning the suite.
CREATE INDEX test_files_case_ids_idx ON test_files USING gin (test_case_ids);

-- +goose Down
DROP TABLE IF EXISTS test_files;
DROP TYPE IF EXISTS test_framework;
