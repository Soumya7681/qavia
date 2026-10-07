-- +goose Up
-- Mock servers (BE-8.6, F-10.6).
--
-- One row per project, because a project has one mock: a second one on another port is
-- two URLs for a client app to keep straight and two things for somebody to forget to
-- stop. Restarting replaces the row's container rather than adding a row.
--
-- The row exists because a container is not a record. A mock outlives the request that
-- started it, so something has to remember that it is running, which container it is,
-- where it can be reached, and what it was configured with — otherwise a mock left
-- running is a container nobody can find and nobody can stop from the UI.

CREATE TABLE mock_servers (
    project_id uuid PRIMARY KEY REFERENCES projects (id) ON DELETE CASCADE,

    -- What is actually running. Empty once stopped, and the row is kept: the last
    -- configuration is what a restart reuses, and the history is what says whether the
    -- mock was up when a client test failed.
    container_id text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'stopped',

    -- Where a client app points. The host and port the platform published, stored
    -- rather than derived, because the port is Docker's choice and nothing else can
    -- reconstruct it.
    url       text NOT NULL DEFAULT '',
    host_port int  NOT NULL DEFAULT 0,

    -- The routes and the fault settings, as served. Kept so a restart is the same mock,
    -- and so "the app got a 500" can be checked against "the mock was injecting 30%".
    routes jsonb NOT NULL DEFAULT '[]'::jsonb,
    faults jsonb NOT NULL DEFAULT '{}'::jsonb,

    route_count int NOT NULL DEFAULT 0,

    image  text NOT NULL DEFAULT '',
    error  text NOT NULL DEFAULT '',

    started_by uuid REFERENCES users (id) ON DELETE SET NULL,
    started_at timestamptz,
    stopped_at timestamptz,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS mock_servers;
