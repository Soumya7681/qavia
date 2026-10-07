-- Generated reports (BE-5.9).
--
-- The row is written before the file, so a report whose generation failed is visible
-- and retryable rather than being a gap nobody notices.

-- name: CreateReport :one
INSERT INTO reports (project_id, format, window_days, job_id, requested_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, project_id, format, status, window_days, storage_key, size_bytes,
          error, job_id, requested_by, created_at, finished_at;

-- name: GetReport :one
SELECT id, project_id, format, status, window_days, storage_key, size_bytes,
       error, job_id, requested_by, created_at, finished_at
FROM reports
WHERE id = $1;

-- name: ListReports :many
SELECT id, project_id, format, status, window_days, storage_key, size_bytes,
       error, job_id, requested_by, created_at, finished_at
FROM reports
WHERE project_id = $1
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: MarkReportRunning :execrows
UPDATE reports SET status = 'running' WHERE id = $1 AND status = 'queued';

-- name: FinishReport :one
UPDATE reports
SET status = 'ready', storage_key = $2, size_bytes = $3, finished_at = now()
WHERE id = $1
RETURNING id, project_id, format, status, window_days, storage_key, size_bytes,
          error, job_id, requested_by, created_at, finished_at;

-- name: FailReport :exec
UPDATE reports
SET status = 'failed', error = $2, finished_at = now()
WHERE id = $1;

-- The report's own reads (BE-5.9.2). They live here rather than being assembled from
-- four services, because one document's definition split across four packages is a
-- document whose sections drift apart.

-- ReportFailures is every failure in the window with its newest analysis, if it has
-- one, and the defect it was promoted to, if it was. A left join in both cases: a
-- report that hid unexplained failures would look better than the project is.
-- name: ReportFailures :many
SELECT DISTINCT ON (r.name)
       r.id, r.name, r.status, r.failure_message, r.created_at,
       -- coalesced rather than nullable, because "no analysis" and "an analysis with
       -- an empty root cause" are the same thing to a reader, and a nullable column
       -- here would put four pointer checks in the renderer for no gain.
       coalesce(a.reason, '') AS reason,
       coalesce(a.root_cause, '') AS root_cause,
       coalesce(a.suggested_fix, '') AS suggested_fix,
       coalesce(a.evidence, '[]'::jsonb) AS evidence,
       a.stability_score,
       d.id AS defect_id
FROM run_results r
JOIN runs n ON n.id = r.run_id
LEFT JOIN LATERAL (
    SELECT x.reason, x.root_cause, x.suggested_fix, x.evidence, x.stability_score
    FROM analyses x
    WHERE x.run_result_id = r.id
    ORDER BY x.created_at DESC
    LIMIT 1
) a ON true
LEFT JOIN defects d ON d.run_result_id = r.id
WHERE n.project_id = $1
  AND n.created_at >= $2
  AND r.status IN ('failed', 'flaky', 'errored')
-- One row per test, preferring the newest occurrence that has an explanation. The
-- alternative is showing "not analysed" for a failure the platform did explain, just
-- on an earlier run of the same test, which reads as a gap in the analysis rather
-- than what it is.
ORDER BY r.name, (a.reason IS NOT NULL) DESC, r.created_at DESC
LIMIT sqlc.arg('page_size');

-- ReportDefects lists the defects with how many times the same failure recurred.
-- Duplicates are counted rather than listed: three occurrences of one bug is one row
-- saying three.
-- name: ReportDefects :many
SELECT d.id, d.title, d.severity, d.status, d.created_at,
       (SELECT count(*) FROM defects k WHERE k.duplicate_of = d.id)::bigint AS occurrences
FROM defects d
WHERE d.project_id = $1 AND d.duplicate_of IS NULL
ORDER BY
    CASE d.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
    d.created_at DESC
LIMIT sqlc.arg('page_size');

-- name: CountOpenDefects :one
SELECT count(*)::bigint FROM defects
WHERE project_id = $1 AND status IN ('open', 'acknowledged', 'in_progress');
