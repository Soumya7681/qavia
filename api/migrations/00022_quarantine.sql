-- +goose Up
-- Flake quarantine (BE-7.6, F-7.12).
--
-- A test that fails one run in four teaches a team to ignore red, which costs more
-- than the test was ever worth. Quarantine is the alternative: the test keeps running
-- and keeps recording results, and it stops failing the run until somebody fixes it.
--
-- Two columns exist so a quarantine cannot become permanent by accident. `owner_id` is
-- who is answerable for it, and `created_at` is its age: a list of quarantines with
-- nobody's name and no dates is how a suite quietly stops testing anything.

CREATE TABLE quarantines (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- Keyed by test name rather than by test file or case: a name is what a report
    -- shows, what a retry filter matches, and the only identifier a suite generated
    -- from a flow graph and one written by hand both have.
    test_name text NOT NULL,

    -- The case it implements, when the platform could map one. Nullable, because a
    -- file with several cases maps to none of them unambiguously.
    test_case_id uuid REFERENCES test_cases (id) ON DELETE SET NULL,

    -- Why, in words, and the arithmetic behind them: this many flaky runs out of this
    -- many looked at. Stored rather than recomputed, because the threshold can change
    -- and a quarantine should still explain the decision that was actually made.
    reason      text NOT NULL DEFAULT '',
    flake_count int  NOT NULL DEFAULT 0,
    window_runs int  NOT NULL DEFAULT 0,

    -- auto when the platform quarantined it, manual when a person did.
    source text NOT NULL DEFAULT 'auto',

    -- Who is answerable. Null for an automatic quarantine nobody has claimed yet,
    -- which is exactly the state a review list exists to surface.
    owner_id uuid REFERENCES users (id) ON DELETE SET NULL,

    -- Set when it ends. A released row is kept: the history of what used to be flaky
    -- is what tells somebody whether a fix held.
    released_at timestamptz,
    released_by uuid REFERENCES users (id) ON DELETE SET NULL,
    release_note text NOT NULL DEFAULT '',

    -- The last run that saw this test flake, so an ageing quarantine can be told apart
    -- from a forgotten one: still flaking is a reason to keep it, and not flaking for a
    -- month is a reason to let it go.
    last_flaked_at timestamptz,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One live quarantine per test per project. A released row does not block a new one,
-- because a test that starts flaking again is a new decision with a new age.
CREATE UNIQUE INDEX quarantines_active_idx
    ON quarantines (project_id, test_name)
    WHERE released_at IS NULL;

CREATE INDEX quarantines_project_idx ON quarantines (project_id, created_at DESC);

-- Marks the result rows a quarantine excused, so a run's status can ignore them
-- without losing what actually happened: the test still passed or failed, and the
-- report still says which (BE-7.6.2).
ALTER TABLE run_results ADD COLUMN quarantined bool NOT NULL DEFAULT false;

-- A run's own tally of excused tests. Counted separately rather than folded into
-- passed, because a suite where six tests are excused is a different thing from one
-- where they all pass, and a dashboard that cannot tell them apart hides exactly the
-- problem quarantine was meant to make visible.
ALTER TABLE runs ADD COLUMN quarantined int NOT NULL DEFAULT 0;
ALTER TABLE runs ADD CONSTRAINT runs_quarantined_nonnegative CHECK (quarantined >= 0);

-- +goose Down
ALTER TABLE runs DROP CONSTRAINT IF EXISTS runs_quarantined_nonnegative;
ALTER TABLE runs DROP COLUMN IF EXISTS quarantined;
ALTER TABLE run_results DROP COLUMN IF EXISTS quarantined;
DROP TABLE IF EXISTS quarantines;
