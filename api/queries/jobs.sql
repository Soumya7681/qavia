-- Jobs and job events.
--
-- The jobs row is the durable record of what ran. Redis holds only in-flight
-- coordination.

-- EnqueueJob is idempotent by unique index on (type, idempotency_key). A retried
-- enqueue returns the existing row instead of creating a second one, which is what
-- makes NFR-4 structural rather than disciplined.
-- name: EnqueueJob :one
INSERT INTO jobs (project_id, type, payload, idempotency_key, correlation_id, parent_job_id, enqueued_by, max_attempts)
VALUES (
    sqlc.narg('project_id')::uuid, $1, $2, $3, $4,
    sqlc.narg('parent_job_id')::uuid, sqlc.narg('enqueued_by')::uuid, $5
)
ON CONFLICT (type, idempotency_key) DO UPDATE
SET type = jobs.type
RETURNING id, project_id, type, status, progress, attempts, max_attempts, error,
          parent_job_id, payload, idempotency_key, correlation_id, enqueued_by,
          queued_at, started_at, finished_at,
          (jobs.queued_at < now() - interval '1 microsecond') AS existed;

-- name: GetJob :one
SELECT id, project_id, type, status, progress, attempts, max_attempts, error,
       parent_job_id, payload, idempotency_key, correlation_id, enqueued_by,
       queued_at, started_at, finished_at
FROM jobs
WHERE id = $1;

-- name: ListJobsForProject :many
SELECT id, project_id, type, status, progress, attempts, max_attempts, error,
       parent_job_id, payload, idempotency_key, correlation_id, enqueued_by,
       queued_at, started_at, finished_at
FROM jobs
WHERE project_id = $1
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR queued_at < sqlc.narg('cursor')::timestamptz)
ORDER BY queued_at DESC
LIMIT sqlc.arg('page_size');

-- name: ListChildJobs :many
SELECT id, project_id, type, status, progress, attempts, max_attempts, error,
       parent_job_id, payload, idempotency_key, correlation_id, enqueued_by,
       queued_at, started_at, finished_at
FROM jobs
WHERE parent_job_id = $1
ORDER BY queued_at;

-- name: MarkJobRunning :execrows
UPDATE jobs
SET status = 'running',
    attempts = attempts + 1,
    started_at = coalesce(started_at, now())
WHERE id = $1 AND status IN ('queued', 'running');

-- MarkChainRunning moves a parent row out of queued when its first stage starts.
--
-- Deliberately not MarkJobRunning: that counts an attempt, and a parent is not
-- attempted. A three-stage chain would otherwise report three attempts on a
-- submission that never failed once.
-- name: MarkChainRunning :execrows
UPDATE jobs
SET status = 'running',
    started_at = coalesce(started_at, now())
WHERE id = $1 AND status = 'queued';

-- name: MarkJobSucceeded :execrows
UPDATE jobs
SET status = 'succeeded', progress = 100, finished_at = now(), error = NULL
WHERE id = $1 AND status = 'running';

-- name: MarkJobFailed :execrows
UPDATE jobs
SET status = 'failed', finished_at = now(), error = $2
WHERE id = $1 AND status IN ('queued', 'running');

-- name: MarkJobQueuedForRetry :execrows
UPDATE jobs
SET status = 'queued', error = $2
WHERE id = $1 AND status = 'running';

-- name: CancelJob :execrows
UPDATE jobs
SET status = 'cancelled', finished_at = now()
WHERE id = $1 AND status IN ('queued', 'running');

-- name: SetJobProgress :execrows
UPDATE jobs
SET progress = $2
WHERE id = $1 AND status = 'running';

-- name: AppendJobEvent :one
INSERT INTO job_events (job_id, level, message)
VALUES ($1, $2, $3)
RETURNING id, job_id, level, message, at;

-- ListJobEventsSince backs the SSE stream. The bigserial id doubles as the
-- Last-Event-ID cursor, so a reconnect resumes without gaps or duplicates.
-- name: ListJobEventsSince :many
SELECT id, job_id, level, message, at
FROM job_events
WHERE job_id = $1 AND id > $2
ORDER BY id
LIMIT sqlc.arg('page_size');

-- ListChainEventsSince is the same, widened to a chain.
--
-- Handlers write their log lines against the stage that is running, so a viewer
-- watching the submission would otherwise see an empty log while three stages
-- narrated themselves. Asking for a stage still returns only that stage's lines,
-- because a stage has no children.
-- name: ListChainEventsSince :many
SELECT e.id, e.job_id, e.level, e.message, e.at
FROM job_events e
JOIN jobs j ON j.id = e.job_id
WHERE (j.id = $1 OR j.parent_job_id = $1) AND e.id > $2
ORDER BY e.id
LIMIT sqlc.arg('page_size');

-- name: CountJobsByStatus :many
SELECT status, count(*) AS total
FROM jobs
WHERE project_id = $1
GROUP BY status;
