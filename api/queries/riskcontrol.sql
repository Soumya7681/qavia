-- Confirmed hosts for the two dangerous test kinds (BE-9.5).

-- name: ConfirmHost :one
INSERT INTO risk_confirmations (project_id, kind, host, confirmed_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id, kind, host) DO UPDATE
SET confirmed_by = EXCLUDED.confirmed_by, confirmed_at = now()
RETURNING id, project_id, kind, host, confirmed_by, confirmed_at;

-- name: HostConfirmed :one
SELECT EXISTS (
    SELECT 1 FROM risk_confirmations
    WHERE project_id = $1 AND kind = $2 AND host = $3
) AS confirmed;

-- name: ListConfirmedHosts :many
SELECT id, project_id, kind, host, confirmed_by, confirmed_at
FROM risk_confirmations
WHERE project_id = $1 AND kind = $2
ORDER BY confirmed_at DESC;
