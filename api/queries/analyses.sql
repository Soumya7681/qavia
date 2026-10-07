-- Failure analyses and the feedback on them (BE-5.1, BE-5.8).
--
-- The evidence column is written whole by Go, after every reference in it has been
-- checked against the artifact it points at: an analysis citing line 4,000 of a
-- 300-line log is rejected before it reaches this table (BE-5.3).

-- name: CreateAnalysis :one
INSERT INTO analyses (
    run_result_id, reason, root_cause, suggested_fix, evidence,
    related_commit, stability_score, prompt_version, model_name
) VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('stability_score')::numeric, $7, $8)
RETURNING id, run_result_id, reason, root_cause, suggested_fix, evidence,
          related_commit, stability_score, prompt_version, model_name, created_at;

-- name: GetAnalysis :one
SELECT id, run_result_id, reason, root_cause, suggested_fix, evidence,
       related_commit, stability_score, prompt_version, model_name, created_at
FROM analyses
WHERE id = $1;

-- LatestAnalysisForResult is what the UI shows: a re-analysis after a prompt change
-- is a new row, and the newest one wins.
-- name: LatestAnalysisForResult :one
SELECT id, run_result_id, reason, root_cause, suggested_fix, evidence,
       related_commit, stability_score, prompt_version, model_name, created_at
FROM analyses
WHERE run_result_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListAnalysesForRun :many
SELECT a.id, a.run_result_id, a.reason, a.root_cause, a.suggested_fix, a.evidence,
       a.related_commit, a.stability_score, a.prompt_version, a.model_name, a.created_at
FROM analyses a
JOIN run_results r ON r.id = a.run_result_id
WHERE r.run_id = $1
ORDER BY a.created_at DESC;

-- SetStabilityScore is written after the analysis, because the score comes from
-- re-run history and commit overlap rather than from the agent (BE-5.4).
-- name: SetStabilityScore :exec
UPDATE analyses
SET stability_score = sqlc.narg('stability_score')::numeric
WHERE id = $1;

-- name: UpsertAnalysisFeedback :exec
INSERT INTO analysis_feedback (analysis_id, user_id, helpful, note, prompt_version)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (analysis_id, user_id) DO UPDATE
SET helpful = EXCLUDED.helpful,
    note = EXCLUDED.note,
    prompt_version = EXCLUDED.prompt_version,
    created_at = now();

-- name: DeleteAnalysisFeedback :exec
DELETE FROM analysis_feedback WHERE analysis_id = $1 AND user_id = $2;

-- name: GetAnalysisFeedback :one
SELECT analysis_id, user_id, helpful, note, prompt_version, created_at
FROM analysis_feedback
WHERE analysis_id = $1 AND user_id = $2;

-- FeedbackByPromptVersion is the point of storing the version: prompt iteration with
-- numbers rather than impressions (BE-5.8.2).
-- name: FeedbackByPromptVersion :many
SELECT prompt_version,
       count(*) FILTER (WHERE helpful)::bigint AS helpful,
       count(*) FILTER (WHERE NOT helpful)::bigint AS unhelpful
FROM analysis_feedback
GROUP BY prompt_version
ORDER BY prompt_version;

-- ResultAttempts backs the stability score's first permitted signal: how a test
-- behaved across the attempts of one run (BE-5.4.1).
-- name: ResultAttempts :many
SELECT status, attempt
FROM run_results
WHERE run_id = $1 AND name = $2
ORDER BY attempt;

-- ResultHistoryByName is the second stability input: how this test has behaved over
-- recent runs of the same project. By name rather than by test case, because a
-- result the platform could not map to exactly one case still has a history.
-- name: ResultHistoryByName :many
SELECT r.status, r.attempt, r.created_at
FROM run_results r
JOIN runs n ON n.id = r.run_id
WHERE n.project_id = $1 AND r.name = $2
ORDER BY r.created_at DESC
LIMIT $3;

-- FailingResultsForRun is what the analyse chain iterates: every failure worth
-- explaining, flaky ones included, because "why is this flaky" is the question a
-- reviewer actually has.
-- name: FailingResultsForRun :many
SELECT id, run_id, test_case_id, test_file_id, name, status, duration_ms, attempt,
       failure_message, log_key, screenshot_key, video_key, created_at, trace_key,
       quarantined
FROM run_results
WHERE run_id = $1 AND status IN ('failed', 'flaky', 'errored')
ORDER BY status, name, attempt;

-- name: GetRunResult :one
SELECT id, run_id, test_case_id, test_file_id, name, status, duration_ms, attempt,
       failure_message, log_key, screenshot_key, video_key, created_at, trace_key,
       quarantined
FROM run_results
WHERE id = $1;
