-- The AI provider layer: providers, models, tier assignments, and the call log.
--
-- Resolution for a tier is project assignment, then global assignment, then an
-- error. Never a silent code default: a zero value that quietly means "use
-- whatever" is the failure this whole design prevents (ai-architecture.md 3.2).

-- name: CreateLLMProvider :one
INSERT INTO llm_providers (name, kind, config, credentials, data_residency, is_enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('created_by')::uuid)
RETURNING id, name, kind, config, credentials, data_residency, is_enabled, is_default,
          health_status, health_checked_at, health_detail, created_by, created_at, updated_at;

-- name: GetLLMProvider :one
SELECT id, name, kind, config, credentials, data_residency, is_enabled, is_default,
       health_status, health_checked_at, health_detail, created_by, created_at, updated_at
FROM llm_providers
WHERE id = $1;

-- name: ListLLMProviders :many
SELECT id, name, kind, config, credentials, data_residency, is_enabled, is_default,
       health_status, health_checked_at, health_detail, created_by, created_at, updated_at
FROM llm_providers
ORDER BY is_default DESC, lower(name);

-- CountEnabledProviders backs the setup status and the enqueue-time check: an AI
-- job with no configured provider is refused when the user presses submit, not
-- inside a worker twenty minutes later.
-- name: CountEnabledProviders :one
SELECT count(*) FROM llm_providers WHERE is_enabled;

-- name: UpdateLLMProvider :one
UPDATE llm_providers
SET name = $2, config = $3, data_residency = $4, is_enabled = $5, updated_at = now()
WHERE id = $1
RETURNING id, name, kind, config, credentials, data_residency, is_enabled, is_default,
          health_status, health_checked_at, health_detail, created_by, created_at, updated_at;

-- SetLLMProviderCredentials is separate from the rest of the update, because a
-- form submitted without the secret field must not blank the stored one: a secret
-- is replaced deliberately or left alone (F-1.8).
-- name: SetLLMProviderCredentials :exec
UPDATE llm_providers
SET credentials = $2, updated_at = now()
WHERE id = $1;

-- name: SetLLMProviderHealth :exec
UPDATE llm_providers
SET health_status = $2, health_detail = $3, health_checked_at = now(), updated_at = now()
WHERE id = $1;

-- ClearDefaultProvider and SetDefaultProvider run in one transaction. The partial
-- unique index refuses two defaults, so the clear has to happen first.
-- name: ClearDefaultProvider :exec
UPDATE llm_providers SET is_default = false, updated_at = now() WHERE is_default;

-- name: SetDefaultProvider :exec
UPDATE llm_providers SET is_default = true, updated_at = now() WHERE id = $1;

-- name: DeleteLLMProvider :execrows
DELETE FROM llm_providers WHERE id = $1;

-- CountAssignmentsUsingProvider is what makes deleting a referenced provider a
-- clear refusal rather than a cascade that silently unassigns a tier.
-- name: CountAssignmentsUsingProvider :one
SELECT count(*)
FROM tier_assignments a
JOIN llm_models m ON m.id = a.model_id OR m.id = a.fallback_model_id
WHERE m.provider_id = $1;

-- name: CreateLLMModel :one
INSERT INTO llm_models (
    provider_id, model_id, display_name, tiers, capabilities,
    price_input, price_output, price_cache_read, price_cache_write,
    max_input_tokens, max_output_tokens, is_enabled
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, provider_id, model_id, display_name, tiers, capabilities,
          price_input, price_output, price_cache_read, price_cache_write,
          max_input_tokens, max_output_tokens, is_enabled, created_at, updated_at;

-- name: GetLLMModel :one
SELECT id, provider_id, model_id, display_name, tiers, capabilities,
       price_input, price_output, price_cache_read, price_cache_write,
       max_input_tokens, max_output_tokens, is_enabled, created_at, updated_at
FROM llm_models
WHERE id = $1;

-- name: ListLLMModels :many
SELECT id, provider_id, model_id, display_name, tiers, capabilities,
       price_input, price_output, price_cache_read, price_cache_write,
       max_input_tokens, max_output_tokens, is_enabled, created_at, updated_at
FROM llm_models
WHERE (sqlc.narg('provider_id')::uuid IS NULL OR provider_id = sqlc.narg('provider_id')::uuid)
ORDER BY model_id;

-- name: UpdateLLMModel :one
UPDATE llm_models
SET display_name = $2, tiers = $3, capabilities = $4,
    price_input = $5, price_output = $6, price_cache_read = $7, price_cache_write = $8,
    max_input_tokens = $9, max_output_tokens = $10, is_enabled = $11, updated_at = now()
WHERE id = $1
RETURNING id, provider_id, model_id, display_name, tiers, capabilities,
          price_input, price_output, price_cache_read, price_cache_write,
          max_input_tokens, max_output_tokens, is_enabled, created_at, updated_at;

-- SetLLMModelCapabilities is written by the probe. Detected capability overwrites
-- declared capability, because a declaration is a claim and a probe is a
-- measurement (F-16.5).
-- name: SetLLMModelCapabilities :exec
UPDATE llm_models
SET capabilities = $2, updated_at = now()
WHERE id = $1;

-- name: DeleteLLMModel :execrows
DELETE FROM llm_models WHERE id = $1;

-- name: CountAssignmentsUsingModel :one
SELECT count(*) FROM tier_assignments WHERE model_id = $1 OR fallback_model_id = $1;

-- UpsertTierAssignment writes the global row. Global and project need separate
-- statements because their uniqueness is enforced by two partial indexes: a NULL
-- scope_id is not equal to itself, so one ON CONFLICT target cannot cover both.
-- name: UpsertGlobalTierAssignment :one
INSERT INTO tier_assignments (scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by)
VALUES ('global', NULL, $1, $2, sqlc.narg('fallback_model_id')::uuid, sqlc.narg('effort')::text,
        sqlc.narg('updated_by')::uuid)
ON CONFLICT (tier) WHERE scope = 'global' DO UPDATE
SET model_id = EXCLUDED.model_id,
    fallback_model_id = EXCLUDED.fallback_model_id,
    effort = EXCLUDED.effort,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING id, scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by, created_at, updated_at;

-- name: UpsertProjectTierAssignment :one
INSERT INTO tier_assignments (scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by)
VALUES ('project', $1, $2, $3, sqlc.narg('fallback_model_id')::uuid, sqlc.narg('effort')::text,
        sqlc.narg('updated_by')::uuid)
ON CONFLICT (scope_id, tier) WHERE scope = 'project' DO UPDATE
SET model_id = EXCLUDED.model_id,
    fallback_model_id = EXCLUDED.fallback_model_id,
    effort = EXCLUDED.effort,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
RETURNING id, scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by, created_at, updated_at;

-- ResolveTier is the whole resolution rule in one query: project first, then
-- global, ordered so the caller takes the first row and never sorts it itself.
-- name: ResolveTier :many
SELECT id, scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by, created_at, updated_at
FROM tier_assignments
WHERE tier = $1
  AND (scope = 'global' OR (scope = 'project' AND scope_id = sqlc.narg('project_id')::uuid))
ORDER BY scope DESC;

-- name: ListTierAssignments :many
SELECT id, scope, scope_id, tier, model_id, fallback_model_id, effort, updated_by, created_at, updated_at
FROM tier_assignments
WHERE (sqlc.narg('project_id')::uuid IS NULL AND scope = 'global')
   OR (sqlc.narg('project_id')::uuid IS NOT NULL AND scope_id = sqlc.narg('project_id')::uuid)
ORDER BY tier;

-- name: DeleteTierAssignment :execrows
DELETE FROM tier_assignments WHERE id = $1;

-- RecordLLMCall is written by Go and only by Go. Python returns usage and
-- persists nothing (backend-standards.md 10).
-- name: RecordLLMCall :one
INSERT INTO llm_calls (
    provider_id, model_id, provider_kind, model_name, tier, agent,
    project_id, job_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
    cost_usd, latency_ms, fallback_used
)
VALUES (
    sqlc.narg('provider_id')::uuid, sqlc.narg('model_id')::uuid, $1, $2, $3, $4,
    sqlc.narg('project_id')::uuid, sqlc.narg('job_id')::uuid, $5, $6, $7, $8,
    $9, $10, $11
)
RETURNING id, provider_id, model_id, provider_kind, model_name, tier, agent,
          project_id, job_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
          cost_usd, latency_ms, fallback_used, at;

-- SpendSince is the ceiling check, evaluated before a call rather than after
-- (F-16.10). A mixed setup may have one cheap local provider and one metered one,
-- so it is available per provider and in total.
-- name: SpendSince :one
SELECT coalesce(sum(cost_usd), 0)::numeric AS total
FROM llm_calls
WHERE at >= $1
  AND (sqlc.narg('provider_id')::uuid IS NULL OR provider_id = sqlc.narg('provider_id')::uuid)
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid);

-- SpendByProvider, SpendByProject, and SpendByAgent back the dashboard. Genuine
-- SQL aggregates, generated like any other query, so the numbers reconcile exactly
-- against a direct SUM over llm_calls (BE-1.14).
-- name: SpendByProvider :many
SELECT coalesce(provider_id, '00000000-0000-0000-0000-000000000000'::uuid) AS provider_id,
       max(provider_kind)::text AS provider_kind,
       count(*)::bigint AS calls,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cost_usd)::numeric AS cost_usd
FROM llm_calls
WHERE at >= $1 AND at < $2
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
GROUP BY 1
ORDER BY cost_usd DESC;

-- name: SpendByProject :many
SELECT coalesce(project_id, '00000000-0000-0000-0000-000000000000'::uuid) AS project_id,
       count(*)::bigint AS calls,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cost_usd)::numeric AS cost_usd
FROM llm_calls
WHERE at >= $1 AND at < $2
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
GROUP BY 1
ORDER BY cost_usd DESC;

-- name: SpendByAgent :many
SELECT agent,
       count(*)::bigint AS calls,
       sum(input_tokens)::bigint AS input_tokens,
       sum(output_tokens)::bigint AS output_tokens,
       sum(cache_read_tokens)::bigint AS cache_read_tokens,
       sum(cost_usd)::numeric AS cost_usd
FROM llm_calls
WHERE at >= $1 AND at < $2
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
GROUP BY agent
ORDER BY cost_usd DESC;

-- name: SpendTotal :one
SELECT count(*)::bigint AS calls,
       coalesce(sum(input_tokens), 0)::bigint AS input_tokens,
       coalesce(sum(output_tokens), 0)::bigint AS output_tokens,
       coalesce(sum(cache_read_tokens), 0)::bigint AS cache_read_tokens,
       coalesce(sum(cache_write_tokens), 0)::bigint AS cache_write_tokens,
       coalesce(sum(cost_usd), 0)::numeric AS cost_usd
FROM llm_calls
WHERE at >= $1 AND at < $2
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid);

-- name: ListLLMCalls :many
SELECT id, provider_id, model_id, provider_kind, model_name, tier, agent,
       project_id, job_id, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
       cost_usd, latency_ms, fallback_used, at
FROM llm_calls
WHERE at >= $1 AND at < $2
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
  AND (sqlc.narg('cursor')::bigint IS NULL OR id < sqlc.narg('cursor')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_size');
