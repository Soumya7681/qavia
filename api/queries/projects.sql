-- Projects, membership, and artifacts.

-- name: CreateProject :one
INSERT INTO projects (name, description, owner_id, test_types)
VALUES ($1, $2, $3, $4)
RETURNING id, name, description, owner_id, test_types, external_ai_approved, archived_at, created_at, updated_at;

-- name: GetProject :one
SELECT id, name, description, owner_id, test_types, external_ai_approved, archived_at, created_at, updated_at
FROM projects
WHERE id = $1;

-- ListProjectsForUser is the QA Engineer view: projects they own or are a member
-- of. A QA Lead or Admin uses ListAllProjects instead, and that difference is a
-- decision the service makes, not the query.
-- name: ListProjectsForUser :many
SELECT p.id, p.name, p.description, p.owner_id, p.test_types, p.external_ai_approved,
       p.archived_at, p.created_at, p.updated_at
FROM projects p
LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = $1
WHERE (p.owner_id = $1 OR m.user_id IS NOT NULL)
  AND (sqlc.narg('include_archived')::boolean OR p.archived_at IS NULL)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR p.created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY p.created_at DESC
LIMIT sqlc.arg('page_size');

-- name: ListAllProjects :many
SELECT id, name, description, owner_id, test_types, external_ai_approved, archived_at, created_at, updated_at
FROM projects
WHERE (sqlc.narg('include_archived')::boolean OR archived_at IS NULL)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: UpdateProject :one
UPDATE projects
SET name = $2, description = $3, test_types = $4, updated_at = now()
WHERE id = $1 AND archived_at IS NULL
RETURNING id, name, description, owner_id, test_types, external_ai_approved, archived_at, created_at, updated_at;

-- name: ArchiveProject :execrows
UPDATE projects
SET archived_at = now(), updated_at = now()
WHERE id = $1 AND archived_at IS NULL;

-- name: UnarchiveProject :execrows
UPDATE projects
SET archived_at = NULL, updated_at = now()
WHERE id = $1 AND archived_at IS NOT NULL;

-- SetExternalAIApproved is admin-only and audited. Without the flag a project may
-- only be assigned providers marked local (F-16.13).
-- name: SetExternalAIApproved :execrows
UPDATE projects
SET external_ai_approved = $2, updated_at = now()
WHERE id = $1;

-- name: AddProjectMember :exec
INSERT INTO project_members (project_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: RemoveProjectMember :execrows
DELETE FROM project_members WHERE project_id = $1 AND user_id = $2;

-- name: ListProjectMembers :many
SELECT m.project_id, m.user_id, m.role, m.created_at, u.email, u.name
FROM project_members m
JOIN users u ON u.id = m.user_id
WHERE m.project_id = $1
ORDER BY u.email;

-- GetProjectMembership backs the RequireProjectMembership middleware. Ownership
-- counts as membership so an owner never has to add themselves.
-- name: GetProjectMembership :one
SELECT p.id AS project_id,
       p.owner_id,
       p.archived_at,
       m.role AS member_role
FROM projects p
LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = $2
WHERE p.id = $1;
