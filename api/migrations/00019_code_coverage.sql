-- +goose Up
-- Code coverage, measured by the repository's own tool (BE-6.6).
--
-- The number is never a model's estimate and never this platform's own instrumentation
-- (F-7.14). It is what the project's coverage tool printed, parsed from its native
-- report: a client comparing our figure against their CI has to see the same figure,
-- and the only way to guarantee that is to run the tool they run.
--
-- Kept separate from requirement coverage, in its own tables, because merging the two
-- produces a single number that means nothing: eighty per cent of requirements having
-- a test case and eighty per cent of lines being executed are different facts about
-- different things (FR-7.2, BE-6.7.2).

CREATE TABLE coverage_runs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- The revision measured, so a rise or fall can be attributed to a change rather
    -- than to a different checkout.
    commit_sha text NOT NULL DEFAULT '',

    -- Which tool produced it and what it was told to run. Recorded because "coverage
    -- is 74%" is only meaningful alongside "as measured by vitest run --coverage".
    tool    text NOT NULL DEFAULT '',
    command text NOT NULL DEFAULT '',

    -- Totals, as the tool reported them. Percentages are computed on read from these
    -- rather than stored, so a stored percentage can never disagree with its own
    -- numerator.
    lines_total     int NOT NULL DEFAULT 0,
    lines_covered   int NOT NULL DEFAULT 0,
    branches_total  int NOT NULL DEFAULT 0,
    branches_covered int NOT NULL DEFAULT 0,

    -- Empty when the tool ran and reported. Set when it could not, which is a
    -- different state from zero coverage.
    error text NOT NULL DEFAULT '',

    job_id     uuid REFERENCES jobs (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX coverage_runs_project_idx ON coverage_runs (project_id, created_at DESC);

CREATE TABLE coverage_files (
    id              bigserial PRIMARY KEY,
    coverage_run_id uuid NOT NULL REFERENCES coverage_runs (id) ON DELETE CASCADE,

    path text NOT NULL,

    lines_total      int NOT NULL DEFAULT 0,
    lines_covered    int NOT NULL DEFAULT 0,
    branches_total   int NOT NULL DEFAULT 0,
    branches_covered int NOT NULL DEFAULT 0
);

-- Ordered by how much of the file is untested, because that is the only order anybody
-- reads this list in.
CREATE INDEX coverage_files_run_idx ON coverage_files (coverage_run_id, lines_covered);

-- +goose Down
DROP TABLE IF EXISTS coverage_files;
DROP TABLE IF EXISTS coverage_runs;
