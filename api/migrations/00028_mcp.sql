-- +goose Up
-- MCP servers and the audit of their calls (BE-10.1, ai-architecture.md 5.4).
--
-- An MCP server is a channel through which model output reaches an external system that
-- can change state. Everything about this schema treats it as one: tools are allowlisted
-- rather than enabled, credentials are encrypted, servers are scoped so one client's Jira
-- is unreachable from another client's project, and every call is logged with its
-- arguments so a write nobody expected can be traced to the job that made it.
--
-- MCP is entirely optional. This table staying empty forever is a supported
-- configuration: every capability it fronts has a built-in that needs no server.

CREATE TYPE mcp_transport AS ENUM ('stdio', 'http');
CREATE TYPE mcp_scope AS ENUM ('global', 'project');
CREATE TYPE mcp_health AS ENUM ('unknown', 'healthy', 'unreachable');

CREATE TABLE mcp_servers (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,

    transport mcp_transport NOT NULL,

    -- stdio: a command and its arguments. http: a URL. Exactly one shape is used, and
    -- the service checks the right one is present for the transport.
    command text NOT NULL DEFAULT '',
    args    text[] NOT NULL DEFAULT '{}',
    url     text NOT NULL DEFAULT '',

    -- Encrypted at rest, like every other secret in the platform. A token or OAuth
    -- material; never returned once stored.
    credentials jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- Per-project scoping is a security boundary, not a convenience: Client A's Jira
    -- server must not be reachable from Client B's project (ai-architecture.md 5.4).
    scope    mcp_scope NOT NULL DEFAULT 'global',
    scope_id uuid,

    -- The allowlist. Deny-all by default: an admin tests the connection, sees the tools,
    -- and opts into specific ones. An empty list means nothing is allowed, NOT
    -- everything — the opposite of the schema doc's shorthand, chosen deliberately
    -- because "empty means all" is how a server's full tool set gets enabled by
    -- accident (ai-architecture.md 5.5).
    enabled_tools text[] NOT NULL DEFAULT '{}',

    -- The tools the last connection test discovered, so the allowlist screen can show
    -- what is available without re-probing on every render.
    discovered_tools jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- Whether writes are permitted for this server without a per-call human step. Off by
    -- default: an agent reasoning over a failed test should not silently file twenty
    -- tickets (ai-architecture.md 5.5, BE-10.7).
    auto_write bool NOT NULL DEFAULT false,

    is_enabled    bool NOT NULL DEFAULT true,
    health_status mcp_health NOT NULL DEFAULT 'unknown',
    health_checked_at timestamptz,
    health_detail text NOT NULL DEFAULT '',

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- A project-scoped server names its project; a global one does not. Enforced so a
    -- row cannot claim project scope with no project.
    CONSTRAINT mcp_scope_id_present CHECK (
        (scope = 'project' AND scope_id IS NOT NULL) OR
        (scope = 'global' AND scope_id IS NULL)
    )
);

CREATE INDEX mcp_servers_scope_idx ON mcp_servers (scope, scope_id) WHERE is_enabled;

-- Every MCP tool call, for the audit a security review asks for after the fact: what
-- was called, with what, by which agent and job, and whether it succeeded. Arguments
-- are stored with credential-shaped values redacted (ai-architecture.md 5.5).
CREATE TABLE mcp_calls (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    server_id uuid REFERENCES mcp_servers (id) ON DELETE SET NULL,

    server_name text NOT NULL DEFAULT '',
    tool        text NOT NULL,

    -- The arguments, redacted. jsonb so the audit screen renders them as fields.
    arguments jsonb NOT NULL DEFAULT '{}'::jsonb,

    -- ok, error, or denied — a call refused by the allowlist before it left the platform.
    status text NOT NULL,
    error  text NOT NULL DEFAULT '',

    agent      text NOT NULL DEFAULT '',
    project_id uuid REFERENCES projects (id) ON DELETE SET NULL,
    job_id     uuid REFERENCES jobs (id) ON DELETE SET NULL,

    at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX mcp_calls_server_idx ON mcp_calls (server_id, at DESC);
CREATE INDEX mcp_calls_project_idx ON mcp_calls (project_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS mcp_calls;
DROP TABLE IF EXISTS mcp_servers;
DROP TYPE IF EXISTS mcp_health;
DROP TYPE IF EXISTS mcp_scope;
DROP TYPE IF EXISTS mcp_transport;
