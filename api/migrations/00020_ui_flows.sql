-- +goose Up
-- Discovered UI flow graphs (BE-7.2).
--
-- One row per discovery rather than one per project, for the same reason a repository
-- map is one row per exploration: a graph describes an application as it behaved at a
-- moment. When a later discovery finds two fewer pages, the earlier graph is what
-- makes that visible; overwriting it would leave nothing to compare against.
--
-- The graph is reviewable before anything is generated from it, which is the point of
-- storing it at all (BE-7.2.3). The trail travels inside the document: a discovery is
-- a model choosing what to click, forty times in a row, and a graph with no record of
-- what was clicked is a conclusion nobody can retrace.

CREATE TABLE ui_flows (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- What was explored, and how it authenticated. A graph with no target is a graph
    -- nobody can tell is stale; a graph with no auth mode cannot distinguish "this
    -- application has no authenticated pages" from "we never signed in".
    target    text NOT NULL DEFAULT '',
    auth_mode text NOT NULL DEFAULT 'none',

    -- The graph itself: pages, what a user can do on each, the flows worth testing,
    -- what could not be reached, and the trail of actions that found them.
    document jsonb NOT NULL,

    -- Counted out of the document so a list screen does not have to parse it.
    page_count int NOT NULL DEFAULT 0,
    flow_count int NOT NULL DEFAULT 0,

    steps     int  NOT NULL DEFAULT 0,
    cut_short bool NOT NULL DEFAULT false,

    -- The video, trace, and screenshots the discovery recorded, as object-store keys
    -- rather than bytes: a trace is megabytes and a database is the wrong place for
    -- it. Kept because a discovery that mapped an application wrongly is one somebody
    -- has to watch back (BE-7.5).
    artifacts jsonb NOT NULL DEFAULT '[]'::jsonb,

    -- Whether a person has looked at this graph. Generation from an unreviewed graph
    -- is allowed; the flag is what lets a UI say which is which (BE-7.2.3).
    reviewed_at timestamptz,
    reviewed_by uuid REFERENCES users (id) ON DELETE SET NULL,

    model_name text NOT NULL DEFAULT '',
    job_id     uuid REFERENCES jobs (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ui_flows_project_idx ON ui_flows (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS ui_flows;
