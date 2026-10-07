-- Qavia schema. GENERATED FILE, do not edit.
--
-- Rebuilt by scripts/dump-schema.sh, which applies every migration in
-- api/migrations to a scratch database and dumps the result. CI regenerates it
-- and fails on a diff, so a hand-edited migration cannot merge.
--
-- Read this top to bottom to understand the data model. Change it by writing a
-- migration.


CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

CREATE TYPE public.job_event_level AS ENUM (
    'debug',
    'info',
    'warn',
    'error'
);

CREATE TYPE public.job_status AS ENUM (
    'queued',
    'running',
    'succeeded',
    'failed',
    'cancelled'
);

CREATE TYPE public.llm_data_residency AS ENUM (
    'local',
    'regional',
    'external'
);

CREATE TYPE public.llm_health_status AS ENUM (
    'unknown',
    'ok',
    'failing'
);

CREATE TYPE public.llm_provider_kind AS ENUM (
    'anthropic',
    'bedrock',
    'vertex',
    'openai',
    'azure-openai',
    'gemini',
    'openai-compatible'
);

CREATE TYPE public.llm_scope AS ENUM (
    'global',
    'project'
);

CREATE TYPE public.llm_tier AS ENUM (
    'reasoning',
    'code',
    'cheap',
    'vision'
);

CREATE TYPE public.requirement_kind AS ENUM (
    'feature',
    'business_rule',
    'validation_rule',
    'auth',
    'edge_case'
);

CREATE TYPE public.run_result_status AS ENUM (
    'passed',
    'failed',
    'flaky',
    'skipped',
    'errored'
);

CREATE TYPE public.run_status AS ENUM (
    'queued',
    'running',
    'passed',
    'failed',
    'cancelled',
    'errored'
);

CREATE TYPE public.run_trigger AS ENUM (
    'manual',
    'schedule',
    'webhook',
    'ci'
);

CREATE TYPE public.settings_scope AS ENUM (
    'global',
    'project',
    'user'
);

CREATE TYPE public.test_case_status AS ENUM (
    'draft',
    'approved',
    'rejected'
);

CREATE TYPE public.test_category AS ENUM (
    'functional',
    'negative',
    'boundary',
    'security',
    'auth',
    'performance',
    'data'
);

CREATE TYPE public.test_framework AS ENUM (
    'supertest',
    'postman',
    'jest',
    'vitest',
    'playwright',
    'cypress',
    'k6',
    'pytest'
);

CREATE TYPE public.test_priority AS ENUM (
    'critical',
    'high',
    'medium',
    'low'
);

CREATE TYPE public.user_role AS ENUM (
    'admin',
    'qa_lead',
    'qa_engineer',
    'viewer'
);

CREATE TABLE public.account_locks (
    email text NOT NULL,
    locked_at timestamp with time zone DEFAULT now() NOT NULL,
    locked_until timestamp with time zone NOT NULL,
    reason text DEFAULT 'too_many_failed_attempts'::text NOT NULL
);

CREATE TABLE public.artifacts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    kind text NOT NULL,
    filename text NOT NULL,
    storage_key text NOT NULL,
    content_type text DEFAULT ''::text NOT NULL,
    size_bytes bigint NOT NULL,
    sha256 bytea NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    lineage_id uuid NOT NULL,
    uploaded_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT artifacts_kind_check CHECK ((kind = ANY (ARRAY['openapi'::text, 'postman'::text, 'requirement_text'::text, 'source_archive'::text, 'sql_dump'::text, 'document'::text]))),
    CONSTRAINT artifacts_sha256_length CHECK ((octet_length(sha256) = 32)),
    CONSTRAINT artifacts_size_check CHECK ((size_bytes >= 0))
);

CREATE TABLE public.audit_log (
    id bigint NOT NULL,
    actor_id uuid,
    actor_email text DEFAULT ''::text NOT NULL,
    action text NOT NULL,
    subject text DEFAULT ''::text NOT NULL,
    project_id uuid,
    ip inet,
    detail jsonb DEFAULT '{}'::jsonb NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.audit_log_id_seq OWNED BY public.audit_log.id;

CREATE TABLE public.endpoints (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid NOT NULL,
    method text NOT NULL,
    path text NOT NULL,
    operation_id text DEFAULT ''::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    parameters jsonb DEFAULT '[]'::jsonb NOT NULL,
    request jsonb DEFAULT '{}'::jsonb NOT NULL,
    responses jsonb DEFAULT '[]'::jsonb NOT NULL,
    security jsonb DEFAULT '[]'::jsonb NOT NULL,
    source_ref text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.job_events (
    id bigint NOT NULL,
    job_id uuid NOT NULL,
    level public.job_event_level DEFAULT 'info'::public.job_event_level NOT NULL,
    message text NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.job_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.job_events_id_seq OWNED BY public.job_events.id;

CREATE TABLE public.jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid,
    type text NOT NULL,
    status public.job_status DEFAULT 'queued'::public.job_status NOT NULL,
    progress smallint DEFAULT 0 NOT NULL,
    attempts smallint DEFAULT 0 NOT NULL,
    max_attempts smallint DEFAULT 3 NOT NULL,
    error text,
    parent_job_id uuid,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    idempotency_key text NOT NULL,
    correlation_id text DEFAULT ''::text NOT NULL,
    enqueued_by uuid,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    CONSTRAINT jobs_progress_range CHECK (((progress >= 0) AND (progress <= 100)))
);

CREATE TABLE public.llm_calls (
    id bigint NOT NULL,
    provider_id uuid,
    model_id uuid,
    provider_kind text DEFAULT ''::text NOT NULL,
    model_name text DEFAULT ''::text NOT NULL,
    tier public.llm_tier NOT NULL,
    agent text DEFAULT ''::text NOT NULL,
    project_id uuid,
    job_id uuid,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    cache_read_tokens integer DEFAULT 0 NOT NULL,
    cache_write_tokens integer DEFAULT 0 NOT NULL,
    cost_usd numeric(14,8) DEFAULT 0 NOT NULL,
    latency_ms integer DEFAULT 0 NOT NULL,
    fallback_used boolean DEFAULT false NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT llm_calls_tokens_nonnegative CHECK (((input_tokens >= 0) AND (output_tokens >= 0) AND (cache_read_tokens >= 0) AND (cache_write_tokens >= 0)))
);

CREATE SEQUENCE public.llm_calls_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.llm_calls_id_seq OWNED BY public.llm_calls.id;

CREATE TABLE public.llm_models (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    provider_id uuid NOT NULL,
    model_id text NOT NULL,
    display_name text DEFAULT ''::text NOT NULL,
    tiers text[] DEFAULT '{}'::text[] NOT NULL,
    capabilities jsonb DEFAULT '{}'::jsonb NOT NULL,
    price_input numeric(12,6) DEFAULT 0 NOT NULL,
    price_output numeric(12,6) DEFAULT 0 NOT NULL,
    price_cache_read numeric(12,6),
    price_cache_write numeric(12,6),
    max_input_tokens integer,
    max_output_tokens integer,
    is_enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT llm_models_price_nonnegative CHECK (((price_input >= (0)::numeric) AND (price_output >= (0)::numeric) AND ((price_cache_read IS NULL) OR (price_cache_read >= (0)::numeric)) AND ((price_cache_write IS NULL) OR (price_cache_write >= (0)::numeric))))
);

CREATE TABLE public.llm_providers (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    kind public.llm_provider_kind NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    credentials jsonb DEFAULT '{}'::jsonb NOT NULL,
    data_residency public.llm_data_residency DEFAULT 'external'::public.llm_data_residency NOT NULL,
    is_enabled boolean DEFAULT true NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    health_status public.llm_health_status DEFAULT 'unknown'::public.llm_health_status NOT NULL,
    health_checked_at timestamp with time zone,
    health_detail text DEFAULT ''::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.login_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    ip inet,
    succeeded boolean NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    attempted_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    title text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    link text DEFAULT ''::text NOT NULL,
    read_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notifications_kind_check CHECK ((kind = ANY (ARRAY['job_completed'::text, 'job_failed'::text, 'job_needs_input'::text, 'run_completed'::text, 'drift_detected'::text, 'integration_failed'::text, 'admin_alert'::text])))
);

CREATE TABLE public.project_members (
    project_id uuid NOT NULL,
    user_id uuid NOT NULL,
    role public.user_role NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.projects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    owner_id uuid NOT NULL,
    test_types text[] DEFAULT '{}'::text[] NOT NULL,
    external_ai_approved boolean DEFAULT false NOT NULL,
    archived_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.requirements (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    artifact_id uuid,
    endpoint_id uuid,
    kind public.requirement_kind NOT NULL,
    title text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    source_ref text DEFAULT ''::text NOT NULL,
    fingerprint bytea NOT NULL,
    generated_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT requirements_fingerprint_length CHECK ((octet_length(fingerprint) = 32))
);

CREATE TABLE public.run_commands (
    id bigint NOT NULL,
    run_id uuid NOT NULL,
    command text NOT NULL,
    exit_code integer,
    duration_ms integer DEFAULT 0 NOT NULL,
    output_excerpt text DEFAULT ''::text NOT NULL,
    at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE SEQUENCE public.run_commands_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.run_commands_id_seq OWNED BY public.run_commands.id;

CREATE TABLE public.run_results (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    test_case_id uuid,
    test_file_id uuid,
    name text DEFAULT ''::text NOT NULL,
    status public.run_result_status NOT NULL,
    duration_ms integer DEFAULT 0 NOT NULL,
    attempt integer DEFAULT 1 NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    log_key text DEFAULT ''::text NOT NULL,
    screenshot_key text DEFAULT ''::text NOT NULL,
    video_key text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT run_results_attempt_positive CHECK ((attempt >= 1))
);

CREATE TABLE public.runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    job_id uuid,
    target_url text DEFAULT ''::text NOT NULL,
    trigger public.run_trigger DEFAULT 'manual'::public.run_trigger NOT NULL,
    status public.run_status DEFAULT 'queued'::public.run_status NOT NULL,
    framework public.test_framework DEFAULT 'supertest'::public.test_framework NOT NULL,
    image text DEFAULT ''::text NOT NULL,
    total integer DEFAULT 0 NOT NULL,
    passed integer DEFAULT 0 NOT NULL,
    failed integer DEFAULT 0 NOT NULL,
    flaky integer DEFAULT 0 NOT NULL,
    skipped integer DEFAULT 0 NOT NULL,
    duration_ms integer DEFAULT 0 NOT NULL,
    error text DEFAULT ''::text NOT NULL,
    log_key text DEFAULT ''::text NOT NULL,
    triggered_by uuid,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT runs_counts_nonnegative CHECK (((total >= 0) AND (passed >= 0) AND (failed >= 0) AND (flaky >= 0) AND (skipped >= 0)))
);

CREATE TABLE public.sessions (
    token text NOT NULL,
    data bytea NOT NULL,
    expiry timestamp with time zone NOT NULL,
    user_id uuid
);

CREATE TABLE public.settings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scope public.settings_scope NOT NULL,
    scope_id uuid,
    key text NOT NULL,
    value jsonb NOT NULL,
    is_secret boolean DEFAULT false NOT NULL,
    updated_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.settings_audit (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scope public.settings_scope NOT NULL,
    scope_id uuid,
    key text NOT NULL,
    old_value jsonb,
    new_value jsonb,
    actor_id uuid,
    at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.test_cases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    requirement_id uuid,
    title text NOT NULL,
    preconditions text DEFAULT ''::text NOT NULL,
    steps jsonb DEFAULT '[]'::jsonb NOT NULL,
    expected text DEFAULT ''::text NOT NULL,
    priority public.test_priority DEFAULT 'medium'::public.test_priority NOT NULL,
    category public.test_category DEFAULT 'functional'::public.test_category NOT NULL,
    status public.test_case_status DEFAULT 'draft'::public.test_case_status NOT NULL,
    fingerprint bytea NOT NULL,
    superseded_by uuid,
    generated_by text DEFAULT ''::text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    endpoint text DEFAULT ''::text NOT NULL,
    CONSTRAINT test_cases_fingerprint_length CHECK ((octet_length(fingerprint) = 32))
);

CREATE TABLE public.test_files (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    project_id uuid NOT NULL,
    framework public.test_framework NOT NULL,
    path text NOT NULL,
    content text NOT NULL,
    test_case_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    generated_by text DEFAULT ''::text NOT NULL,
    validated_at timestamp with time zone,
    validation_note text DEFAULT ''::text NOT NULL,
    size_bytes integer DEFAULT 0 NOT NULL,
    generated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT test_files_path_not_empty CHECK ((length(TRIM(BOTH FROM path)) > 0))
);

CREATE TABLE public.tier_assignments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scope public.llm_scope NOT NULL,
    scope_id uuid,
    tier public.llm_tier NOT NULL,
    model_id uuid NOT NULL,
    fallback_model_id uuid,
    effort text,
    updated_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tier_assignments_fallback_differs CHECK (((fallback_model_id IS NULL) OR (fallback_model_id <> model_id))),
    CONSTRAINT tier_assignments_scope_id_shape CHECK ((((scope = 'global'::public.llm_scope) AND (scope_id IS NULL)) OR ((scope = 'project'::public.llm_scope) AND (scope_id IS NOT NULL))))
);

CREATE TABLE public.user_invitations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    accepted_at timestamp with time zone,
    invited_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

CREATE TABLE public.users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    name text DEFAULT ''::text NOT NULL,
    role public.user_role NOT NULL,
    password_hash text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    disabled_at timestamp with time zone,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.audit_log ALTER COLUMN id SET DEFAULT nextval('public.audit_log_id_seq'::regclass);

ALTER TABLE ONLY public.job_events ALTER COLUMN id SET DEFAULT nextval('public.job_events_id_seq'::regclass);

ALTER TABLE ONLY public.llm_calls ALTER COLUMN id SET DEFAULT nextval('public.llm_calls_id_seq'::regclass);

ALTER TABLE ONLY public.run_commands ALTER COLUMN id SET DEFAULT nextval('public.run_commands_id_seq'::regclass);

ALTER TABLE ONLY public.account_locks
    ADD CONSTRAINT account_locks_pkey PRIMARY KEY (email);

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.endpoints
    ADD CONSTRAINT endpoints_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.job_events
    ADD CONSTRAINT job_events_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_calls
    ADD CONSTRAINT llm_calls_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_models
    ADD CONSTRAINT llm_models_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.llm_providers
    ADD CONSTRAINT llm_providers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.login_attempts
    ADD CONSTRAINT login_attempts_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_pkey PRIMARY KEY (project_id, user_id);

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.requirements
    ADD CONSTRAINT requirements_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.run_commands
    ADD CONSTRAINT run_commands_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.run_results
    ADD CONSTRAINT run_results_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.runs
    ADD CONSTRAINT runs_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (token);

ALTER TABLE ONLY public.settings_audit
    ADD CONSTRAINT settings_audit_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.test_cases
    ADD CONSTRAINT test_cases_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.test_files
    ADD CONSTRAINT test_files_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.tier_assignments
    ADD CONSTRAINT tier_assignments_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.user_invitations
    ADD CONSTRAINT user_invitations_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

CREATE UNIQUE INDEX artifacts_lineage_version_key ON public.artifacts USING btree (lineage_id, version);

CREATE INDEX artifacts_project_created_idx ON public.artifacts USING btree (project_id, created_at DESC);

CREATE UNIQUE INDEX artifacts_project_sha256_key ON public.artifacts USING btree (project_id, sha256);

CREATE INDEX audit_log_action_idx ON public.audit_log USING btree (action, at DESC);

CREATE INDEX audit_log_actor_idx ON public.audit_log USING btree (actor_id, at DESC);

CREATE INDEX audit_log_at_idx ON public.audit_log USING btree (at DESC);

CREATE INDEX audit_log_project_idx ON public.audit_log USING btree (project_id, at DESC);

CREATE UNIQUE INDEX endpoints_artifact_operation_key ON public.endpoints USING btree (artifact_id, method, path);

CREATE INDEX endpoints_project_idx ON public.endpoints USING btree (project_id, created_at DESC);

CREATE INDEX job_events_job_id_idx ON public.job_events USING btree (job_id, id);

CREATE UNIQUE INDEX jobs_idempotency_key ON public.jobs USING btree (type, idempotency_key);

CREATE INDEX jobs_parent_idx ON public.jobs USING btree (parent_job_id);

CREATE INDEX jobs_project_queued_idx ON public.jobs USING btree (project_id, queued_at DESC);

CREATE INDEX jobs_status_idx ON public.jobs USING btree (status) WHERE (status = ANY (ARRAY['queued'::public.job_status, 'running'::public.job_status]));

CREATE INDEX llm_calls_at_idx ON public.llm_calls USING btree (at DESC);

CREATE INDEX llm_calls_job_idx ON public.llm_calls USING btree (job_id);

CREATE INDEX llm_calls_project_at_idx ON public.llm_calls USING btree (project_id, at DESC);

CREATE INDEX llm_calls_provider_at_idx ON public.llm_calls USING btree (provider_id, at DESC);

CREATE INDEX llm_models_provider_idx ON public.llm_models USING btree (provider_id);

CREATE UNIQUE INDEX llm_models_provider_model_key ON public.llm_models USING btree (provider_id, model_id);

CREATE UNIQUE INDEX llm_providers_name_key ON public.llm_providers USING btree (lower(name));

CREATE UNIQUE INDEX llm_providers_one_default ON public.llm_providers USING btree (is_default) WHERE is_default;

CREATE INDEX login_attempts_email_idx ON public.login_attempts USING btree (lower(email), attempted_at DESC);

CREATE INDEX login_attempts_ip_idx ON public.login_attempts USING btree (ip, attempted_at DESC);

CREATE INDEX notifications_user_created_idx ON public.notifications USING btree (user_id, created_at DESC);

CREATE INDEX notifications_user_unread_idx ON public.notifications USING btree (user_id) WHERE (read_at IS NULL);

CREATE INDEX project_members_user_idx ON public.project_members USING btree (user_id);

CREATE INDEX projects_created_at_idx ON public.projects USING btree (created_at DESC);

CREATE INDEX projects_owner_idx ON public.projects USING btree (owner_id);

CREATE INDEX requirements_endpoint_idx ON public.requirements USING btree (endpoint_id);

CREATE INDEX requirements_project_artifact_idx ON public.requirements USING btree (project_id, artifact_id);

CREATE UNIQUE INDEX requirements_project_fingerprint_key ON public.requirements USING btree (project_id, fingerprint);

CREATE INDEX run_commands_run_idx ON public.run_commands USING btree (run_id, id);

CREATE INDEX run_results_case_idx ON public.run_results USING btree (test_case_id, created_at DESC);

CREATE INDEX run_results_file_idx ON public.run_results USING btree (test_file_id);

CREATE INDEX run_results_run_status_idx ON public.run_results USING btree (run_id, status);

CREATE INDEX runs_job_idx ON public.runs USING btree (job_id);

CREATE INDEX runs_project_started_idx ON public.runs USING btree (project_id, created_at DESC);

CREATE INDEX runs_status_idx ON public.runs USING btree (status) WHERE (status = ANY (ARRAY['queued'::public.run_status, 'running'::public.run_status]));

CREATE INDEX sessions_expiry_idx ON public.sessions USING btree (expiry);

CREATE INDEX sessions_user_id_idx ON public.sessions USING btree (user_id);

CREATE INDEX settings_audit_actor_idx ON public.settings_audit USING btree (actor_id, at DESC);

CREATE INDEX settings_audit_key_idx ON public.settings_audit USING btree (key, at DESC);

CREATE UNIQUE INDEX settings_global_key ON public.settings USING btree (key) WHERE (scope_id IS NULL);

CREATE INDEX settings_key_idx ON public.settings USING btree (key);

CREATE UNIQUE INDEX settings_scoped_key ON public.settings USING btree (scope, scope_id, key) WHERE (scope_id IS NOT NULL);

CREATE INDEX test_cases_project_created_idx ON public.test_cases USING btree (project_id, created_at DESC);

CREATE INDEX test_cases_project_endpoint_idx ON public.test_cases USING btree (project_id, endpoint) WHERE (superseded_by IS NULL);

CREATE UNIQUE INDEX test_cases_project_fingerprint_key ON public.test_cases USING btree (project_id, fingerprint) WHERE (superseded_by IS NULL);

CREATE INDEX test_cases_project_requirement_idx ON public.test_cases USING btree (project_id, requirement_id);

CREATE INDEX test_cases_project_status_idx ON public.test_cases USING btree (project_id, status) WHERE (superseded_by IS NULL);

CREATE INDEX test_files_case_ids_idx ON public.test_files USING gin (test_case_ids);

CREATE INDEX test_files_project_framework_idx ON public.test_files USING btree (project_id, framework);

CREATE UNIQUE INDEX test_files_project_framework_path_key ON public.test_files USING btree (project_id, framework, path);

CREATE UNIQUE INDEX tier_assignments_global_key ON public.tier_assignments USING btree (tier) WHERE (scope = 'global'::public.llm_scope);

CREATE UNIQUE INDEX tier_assignments_project_key ON public.tier_assignments USING btree (scope_id, tier) WHERE (scope = 'project'::public.llm_scope);

CREATE UNIQUE INDEX user_invitations_token_hash_key ON public.user_invitations USING btree (token_hash);

CREATE INDEX user_invitations_user_id_idx ON public.user_invitations USING btree (user_id);

CREATE UNIQUE INDEX users_email_key ON public.users USING btree (lower(email));

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.artifacts
    ADD CONSTRAINT artifacts_uploaded_by_fkey FOREIGN KEY (uploaded_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.audit_log
    ADD CONSTRAINT audit_log_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.endpoints
    ADD CONSTRAINT endpoints_artifact_id_fkey FOREIGN KEY (artifact_id) REFERENCES public.artifacts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.endpoints
    ADD CONSTRAINT endpoints_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.job_events
    ADD CONSTRAINT job_events_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_enqueued_by_fkey FOREIGN KEY (enqueued_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_parent_job_id_fkey FOREIGN KEY (parent_job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT jobs_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.llm_calls
    ADD CONSTRAINT llm_calls_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.llm_calls
    ADD CONSTRAINT llm_calls_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.llm_models(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.llm_calls
    ADD CONSTRAINT llm_calls_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.llm_calls
    ADD CONSTRAINT llm_calls_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.llm_providers(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.llm_models
    ADD CONSTRAINT llm_models_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.llm_providers(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.llm_providers
    ADD CONSTRAINT llm_providers_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.project_members
    ADD CONSTRAINT project_members_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.projects
    ADD CONSTRAINT projects_owner_id_fkey FOREIGN KEY (owner_id) REFERENCES public.users(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.requirements
    ADD CONSTRAINT requirements_artifact_id_fkey FOREIGN KEY (artifact_id) REFERENCES public.artifacts(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.requirements
    ADD CONSTRAINT requirements_endpoint_id_fkey FOREIGN KEY (endpoint_id) REFERENCES public.endpoints(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.requirements
    ADD CONSTRAINT requirements_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.run_commands
    ADD CONSTRAINT run_commands_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.runs(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.run_results
    ADD CONSTRAINT run_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.runs(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.run_results
    ADD CONSTRAINT run_results_test_case_id_fkey FOREIGN KEY (test_case_id) REFERENCES public.test_cases(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.run_results
    ADD CONSTRAINT run_results_test_file_id_fkey FOREIGN KEY (test_file_id) REFERENCES public.test_files(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.runs
    ADD CONSTRAINT runs_job_id_fkey FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.runs
    ADD CONSTRAINT runs_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.runs
    ADD CONSTRAINT runs_triggered_by_fkey FOREIGN KEY (triggered_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.settings_audit
    ADD CONSTRAINT settings_audit_actor_id_fkey FOREIGN KEY (actor_id) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.settings
    ADD CONSTRAINT settings_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.test_cases
    ADD CONSTRAINT test_cases_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.test_cases
    ADD CONSTRAINT test_cases_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.test_cases
    ADD CONSTRAINT test_cases_requirement_id_fkey FOREIGN KEY (requirement_id) REFERENCES public.requirements(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.test_cases
    ADD CONSTRAINT test_cases_superseded_by_fkey FOREIGN KEY (superseded_by) REFERENCES public.test_cases(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.test_files
    ADD CONSTRAINT test_files_project_id_fkey FOREIGN KEY (project_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.tier_assignments
    ADD CONSTRAINT tier_assignments_fallback_model_id_fkey FOREIGN KEY (fallback_model_id) REFERENCES public.llm_models(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.tier_assignments
    ADD CONSTRAINT tier_assignments_model_id_fkey FOREIGN KEY (model_id) REFERENCES public.llm_models(id) ON DELETE RESTRICT;

ALTER TABLE ONLY public.tier_assignments
    ADD CONSTRAINT tier_assignments_scope_id_fkey FOREIGN KEY (scope_id) REFERENCES public.projects(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.tier_assignments
    ADD CONSTRAINT tier_assignments_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.user_invitations
    ADD CONSTRAINT user_invitations_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE ONLY public.user_invitations
    ADD CONSTRAINT user_invitations_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

