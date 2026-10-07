-- Code coverage (BE-6.6, BE-6.7).
--
-- Percentages are computed on read from the stored totals. A stored percentage is a
-- number that can disagree with its own numerator after one bad migration, and the
-- whole value of this table is that it agrees with the tool that produced it.

-- name: CreateCoverageRun :one
INSERT INTO coverage_runs (
    project_id, commit_sha, tool, command,
    lines_total, lines_covered, branches_total, branches_covered, error, job_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id, project_id, commit_sha, tool, command, lines_total, lines_covered,
          branches_total, branches_covered, error, job_id, created_at;

-- name: CreateCoverageFilesBulk :copyfrom
INSERT INTO coverage_files (
    coverage_run_id, path, lines_total, lines_covered, branches_total, branches_covered
) VALUES ($1, $2, $3, $4, $5, $6);

-- name: LatestCoverageRun :one
SELECT id, project_id, commit_sha, tool, command, lines_total, lines_covered,
       branches_total, branches_covered, error, job_id, created_at
FROM coverage_runs
WHERE project_id = $1 AND error = ''
ORDER BY created_at DESC
LIMIT 1;

-- ListCoverageFiles is the per-file detail, least covered first: the only order
-- anybody reads it in.
-- name: ListCoverageFiles :many
SELECT path, lines_total, lines_covered, branches_total, branches_covered
FROM coverage_files
WHERE coverage_run_id = $1
ORDER BY
    CASE WHEN lines_total = 0 THEN 1 ELSE 0 END,
    (lines_covered::numeric / NULLIF(lines_total, 0)),
    path
LIMIT sqlc.arg('page_size');

-- CoverageTrend backs the "did it rise" question that gives measured coverage its
-- point: a number with no history is a number nobody acts on.
-- name: CoverageTrend :many
SELECT id, commit_sha, lines_total, lines_covered, branches_total, branches_covered, created_at
FROM coverage_runs
WHERE project_id = $1 AND error = '' AND created_at >= $2
ORDER BY created_at;
