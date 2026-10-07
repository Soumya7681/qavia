-- Security findings (BE-9.4). One per probe that succeeded, keyed to the run_result the
-- probe produced so the promote-to-defect path works unchanged.

-- name: CreateSecurityFinding :one
INSERT INTO security_findings (
    run_result_id, run_id, payload_id, category, endpoint, parameter,
    severity, evidence, reproduction
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, run_result_id, run_id, payload_id, category, endpoint, parameter,
          severity, evidence, reproduction, created_at;

-- name: ListSecurityFindings :many
SELECT id, run_result_id, run_id, payload_id, category, endpoint, parameter,
       severity, evidence, reproduction, created_at
FROM security_findings
WHERE run_id = $1
ORDER BY
    CASE severity
        WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2
        WHEN 'low' THEN 3 ELSE 4
    END,
    endpoint;

-- name: GetSecurityFinding :one
SELECT id, run_result_id, run_id, payload_id, category, endpoint, parameter,
       severity, evidence, reproduction, created_at
FROM security_findings
WHERE id = $1;
