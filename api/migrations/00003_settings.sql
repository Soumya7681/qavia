-- +goose Up
-- One settings table, and its audit trail.
--
-- Everything except the six bootstrap environment variables lives here
-- (requirements.md 5). Adding a setting is a registry entry in Go, never a
-- migration and never a new column, which is what makes UI-first configuration
-- affordable rather than a permanent tax.

CREATE TABLE settings (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope       settings_scope NOT NULL,

    -- Null for global scope, the project or user id otherwise. There is no
    -- foreign key: the referenced table depends on the scope, and a nullable
    -- polymorphic reference is the honest shape here. Deletion cleanup is
    -- explicit in the owning service.
    scope_id    uuid,

    key         text        NOT NULL,
    value       jsonb       NOT NULL,

    -- Mirrors the registry declaration. Stored so a read path can refuse to
    -- return a secret without having to consult the registry first.
    is_secret   boolean     NOT NULL DEFAULT false,

    updated_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per (scope, scope_id, key). Two NULL scope_ids are distinct to a plain
-- unique index, so global rows need their own partial index to be constrained at
-- all.
CREATE UNIQUE INDEX settings_scoped_key ON settings (scope, scope_id, key)
    WHERE scope_id IS NOT NULL;

CREATE UNIQUE INDEX settings_global_key ON settings (key)
    WHERE scope_id IS NULL;

CREATE INDEX settings_key_idx ON settings (key);

-- Every write produces an audit row, in the same transaction as the write
-- (requirements.md 5.3). For secrets both values are '[redacted]': only the fact
-- of the change is kept.
CREATE TABLE settings_audit (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    scope      settings_scope NOT NULL,
    scope_id   uuid,
    key        text        NOT NULL,
    old_value  jsonb,
    new_value  jsonb,
    actor_id   uuid        REFERENCES users (id) ON DELETE SET NULL,
    at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX settings_audit_key_idx ON settings_audit (key, at DESC);
CREATE INDEX settings_audit_actor_idx ON settings_audit (actor_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS settings_audit;
DROP TABLE IF EXISTS settings;
