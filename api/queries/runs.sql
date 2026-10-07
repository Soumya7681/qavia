-- Runs, results, and the command log.
--
-- The counts on the runs row are maintained when results are written, so the
-- dashboard reads one row rather than aggregating a result table that grows with
-- every execution.

-- name: CreateRun :one
INSERT INTO runs (project_id, job_id, target_url, trigger, framework, image, triggered_by,
                  kind, load_profile)
VALUES (
    $1, sqlc.narg('job_id')::uuid, $2, $3, $4, $5, sqlc.narg('triggered_by')::uuid,
    sqlc.arg('kind'), sqlc.arg('load_profile')
)
RETURNING id, project_id, job_id, target_url, trigger, status, framework, image,
          total, passed, failed, flaky, skipped, duration_ms, error, log_key,
          triggered_by, started_at, finished_at, created_at, quarantined, kind, load_profile;

-- name: GetRun :one
SELECT id, project_id, job_id, target_url, trigger, status, framework, image,
       total, passed, failed, flaky, skipped, duration_ms, error, log_key,
       triggered_by, started_at, finished_at, created_at, quarantined, kind, load_profile
FROM runs
WHERE id = $1;

-- name: ListRuns :many
SELECT id, project_id, job_id, target_url, trigger, status, framework, image,
       total, passed, failed, flaky, skipped, duration_ms, error, log_key,
       triggered_by, started_at, finished_at, created_at, quarantined, kind, load_profile
FROM runs
WHERE project_id = $1
  AND (sqlc.narg('kind')::run_kind IS NULL OR kind = sqlc.narg('kind')::run_kind)
  AND (sqlc.narg('status')::run_status IS NULL OR status = sqlc.narg('status')::run_status)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: MarkRunStarted :execrows
UPDATE runs
SET status = 'running', started_at = coalesce(started_at, now()), image = $2
WHERE id = $1 AND status = 'queued';

-- FinishRun writes the outcome and the counts in one statement, so a dashboard can
-- never read a run that is finished but not yet counted.
-- name: FinishRun :one
UPDATE runs
SET status = $2, total = $3, passed = $4, failed = $5, flaky = $6, skipped = $7,
    quarantined = sqlc.arg('quarantined'),
    duration_ms = $8, error = $9, log_key = $10, finished_at = now()
WHERE id = $1
RETURNING id, project_id, job_id, target_url, trigger, status, framework, image,
          total, passed, failed, flaky, skipped, duration_ms, error, log_key,
          triggered_by, started_at, finished_at, created_at, quarantined, kind, load_profile;

-- name: CancelRun :execrows
UPDATE runs
SET status = 'cancelled', finished_at = now()
WHERE id = $1 AND status IN ('queued', 'running');

-- CountActiveRuns backs the concurrency limit's "waiting for a slot" message: the
-- semaphore is what enforces it, and this is what explains it (BE-4.10).
-- name: CountActiveRuns :one
SELECT count(*) FROM runs WHERE status = 'running';

-- CountActiveRunsForProject backs the per-project cap. Global concurrency is a
-- semaphore in the worker; this is the check that stops one project queueing ten
-- runs and starving every other project behind them (BE-4.10).
-- name: CountActiveRunsForProject :one
SELECT count(*) FROM runs
WHERE project_id = $1 AND status IN ('queued', 'running');

-- MarkRunFailed is the sweeper's write: a run whose worker died is errored with a
-- stated reason rather than left running forever.
-- name: MarkRunFailed :execrows
UPDATE runs
SET status = 'errored', error = $2, finished_at = now()
WHERE id = $1 AND status IN ('queued', 'running');

-- DeleteRunResults makes the result write idempotent: a redelivered execute job
-- replaces its own results rather than doubling them (BE-4.9.5).
-- name: DeleteRunResults :exec
DELETE FROM run_results WHERE run_id = $1;

-- name: CreateRunResultsBulk :copyfrom
INSERT INTO run_results (
    run_id, test_case_id, test_file_id, name, status, duration_ms, attempt,
    failure_message, log_key, screenshot_key, video_key, trace_key, quarantined
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13);

-- name: ListRunResults :many
SELECT id, run_id, test_case_id, test_file_id, name, status, duration_ms, attempt,
       failure_message, log_key, screenshot_key, video_key, created_at, trace_key,
       quarantined
FROM run_results
WHERE run_id = $1
  AND (sqlc.narg('status')::run_result_status IS NULL
       OR status = sqlc.narg('status')::run_result_status)
ORDER BY name, attempt
LIMIT sqlc.arg('page_size') OFFSET sqlc.arg('row_offset');

-- name: CountRunResultsByStatus :many
SELECT status, count(*)::bigint AS total
FROM run_results
WHERE run_id = $1
GROUP BY status;

-- ResultHistoryForCase backs the stability score and the flake view: the same case
-- across runs, newest first.
-- name: ResultHistoryForCase :many
SELECT r.id, r.run_id, r.status, r.duration_ms, r.attempt, r.created_at
FROM run_results r
WHERE r.test_case_id = $1
ORDER BY r.created_at DESC
LIMIT sqlc.arg('page_size');

-- RunTrend is the dashboard's trend panel: one row per run, oldest first, so a
-- chart plots it without reversing.
-- name: RunTrend :many
SELECT id, created_at, status, total, passed, failed, flaky, skipped, quarantined, duration_ms
FROM runs
WHERE project_id = $1 AND created_at >= $2 AND status NOT IN ('queued', 'running')
ORDER BY created_at;

-- ProjectDashboard is the counts panel (F-12.1) as one query rather than five.
--
-- Every subquery qualifies its own table: without the alias the planner and the
-- generator both have to guess which project_id is meant, and one of them guesses
-- differently.
-- name: ProjectDashboard :one
SELECT
    (SELECT count(*) FROM requirements q WHERE q.project_id = $1)::bigint AS requirements,
    (SELECT count(*) FROM test_cases c
      WHERE c.project_id = $1 AND c.superseded_by IS NULL)::bigint AS test_cases,
    (SELECT count(*) FROM test_cases a
      WHERE a.project_id = $1 AND a.superseded_by IS NULL
        AND a.status = 'approved')::bigint AS approved_cases,
    (SELECT count(*) FROM test_files f WHERE f.project_id = $1)::bigint AS test_files,
    (SELECT count(*) FROM runs r WHERE r.project_id = $1)::bigint AS runs,
    -- The pass, fail, and flake counts are the latest finished run, not a lifetime
    -- sum. A sum grows forever and answers a question nobody asked: what a dashboard
    -- has to say is "where does this project stand now", and that is the last run
    -- that actually produced results (F-12.1).
    (SELECT coalesce(l.passed, 0) FROM runs l
      WHERE l.project_id = $1 AND l.status NOT IN ('queued', 'running')
      ORDER BY l.created_at DESC LIMIT 1)::bigint AS passed,
    (SELECT coalesce(x.failed, 0) FROM runs x
      WHERE x.project_id = $1 AND x.status NOT IN ('queued', 'running')
      ORDER BY x.created_at DESC LIMIT 1)::bigint AS failed,
    (SELECT coalesce(k.flaky, 0) FROM runs k
      WHERE k.project_id = $1 AND k.status NOT IN ('queued', 'running')
      ORDER BY k.created_at DESC LIMIT 1)::bigint AS flaky;

-- name: RecordRunCommand :exec
INSERT INTO run_commands (run_id, command, exit_code, duration_ms, output_excerpt)
VALUES ($1, $2, sqlc.narg('exit_code')::integer, $3, $4);

-- name: ListRunCommands :many
SELECT id, run_id, command, exit_code, duration_ms, output_excerpt, at
FROM run_commands
WHERE run_id = $1
ORDER BY id;

-- ListStaleRuns finds runs a crashed worker left behind, so the sweeper can mark
-- them errored rather than leaving them running forever (BE-4.2).
-- name: ListStaleRuns :many
SELECT id, project_id, started_at
FROM runs
WHERE status = 'running' AND started_at < $1
ORDER BY started_at
LIMIT sqlc.arg('page_size');

-- Live log lines (BE-4.11). Written in batches by the worker, read by the SSE
-- stream, deleted once the run's full log is safely in object storage.
-- name: AppendRunLogLines :copyfrom
INSERT INTO run_log_lines (run_id, line) VALUES ($1, $2);

-- name: ListRunLogLinesSince :many
SELECT id, run_id, line, at
FROM run_log_lines
WHERE run_id = $1 AND id > $2
ORDER BY id
LIMIT $3;

-- name: DeleteRunLogLines :exec
DELETE FROM run_log_lines WHERE run_id = $1;

-- Run artifact retention (BE-7.5.3). A video, a trace, and a screenshot are the
-- largest things this platform stores, and they are stored per failing attempt: an
-- installation that never expires them fills a disk with evidence about tests nobody
-- is still investigating.
--
-- The cutoff is the run's finish time rather than the result's creation, because a run
-- and its evidence expire together: half a run's artifacts is worse than none.
-- name: ListRunArtifactsOlderThan :many
SELECT rr.id, rr.screenshot_key, rr.video_key, rr.trace_key
FROM run_results rr
JOIN runs r ON r.id = rr.run_id
WHERE r.finished_at IS NOT NULL
  AND r.finished_at < $1
  AND (rr.screenshot_key <> '' OR rr.video_key <> '' OR rr.trace_key <> '')
ORDER BY r.finished_at
LIMIT sqlc.arg('page_size');

-- ClearRunResultArtifacts forgets the keys after the objects are gone. In that order:
-- a stored file with no key wastes space, while a key with no file is a download that
-- fails, and only one of those is visible to a user.
-- name: ClearRunResultArtifacts :exec
UPDATE run_results
SET screenshot_key = '', video_key = '', trace_key = ''
WHERE id = $1;

-- name: ListRunLogsOlderThan :many
SELECT id, log_key
FROM runs
WHERE finished_at IS NOT NULL
  AND finished_at < $1
  AND log_key <> ''
ORDER BY finished_at
LIMIT sqlc.arg('page_size');

-- name: ClearRunLog :exec
UPDATE runs SET log_key = '' WHERE id = $1;

-- Quarantine (BE-7.6). A test that flakes more than the threshold stops failing the
-- run and appears on a list with an owner and an age.

-- name: ActiveQuarantines :many
SELECT id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
       source, owner_id, released_at, released_by, release_note, last_flaked_at,
       created_at, updated_at
FROM quarantines
WHERE project_id = $1 AND released_at IS NULL
ORDER BY created_at;

-- name: ListQuarantines :many
SELECT id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
       source, owner_id, released_at, released_by, release_note, last_flaked_at,
       created_at, updated_at
FROM quarantines
WHERE project_id = $1
  AND (sqlc.narg('include_released')::bool IS TRUE OR released_at IS NULL)
ORDER BY released_at NULLS FIRST, created_at
LIMIT sqlc.arg('page_size');

-- name: GetQuarantine :one
SELECT id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
       source, owner_id, released_at, released_by, release_note, last_flaked_at,
       created_at, updated_at
FROM quarantines
WHERE id = $1;

-- QuarantineTest is idempotent on the live row: a second run that finds the same test
-- still flaking updates the arithmetic and the last-flaked time rather than creating a
-- second quarantine for the same test.
-- name: QuarantineTest :one
INSERT INTO quarantines (
    project_id, test_name, test_case_id, reason, flake_count, window_runs, source,
    owner_id, last_flaked_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
ON CONFLICT (project_id, test_name) WHERE released_at IS NULL
DO UPDATE SET reason = EXCLUDED.reason,
              flake_count = EXCLUDED.flake_count,
              window_runs = EXCLUDED.window_runs,
              last_flaked_at = now(),
              updated_at = now()
RETURNING id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
          source, owner_id, released_at, released_by, release_note, last_flaked_at,
          created_at, updated_at;

-- name: AssignQuarantineOwner :one
UPDATE quarantines
SET owner_id = $2, updated_at = now()
WHERE id = $1 AND released_at IS NULL
RETURNING id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
          source, owner_id, released_at, released_by, release_note, last_flaked_at,
          created_at, updated_at;

-- name: ReleaseQuarantine :one
UPDATE quarantines
SET released_at = now(), released_by = $2, release_note = $3, updated_at = now()
WHERE id = $1 AND released_at IS NULL
RETURNING id, project_id, test_name, test_case_id, reason, flake_count, window_runs,
          source, owner_id, released_at, released_by, release_note, last_flaked_at,
          created_at, updated_at;

-- FlakeCountsForProject is the arithmetic behind an automatic quarantine: how many of
-- the last N finished runs saw each test flake. Counted from the results rather than
-- from a stored score, because a score is a summary and this decision deserves the
-- underlying facts (BE-4.13).
-- A test flaked in a run when that run holds both a passing and a failing attempt of
-- it. Derived rather than read from a status column, because the results table stores
-- what each attempt did and `flaky` is a conclusion about a set of attempts: the run
-- row carries the tally, and the rows carry the facts it was computed from (BE-4.13.2).
-- name: FlakeCountsForProject :many
WITH recent AS (
    SELECT id
    FROM runs
    WHERE project_id = $1 AND finished_at IS NOT NULL
    ORDER BY finished_at DESC
    LIMIT sqlc.arg('window_runs')
),
per_run AS (
    SELECT rr.name,
           rr.run_id,
           bool_or(rr.status = 'passed') AS ever_passed,
           bool_or(rr.status IN ('failed', 'errored', 'flaky')) AS ever_failed
    FROM run_results rr
    JOIN recent ON recent.id = rr.run_id
    GROUP BY rr.name, rr.run_id
)
SELECT name,
       count(*) FILTER (WHERE ever_passed AND ever_failed)::bigint AS flaky_runs,
       count(*)::bigint AS seen_runs
FROM per_run
GROUP BY name;

-- Performance metrics (BE-9.1). One row per run: a run has one summary, and a
-- percentile is the summary rather than a sample of one.

-- name: StoreRunMetrics :exec
INSERT INTO run_metrics (
    run_id, requests, throughput, error_rate,
    latency_avg_ms, latency_p50_ms, latency_p90_ms, latency_p95_ms, latency_p99_ms, latency_max_ms,
    virtual_users, duration_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (run_id) DO UPDATE
SET requests = EXCLUDED.requests, throughput = EXCLUDED.throughput, error_rate = EXCLUDED.error_rate,
    latency_avg_ms = EXCLUDED.latency_avg_ms, latency_p50_ms = EXCLUDED.latency_p50_ms,
    latency_p90_ms = EXCLUDED.latency_p90_ms, latency_p95_ms = EXCLUDED.latency_p95_ms,
    latency_p99_ms = EXCLUDED.latency_p99_ms, latency_max_ms = EXCLUDED.latency_max_ms,
    virtual_users = EXCLUDED.virtual_users, duration_ms = EXCLUDED.duration_ms;

-- name: GetRunMetrics :one
SELECT run_id, requests, throughput, error_rate,
       latency_avg_ms, latency_p50_ms, latency_p90_ms, latency_p95_ms, latency_p99_ms, latency_max_ms,
       virtual_users, duration_ms, created_at
FROM run_metrics
WHERE run_id = $1;
