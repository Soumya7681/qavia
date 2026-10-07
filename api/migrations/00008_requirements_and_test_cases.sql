-- +goose Up
-- Ingest and generation: the normalized endpoint model, extracted requirements,
-- and the test cases designed from them.
--
-- Two columns carry most of the weight here. `source_ref` is what makes every
-- generated item traceable back to a line in the file somebody uploaded, which is
-- the difference between output a QA engineer trusts and output they audit by
-- hand. `fingerprint` is what makes re-running generation free: the deduplication
-- guarantee is a unique index, not a habit in a service.

CREATE TYPE requirement_kind AS ENUM (
    'feature', 'business_rule', 'validation_rule', 'auth', 'edge_case'
);

CREATE TYPE test_priority AS ENUM ('critical', 'high', 'medium', 'low');

CREATE TYPE test_category AS ENUM (
    'functional', 'negative', 'boundary', 'security', 'auth', 'performance', 'data'
);

CREATE TYPE test_case_status AS ENUM ('draft', 'approved', 'rejected');

-- The normalized endpoint model, produced by deterministic parsing and never by a
-- model (ai-architecture.md 2). OpenAPI and Postman both land here, so everything
-- downstream is format-agnostic.
CREATE TABLE endpoints (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    artifact_id uuid NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,

    method text NOT NULL,
    path   text NOT NULL,

    operation_id text NOT NULL DEFAULT '',
    summary      text NOT NULL DEFAULT '',
    description  text NOT NULL DEFAULT '',

    -- Parameters, request body, responses, and security, each a named Go type
    -- rather than a map (backend-standards.md 9).
    parameters jsonb NOT NULL DEFAULT '[]'::jsonb,
    request    jsonb NOT NULL DEFAULT '{}'::jsonb,
    responses  jsonb NOT NULL DEFAULT '[]'::jsonb,
    security   jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- Where this came from in the uploaded file, as "line:column" or a character
    -- range for pasted text. Kept for every normalized item, because a generated
    -- test that cannot be traced back is a claim rather than a citation.
    source_ref text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now()
);

-- Re-parsing the same artifact updates rather than duplicating, which is what
-- makes the ingest job idempotent (BE-2.5).
CREATE UNIQUE INDEX endpoints_artifact_operation_key
    ON endpoints (artifact_id, method, path);
CREATE INDEX endpoints_project_idx ON endpoints (project_id, created_at DESC);

-- What the extract agent understood: features, business rules, validation rules,
-- the auth model, and edge cases.
CREATE TABLE requirements (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    artifact_id uuid REFERENCES artifacts (id) ON DELETE CASCADE,

    -- Null for a requirement that is not about one endpoint, such as a global auth
    -- rule.
    endpoint_id uuid REFERENCES endpoints (id) ON DELETE SET NULL,

    kind  requirement_kind NOT NULL,
    title text NOT NULL,
    body  text NOT NULL DEFAULT '',

    source_ref text NOT NULL DEFAULT '',

    -- Hash of the normalized (kind, title, source_ref). Re-running ingest on an
    -- unchanged artifact writes nothing new, and that is enforced by the index
    -- below rather than by a check-then-write in the service.
    fingerprint bytea NOT NULL,

    -- Which provider and model produced this, so output quality traces back to
    -- what generated it (ai-architecture.md 3.5).
    generated_by text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT requirements_fingerprint_length CHECK (octet_length(fingerprint) = 32)
);

CREATE UNIQUE INDEX requirements_project_fingerprint_key
    ON requirements (project_id, fingerprint);
CREATE INDEX requirements_project_artifact_idx ON requirements (project_id, artifact_id);
CREATE INDEX requirements_endpoint_idx ON requirements (endpoint_id);

CREATE TABLE test_cases (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id     uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    requirement_id uuid REFERENCES requirements (id) ON DELETE CASCADE,

    title         text NOT NULL,
    preconditions text NOT NULL DEFAULT '',

    -- An ordered list of {action, data, expected}, as a named Go type. Never
    -- map[string]any past the store boundary.
    steps jsonb NOT NULL DEFAULT '[]'::jsonb,

    expected text NOT NULL DEFAULT '',

    priority test_priority    NOT NULL DEFAULT 'medium',
    category test_category    NOT NULL DEFAULT 'functional',
    status   test_case_status NOT NULL DEFAULT 'draft',

    -- Deterministic hash of the normalized (method, path, assertion kind). Free,
    -- exact, and instant: the AI dedupe pass is only for near misses (F-5.3).
    fingerprint bytea NOT NULL,

    -- Set when a later case replaced this one, so a merge keeps its history rather
    -- than deleting the evidence.
    superseded_by uuid REFERENCES test_cases (id) ON DELETE SET NULL,

    generated_by text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT test_cases_fingerprint_length CHECK (octet_length(fingerprint) = 32)
);

-- The deduplication guarantee. Partial, so a superseded case does not block the
-- case that replaced it.
CREATE UNIQUE INDEX test_cases_project_fingerprint_key
    ON test_cases (project_id, fingerprint) WHERE superseded_by IS NULL;

CREATE INDEX test_cases_project_requirement_idx ON test_cases (project_id, requirement_id);
CREATE INDEX test_cases_project_status_idx ON test_cases (project_id, status)
    WHERE superseded_by IS NULL;
CREATE INDEX test_cases_project_created_idx ON test_cases (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS test_cases;
DROP TABLE IF EXISTS requirements;
DROP TABLE IF EXISTS endpoints;
DROP TYPE IF EXISTS test_case_status;
DROP TYPE IF EXISTS test_category;
DROP TYPE IF EXISTS test_priority;
DROP TYPE IF EXISTS requirement_kind;
