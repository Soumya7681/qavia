-- +goose Up
-- Failure analysis and the built-in defect tracker (BE-5.1, BE-5.5).
--
-- Two rules shape this schema, and both come from the same place: an analysis is a
-- model's account of a failure, and a model's account is worth nothing without
-- something a person can check.
--
--   - **Evidence is structured, not prose.** A log line range, a response field
--     path, a source file and line. A UI renders those as links a reviewer clicks;
--     it cannot render a paragraph that says "see the logs" (BE-5.1.2).
--   - **The stability score is nullable and never a model's self-report.** It is
--     computed from re-run stability and commit overlap, and when neither signal
--     exists the column stays null and the API says why. A confident number with
--     nothing behind it is worse than no number (BE-5.4).
--
-- The defect tables ship here rather than in the integrations phase, because bug
-- tracking has to work on an installation with no Jira, no GitHub, and no Slack
-- (F-9.7, BE-5.5).

CREATE TABLE analyses (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    run_result_id  uuid NOT NULL REFERENCES run_results (id) ON DELETE CASCADE,

    -- reason is the one-line summary a list shows; root_cause is the explanation.
    reason      text NOT NULL,
    root_cause  text NOT NULL,

    -- suggested_fix is advice, never applied automatically. Nothing in this platform
    -- writes to a client's repository.
    suggested_fix text NOT NULL DEFAULT '',

    -- evidence is a list of references, each one checked against the artifact it
    -- points at before the row is written (BE-5.3).
    evidence jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- related_commit is set only when a repository is connected and a named file
    -- overlaps a recent commit. Empty is the normal case.
    related_commit text NOT NULL DEFAULT '',

    -- stability_score is null when no permitted signal was available. Null means
    -- "not measurable", never "zero".
    stability_score numeric(4, 3),

    -- The prompt version behind this analysis, so feedback can be grouped by it and
    -- prompt iteration has data rather than opinion (BE-5.8).
    prompt_version text NOT NULL DEFAULT '',
    model_name     text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now()
);

-- One analysis per result is the common case; a re-analysis after a prompt change is
-- a second row, and the newest wins in the UI. So: an index, not a unique
-- constraint.
CREATE INDEX analyses_run_result_idx ON analyses (run_result_id, created_at DESC);

CREATE TYPE defect_severity AS ENUM ('critical', 'high', 'medium', 'low');

-- The lifecycle a real team uses. `duplicate` is a status as well as a link,
-- because a duplicate that is only a link disappears from every status filter.
CREATE TYPE defect_status AS ENUM (
    'open', 'acknowledged', 'in_progress', 'fixed', 'wont_fix', 'duplicate'
);

CREATE TABLE defects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- Every link is optional and every one is a reference rather than a copy: a
    -- defect promoted from a failure points at the failure's artifacts instead of
    -- duplicating them (BE-5.6.2).
    run_result_id  uuid REFERENCES run_results (id) ON DELETE SET NULL,
    test_case_id   uuid REFERENCES test_cases (id) ON DELETE SET NULL,
    requirement_id uuid REFERENCES requirements (id) ON DELETE SET NULL,
    analysis_id    uuid REFERENCES analyses (id) ON DELETE SET NULL,

    title       text NOT NULL,
    description text NOT NULL DEFAULT '',

    severity defect_severity NOT NULL DEFAULT 'medium',
    status   defect_status   NOT NULL DEFAULT 'open',

    assignee_id uuid REFERENCES users (id) ON DELETE SET NULL,

    -- duplicate_of is proposed by the platform and reversible by a person: the
    -- system suggests, it does not overrule (BE-5.7.3).
    duplicate_of uuid REFERENCES defects (id) ON DELETE SET NULL,

    -- root_cause_key is the deterministic half of duplicate detection: the test case
    -- plus a normalised root cause. Stored so the match is a lookup rather than a
    -- model call (BE-5.7.2).
    root_cause_key text NOT NULL DEFAULT '',

    -- external_ref is where this defect lives in a tracker somebody else owns, once
    -- one is configured. Empty on an installation with no integration, which is the
    -- installation this phase has to work on.
    external_ref jsonb NOT NULL DEFAULT '{}'::jsonb,

    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    -- Set when the status becomes terminal, so "how long was this open" is a
    -- subtraction rather than a scan of a history table.
    resolved_at timestamptz
);

CREATE INDEX defects_project_status_idx ON defects (project_id, status, created_at DESC);
CREATE INDEX defects_project_severity_idx ON defects (project_id, severity, created_at DESC);
CREATE INDEX defects_assignee_idx ON defects (assignee_id) WHERE assignee_id IS NOT NULL;
CREATE INDEX defects_test_case_idx ON defects (test_case_id) WHERE test_case_id IS NOT NULL;
CREATE INDEX defects_duplicate_of_idx ON defects (duplicate_of) WHERE duplicate_of IS NOT NULL;

-- The promotion guard (BE-5.6.3): one defect per failure, so promoting the same
-- failure twice returns the existing defect instead of creating a second one.
CREATE UNIQUE INDEX defects_run_result_key ON defects (run_result_id)
    WHERE run_result_id IS NOT NULL;

-- The duplicate-detection index. Partial on open work, because a closed defect with
-- the same root cause is history: a failure recurring after a fix is a new defect,
-- not a comment on the old one.
CREATE INDEX defects_root_cause_open_idx ON defects (project_id, test_case_id, root_cause_key)
    WHERE status IN ('open', 'acknowledged', 'in_progress') AND root_cause_key <> '';

CREATE TABLE defect_comments (
    id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    defect_id uuid NOT NULL REFERENCES defects (id) ON DELETE CASCADE,

    -- Null for a comment the platform wrote itself, such as "linked as a duplicate
    -- of QA-114". Those are comments, not audit rows: they belong in the thread a
    -- person is reading.
    author_id uuid REFERENCES users (id) ON DELETE SET NULL,

    body text NOT NULL,

    -- system marks a comment the platform wrote, so the UI can style it differently
    -- from something a colleague said.
    system bool NOT NULL DEFAULT false,

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX defect_comments_defect_idx ON defect_comments (defect_id, created_at);

-- Feedback on an analysis (BE-5.8). One row per user per analysis, so a second vote
-- replaces the first rather than stacking.
CREATE TABLE analysis_feedback (
    analysis_id uuid NOT NULL REFERENCES analyses (id) ON DELETE CASCADE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    helpful bool NOT NULL,
    note    text NOT NULL DEFAULT '',

    -- Copied from the analysis rather than joined, because the question this table
    -- answers is "how did prompt v3 do", and the analysis row may be re-analysed
    -- under a later version.
    prompt_version text NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (analysis_id, user_id)
);

CREATE INDEX analysis_feedback_prompt_idx ON analysis_feedback (prompt_version, helpful);

-- +goose Down
DROP TABLE IF EXISTS analysis_feedback;
DROP TABLE IF EXISTS defect_comments;
DROP TABLE IF EXISTS defects;
DROP TYPE IF EXISTS defect_status;
DROP TYPE IF EXISTS defect_severity;
DROP TABLE IF EXISTS analyses;
