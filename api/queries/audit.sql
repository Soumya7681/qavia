-- The audit log. Every privileged action, and nothing that holds a secret.

-- name: RecordAuditEntry :exec
INSERT INTO audit_log (actor_id, actor_email, action, subject, project_id, ip, detail)
VALUES (sqlc.narg('actor_id')::uuid, $1, $2, $3, sqlc.narg('project_id')::uuid, sqlc.narg('ip')::inet, $4);

-- ListAuditEntries is deliberately narrow. The filtered admin view is a dynamic
-- query built with squirrel inside the store, because four optional filters would
-- otherwise mean sixteen generated queries.
-- name: ListAuditEntries :many
SELECT id, actor_id, actor_email, action, subject, project_id, ip, detail, at
FROM audit_log
WHERE (sqlc.narg('cursor')::bigint IS NULL OR id < sqlc.narg('cursor')::bigint)
ORDER BY id DESC
LIMIT sqlc.arg('page_size');

-- name: CountAuditEntriesByAction :many
SELECT action, count(*) AS total
FROM audit_log
WHERE at > $1
GROUP BY action
ORDER BY total DESC;
