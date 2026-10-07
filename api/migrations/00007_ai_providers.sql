-- +goose Up
-- The AI provider layer (ai-architecture.md 3.2).
--
-- The shape here is what makes provider switching a dropdown change rather than a
-- deploy: an agent asks for a tier, a tier assignment names a model, and a model
-- belongs to a provider. Nothing in the agent code knows a vendor name.

-- Provider kinds from ai-architecture.md 3.1. openai-compatible is the row that
-- makes "any other provider" real: one adapter reaches Ollama, vLLM, LiteLLM,
-- OpenRouter, Together, Groq, Fireworks, and DeepSeek.
CREATE TYPE llm_provider_kind AS ENUM (
    'anthropic', 'bedrock', 'vertex', 'openai', 'azure-openai', 'gemini', 'openai-compatible'
);

-- Where a provider processes data. This is the server-side half of the NDA
-- problem (requirements.md 8.1): a project without external_ai_approved may only
-- be assigned a provider marked local.
CREATE TYPE llm_data_residency AS ENUM ('local', 'regional', 'external');

CREATE TYPE llm_health_status AS ENUM ('unknown', 'ok', 'failing');

-- What a call is for, rather than which model runs it.
CREATE TYPE llm_tier AS ENUM ('reasoning', 'code', 'cheap', 'vision');

CREATE TYPE llm_scope AS ENUM ('global', 'project');

CREATE TABLE llm_providers (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text              NOT NULL,
    kind        llm_provider_kind NOT NULL,

    -- Non-secret settings: base_url, region, project_id, api_version, deployment.
    config      jsonb             NOT NULL DEFAULT '{}'::jsonb,

    -- Encrypted at rest with the same envelope as settings secrets. The shape
    -- varies by kind (ai-architecture.md 3.3) and is validated in code, because a
    -- check constraint per kind would be seven constraints nobody reads.
    credentials jsonb             NOT NULL DEFAULT '{}'::jsonb,

    data_residency llm_data_residency NOT NULL DEFAULT 'external',

    is_enabled  boolean           NOT NULL DEFAULT true,
    is_default  boolean           NOT NULL DEFAULT false,

    health_status     llm_health_status NOT NULL DEFAULT 'unknown',
    health_checked_at timestamptz,
    health_detail     text        NOT NULL DEFAULT '',

    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz       NOT NULL DEFAULT now(),
    updated_at  timestamptz       NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX llm_providers_name_key ON llm_providers (lower(name));

-- At most one default. A partial unique index rather than a trigger: the database
-- enforces it on every path, including a migration somebody writes later.
CREATE UNIQUE INDEX llm_providers_one_default ON llm_providers (is_default) WHERE is_default;

CREATE TABLE llm_models (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id  uuid    NOT NULL REFERENCES llm_providers (id) ON DELETE CASCADE,

    -- The provider's own identifier: "claude-opus-5", "gpt-5",
    -- "anthropic.claude-opus-5", "qwen2.5-coder:32b".
    model_id     text    NOT NULL,
    display_name text    NOT NULL DEFAULT '',

    -- Which tiers this model may serve. Text array rather than an enum array so a
    -- new tier is a code change and not a migration.
    tiers        text[]  NOT NULL DEFAULT '{}',

    -- The capability matrix from ai-architecture.md 3.4. Features are gated on
    -- this: a model with tool_use false is disqualified from the agents that need
    -- tools, with the reason returned so the UI explains rather than hides.
    capabilities jsonb   NOT NULL DEFAULT '{}'::jsonb,

    -- USD per million tokens. numeric, never float: money does not round twice.
    -- Editable from the UI, because provider pricing changes and a deploy is the
    -- wrong way to track it (ai-architecture.md 3.7).
    price_input      numeric(12, 6) NOT NULL DEFAULT 0,
    price_output     numeric(12, 6) NOT NULL DEFAULT 0,
    price_cache_read numeric(12, 6),
    price_cache_write numeric(12, 6),

    max_input_tokens  integer,
    max_output_tokens integer,

    is_enabled  boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT llm_models_price_nonnegative CHECK (
        price_input >= 0 AND price_output >= 0
        AND (price_cache_read IS NULL OR price_cache_read >= 0)
        AND (price_cache_write IS NULL OR price_cache_write >= 0)
    )
);

CREATE UNIQUE INDEX llm_models_provider_model_key ON llm_models (provider_id, model_id);
CREATE INDEX llm_models_provider_idx ON llm_models (provider_id);

CREATE TABLE tier_assignments (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope    llm_scope NOT NULL,

    -- Null for global. A project assignment overrides the global one, and
    -- resolution ends at an error rather than a silent code default.
    scope_id uuid REFERENCES projects (id) ON DELETE CASCADE,

    tier     llm_tier NOT NULL,
    model_id uuid     NOT NULL REFERENCES llm_models (id) ON DELETE RESTRICT,

    -- Used only for retryable failures: rate limit, overload, connection failure.
    -- Never for a 400 (ai-architecture.md 3.8).
    fallback_model_id uuid REFERENCES llm_models (id) ON DELETE SET NULL,

    -- Provider-specific reasoning effort. Null where the provider has no such
    -- control, rather than a value invented to fill the column.
    effort   text,

    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT tier_assignments_scope_id_shape CHECK (
        (scope = 'global' AND scope_id IS NULL) OR (scope = 'project' AND scope_id IS NOT NULL)
    ),
    CONSTRAINT tier_assignments_fallback_differs CHECK (
        fallback_model_id IS NULL OR fallback_model_id <> model_id
    )
);

-- Two NULLs are distinct to a plain unique index, so the global rows need their
-- own partial index or a second global reasoning assignment would be allowed.
CREATE UNIQUE INDEX tier_assignments_project_key
    ON tier_assignments (scope_id, tier) WHERE scope = 'project';
CREATE UNIQUE INDEX tier_assignments_global_key
    ON tier_assignments (tier) WHERE scope = 'global';

-- Every call, so spend is a fact rather than an estimate (ai-architecture.md 3.7).
-- Python returns usage; this is written by Go and only by Go.
CREATE TABLE llm_calls (
    id          bigserial PRIMARY KEY,

    -- Providers and models are kept even if the row that named them is deleted:
    -- spend history that loses its subject is not spend history.
    provider_id uuid REFERENCES llm_providers (id) ON DELETE SET NULL,
    model_id    uuid REFERENCES llm_models (id) ON DELETE SET NULL,

    -- Recorded as text as well, so a deleted provider still names itself.
    provider_kind text NOT NULL DEFAULT '',
    model_name    text NOT NULL DEFAULT '',

    tier   llm_tier NOT NULL,
    agent  text     NOT NULL DEFAULT '',

    project_id uuid REFERENCES projects (id) ON DELETE CASCADE,
    job_id     uuid REFERENCES jobs (id) ON DELETE SET NULL,

    input_tokens       integer NOT NULL DEFAULT 0,
    output_tokens      integer NOT NULL DEFAULT 0,
    cache_read_tokens  integer NOT NULL DEFAULT 0,
    cache_write_tokens integer NOT NULL DEFAULT 0,

    -- Computed from the prices on llm_models at the time of the call, so a later
    -- price edit does not silently rewrite history.
    cost_usd numeric(14, 8) NOT NULL DEFAULT 0,

    latency_ms integer NOT NULL DEFAULT 0,

    -- True when the tier's fallback served the call. Cost attribution depends on
    -- it: the fallback has its own price list.
    fallback_used boolean NOT NULL DEFAULT false,

    at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT llm_calls_tokens_nonnegative CHECK (
        input_tokens >= 0 AND output_tokens >= 0
        AND cache_read_tokens >= 0 AND cache_write_tokens >= 0
    )
);

-- The spend dashboard aggregates on both of these.
CREATE INDEX llm_calls_project_at_idx ON llm_calls (project_id, at DESC);
CREATE INDEX llm_calls_provider_at_idx ON llm_calls (provider_id, at DESC);
CREATE INDEX llm_calls_at_idx ON llm_calls (at DESC);
CREATE INDEX llm_calls_job_idx ON llm_calls (job_id);

-- +goose Down
DROP TABLE IF EXISTS llm_calls;
DROP TABLE IF EXISTS tier_assignments;
DROP TABLE IF EXISTS llm_models;
DROP TABLE IF EXISTS llm_providers;
DROP TYPE IF EXISTS llm_scope;
DROP TYPE IF EXISTS llm_tier;
DROP TYPE IF EXISTS llm_health_status;
DROP TYPE IF EXISTS llm_data_residency;
DROP TYPE IF EXISTS llm_provider_kind;
