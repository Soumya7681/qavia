-- MCP servers and call audit (BE-10.1).

-- name: CreateMCPServer :one
INSERT INTO mcp_servers (
    name, transport, command, args, url, credentials, scope, scope_id,
    enabled_tools, auto_write, is_enabled, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, name, transport, command, args, url, credentials, scope, scope_id,
          enabled_tools, discovered_tools, auto_write, is_enabled, health_status,
          health_checked_at, health_detail, created_by, created_at, updated_at;

-- name: GetMCPServer :one
SELECT id, name, transport, command, args, url, credentials, scope, scope_id,
       enabled_tools, discovered_tools, auto_write, is_enabled, health_status,
       health_checked_at, health_detail, created_by, created_at, updated_at
FROM mcp_servers
WHERE id = $1;

-- ListMCPServersForScope is what an agent's tool loader reads: the enabled servers a
-- project may use, which is the global ones plus that project's own. The per-project
-- boundary is here — another project's servers are never returned.
-- name: ListMCPServersForScope :many
SELECT id, name, transport, command, args, url, credentials, scope, scope_id,
       enabled_tools, discovered_tools, auto_write, is_enabled, health_status,
       health_checked_at, health_detail, created_by, created_at, updated_at
FROM mcp_servers
WHERE is_enabled
  AND (scope = 'global' OR (scope = 'project' AND scope_id = $1))
ORDER BY name;

-- name: ListAllMCPServers :many
SELECT id, name, transport, command, args, url, credentials, scope, scope_id,
       enabled_tools, discovered_tools, auto_write, is_enabled, health_status,
       health_checked_at, health_detail, created_by, created_at, updated_at
FROM mcp_servers
ORDER BY scope, name;

-- name: UpdateMCPServer :one
UPDATE mcp_servers
SET name = $2, transport = $3, command = $4, args = $5, url = $6,
    scope = $7, scope_id = $8, enabled_tools = $9, auto_write = $10, is_enabled = $11,
    updated_at = now()
WHERE id = $1
RETURNING id, name, transport, command, args, url, credentials, scope, scope_id,
          enabled_tools, discovered_tools, auto_write, is_enabled, health_status,
          health_checked_at, health_detail, created_by, created_at, updated_at;

-- name: SetMCPCredentials :exec
UPDATE mcp_servers SET credentials = $2, updated_at = now() WHERE id = $1;

-- name: RecordMCPHealth :exec
UPDATE mcp_servers
SET health_status = $2, health_detail = $3, discovered_tools = $4, health_checked_at = now(),
    updated_at = now()
WHERE id = $1;

-- name: DeleteMCPServer :execrows
DELETE FROM mcp_servers WHERE id = $1;

-- name: RecordMCPCall :exec
INSERT INTO mcp_calls (server_id, server_name, tool, arguments, status, error, agent, project_id, job_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListMCPCalls :many
SELECT id, server_id, server_name, tool, arguments, status, error, agent, project_id, job_id, at
FROM mcp_calls
WHERE (sqlc.narg('server_id')::uuid IS NULL OR server_id = sqlc.narg('server_id')::uuid)
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id')::uuid)
ORDER BY at DESC
LIMIT sqlc.arg('page_size');
