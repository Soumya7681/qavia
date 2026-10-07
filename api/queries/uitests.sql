-- Discovered UI flow graphs (BE-7.2). One row per discovery: a graph describes an
-- application as it behaved at a moment, and last week's graph is history rather than
-- something to overwrite.

-- name: CreateUIFlow :one
INSERT INTO ui_flows (
    project_id, target, auth_mode, document, page_count, flow_count,
    steps, cut_short, artifacts, model_name, job_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING id, project_id, target, auth_mode, document, page_count, flow_count,
          steps, cut_short, artifacts, reviewed_at, reviewed_by, model_name, job_id, created_at;

-- name: LatestUIFlow :one
SELECT id, project_id, target, auth_mode, document, page_count, flow_count,
       steps, cut_short, artifacts, reviewed_at, reviewed_by, model_name, job_id, created_at
FROM ui_flows
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: GetUIFlow :one
SELECT id, project_id, target, auth_mode, document, page_count, flow_count,
       steps, cut_short, artifacts, reviewed_at, reviewed_by, model_name, job_id, created_at
FROM ui_flows
WHERE id = $1;

-- ListUIFlows is the history. The document is left out: a list screen shows the
-- counts and the date, and shipping forty graphs to render ten rows is a page nobody
-- waits for.
-- name: ListUIFlows :many
SELECT id, project_id, target, auth_mode, page_count, flow_count,
       steps, cut_short, reviewed_at, reviewed_by, model_name, created_at
FROM ui_flows
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- MarkUIFlowReviewed records that a person has read the graph, so generating from an
-- unreviewed one is a visible choice rather than an invisible default (BE-7.2.3).
-- name: MarkUIFlowReviewed :one
UPDATE ui_flows
SET reviewed_at = now(), reviewed_by = $2
WHERE id = $1
RETURNING id, project_id, target, auth_mode, document, page_count, flow_count,
          steps, cut_short, artifacts, reviewed_at, reviewed_by, model_name, job_id, created_at;
