-- Mock servers (BE-8.6). One row per project: a project has one mock, and restarting
-- replaces its container rather than adding a row.

-- name: UpsertMockServer :one
INSERT INTO mock_servers (
    project_id, container_id, status, url, host_port, routes, faults, route_count,
    image, error, started_by, started_at, stopped_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (project_id) DO UPDATE
SET container_id = EXCLUDED.container_id,
    status       = EXCLUDED.status,
    url          = EXCLUDED.url,
    host_port    = EXCLUDED.host_port,
    routes       = EXCLUDED.routes,
    faults       = EXCLUDED.faults,
    route_count  = EXCLUDED.route_count,
    image        = EXCLUDED.image,
    error        = EXCLUDED.error,
    started_by   = EXCLUDED.started_by,
    started_at   = EXCLUDED.started_at,
    stopped_at   = EXCLUDED.stopped_at,
    updated_at   = now()
RETURNING project_id, container_id, status, url, host_port, routes, faults, route_count,
          image, error, started_by, started_at, stopped_at, created_at, updated_at;

-- name: GetMockServer :one
SELECT project_id, container_id, status, url, host_port, routes, faults, route_count,
       image, error, started_by, started_at, stopped_at, created_at, updated_at
FROM mock_servers
WHERE project_id = $1;

-- MarkMockStopped records that the container is gone, keeping the routes so a restart
-- serves the same mock.
-- name: MarkMockStopped :one
UPDATE mock_servers
SET status = 'stopped', container_id = '', url = '', host_port = 0,
    stopped_at = now(), error = $2, updated_at = now()
WHERE project_id = $1
RETURNING project_id, container_id, status, url, host_port, routes, faults, route_count,
          image, error, started_by, started_at, stopped_at, created_at, updated_at;

-- ListRunningMocks is what a sweeper reconciles against the host: a row that says
-- running with no container behind it is a row a UI is lying with.
-- name: ListRunningMocks :many
SELECT project_id, container_id, status, url, host_port, routes, faults, route_count,
       image, error, started_by, started_at, stopped_at, created_at, updated_at
FROM mock_servers
WHERE status = 'running'
ORDER BY started_at;
