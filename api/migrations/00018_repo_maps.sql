-- +goose Up
-- Repository comprehension maps (BE-6.4).
--
-- One row per exploration rather than one per project, because a map describes a
-- revision: the map of last month's commit is not wrong, it is history, and
-- overwriting it would leave nothing to compare against when a later map says
-- something different.
--
-- The trail is stored with the map, and that is the part worth defending. An
-- exploration is a model choosing what to look at, twenty times in a row; a map with
-- no record of what was read is a conclusion nobody can retrace.

CREATE TABLE repo_maps (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,

    -- The revision this map describes. A map with no commit is a map nobody can tell
    -- is stale.
    commit_sha text NOT NULL DEFAULT '',
    stack      text NOT NULL DEFAULT '',

    -- The map itself: controllers, services, data access, untested paths, unknowns,
    -- and the trail of what was read to find them.
    document jsonb NOT NULL,

    steps      int  NOT NULL DEFAULT 0,
    cut_short  bool NOT NULL DEFAULT false,

    model_name  text NOT NULL DEFAULT '',
    job_id      uuid REFERENCES jobs (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX repo_maps_project_idx ON repo_maps (project_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS repo_maps;
