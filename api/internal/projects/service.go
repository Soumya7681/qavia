package projects

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service owns projects and membership.
type Service struct {
	db       *store.DB
	recorder *audit.Recorder
}

func NewService(db *store.DB, recorder *audit.Recorder) *Service {
	return &Service{db: db, recorder: recorder}
}

// CreateInput is a new project.
type CreateInput struct {
	Name        string
	Description string
	TestTypes   []TestType
}

// UpdateInput carries only what a caller asked to change. A nil field is left
// alone, which is what makes PATCH different from PUT here.
type UpdateInput struct {
	Name        *string
	Description *string
	TestTypes   *[]TestType
}

// Create makes a project owned by the caller.
//
// Ownership counts as membership, so no membership row is written for the owner:
// one fewer row to keep in step, and an owner can never lose access to their own
// project by having that row deleted.
func (s *Service) Create(ctx context.Context, actor httpx.Principal, input CreateInput) (Project, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Project{}, apierr.Validation("Give the project a name.",
			map[string]any{"field": "name"})
	}

	types, err := normalizeTestTypes(input.TestTypes)
	if err != nil {
		return Project{}, err
	}

	row, err := s.db.Queries().CreateProject(ctx, dbgen.CreateProjectParams{
		Name:        name,
		Description: strings.TrimSpace(input.Description),
		OwnerID:     actor.UserID,
		TestTypes:   types,
	})
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}

	project := toProject(row)
	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectCreated,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    project.Name,
		ProjectID:  &project.ID,
		Detail:     map[string]any{"testTypes": types},
	})
	return project, nil
}

// List returns the projects the caller may see.
//
// A QA Lead and an Admin see every project, because they answer for the whole
// platform. Everybody else sees the projects they own or are a member of. The
// difference is applied in the query rather than by filtering afterwards, so a
// caller cannot page past their own visibility.
func (s *Service) List(
	ctx context.Context,
	actor httpx.Principal,
	limit int,
	cursor string,
	includeArchived bool,
) (Page, error) {
	limit = paging.ClampLimit(limit)
	pageSize := int32(limit + 1)

	var at *time.Time
	if cursor != "" {
		decoded, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		at = &decoded
	}

	var (
		rows []dbgen.Project
		err  error
	)
	if seesEveryProject(actor.Role) {
		rows, err = s.db.Queries().ListAllProjects(ctx, dbgen.ListAllProjectsParams{
			IncludeArchived: &includeArchived,
			Cursor:          at,
			PageSize:        pageSize,
		})
	} else {
		rows, err = s.db.Queries().ListProjectsForUser(ctx, dbgen.ListProjectsForUserParams{
			UserID:          actor.UserID,
			IncludeArchived: &includeArchived,
			Cursor:          at,
			PageSize:        pageSize,
		})
	}
	if err != nil {
		return Page{}, fmt.Errorf("list projects: %w", err)
	}

	page := Page{Items: make([]Project, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = paging.EncodeTime(rows[i-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toProject(row))
	}
	return page, nil
}

// Get loads one project without an access check. Callers that serve a request use
// GetForActor; this exists for the job pipeline, which runs without a user.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Project, error) {
	row, err := s.db.Queries().GetProject(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Project{}, apierr.ProjectNotFound(id)
		}
		return Project{}, fmt.Errorf("load project %s: %w", id, err)
	}
	return toProject(row), nil
}

// GetForActor loads a project the caller is allowed to see.
//
// A project the caller cannot see is reported as not found rather than forbidden:
// the two are deliberately indistinguishable, so a caller cannot enumerate which
// projects exist.
func (s *Service) GetForActor(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Project, error) {
	if err := s.EnsureMember(ctx, actor, id); err != nil {
		return Project{}, err
	}
	return s.Get(ctx, id)
}

// Update renames a project or changes its test types.
func (s *Service) Update(
	ctx context.Context,
	actor httpx.Principal,
	id uuid.UUID,
	input UpdateInput,
) (Project, error) {
	existing, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return Project{}, err
	}
	if existing.Archived() {
		return Project{}, apierr.ProjectArchived()
	}

	name := existing.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
		if name == "" {
			return Project{}, apierr.Validation("Give the project a name.",
				map[string]any{"field": "name"})
		}
	}

	description := existing.Description
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
	}

	types := make([]string, 0, len(existing.TestTypes))
	for _, testType := range existing.TestTypes {
		types = append(types, string(testType))
	}
	if input.TestTypes != nil {
		types, err = normalizeTestTypes(*input.TestTypes)
		if err != nil {
			return Project{}, err
		}
	}

	row, err := s.db.Queries().UpdateProject(ctx, dbgen.UpdateProjectParams{
		ID: id, Name: name, Description: description, TestTypes: types,
	})
	if err != nil {
		if store.IsNotFound(err) {
			// The row exists, so no rows updated means the archived_at guard in the
			// query matched: it was archived between the read and the write.
			return Project{}, apierr.ProjectArchived()
		}
		return Project{}, fmt.Errorf("update project %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectUpdated,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    name,
		ProjectID:  &id,
	})
	return toProject(row), nil
}

// Archive makes a project read-only.
//
// A soft delete, because history is a requirement here: runs, defects, and
// generated suites stay readable. Archiving twice is a success, so a retried
// request does not surface as an error.
func (s *Service) Archive(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Project, error) {
	project, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return Project{}, err
	}

	if _, err := s.db.Queries().ArchiveProject(ctx, id); err != nil {
		return Project{}, fmt.Errorf("archive project %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectArchived,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    project.Name,
		ProjectID:  &id,
	})
	return s.Get(ctx, id)
}

// Unarchive restores a project to writable.
func (s *Service) Unarchive(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Project, error) {
	project, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return Project{}, err
	}

	if _, err := s.db.Queries().UnarchiveProject(ctx, id); err != nil {
		return Project{}, fmt.Errorf("unarchive project %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectUnarchived,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    project.Name,
		ProjectID:  &id,
	})
	return s.Get(ctx, id)
}

// SetExternalAIApproval records whether the project may be processed by a
// provider that is not local.
//
// The flag is checked before an AI job is enqueued, not inside a worker, so a
// project that is not approved fails at submit with an explanation (F-16.13).
func (s *Service) SetExternalAIApproval(
	ctx context.Context,
	actor httpx.Principal,
	id uuid.UUID,
	approved bool,
	note string,
) (Project, error) {
	project, err := s.Get(ctx, id)
	if err != nil {
		return Project{}, err
	}

	if _, err := s.db.Queries().SetExternalAIApproved(ctx, dbgen.SetExternalAIApprovedParams{
		ID: id, ExternalAiApproved: approved,
	}); err != nil {
		return Project{}, fmt.Errorf("set external AI approval on %s: %w", id, err)
	}

	detail := map[string]any{"approved": approved}
	if note = strings.TrimSpace(note); note != "" {
		detail["note"] = note
	}
	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectApproval,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    project.Name,
		ProjectID:  &id,
		Detail:     detail,
	})
	return s.Get(ctx, id)
}

// ------------------------------------------------------------------ membership

// Members lists everybody with access, owner first.
func (s *Service) Members(ctx context.Context, actor httpx.Principal, id uuid.UUID) ([]Member, error) {
	project, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Queries().ListProjectMembers(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("list project members: %w", err)
	}

	members := make([]Member, 0, len(rows)+1)
	ownerListed := false
	for _, row := range rows {
		member := Member{
			UserID:    row.UserID,
			Email:     row.Email,
			Name:      row.Name,
			Role:      role.Role(row.Role),
			IsOwner:   row.UserID == project.OwnerID,
			CreatedAt: row.CreatedAt,
		}
		ownerListed = ownerListed || member.IsOwner
		members = append(members, member)
	}

	if !ownerListed {
		// The owner has access without a membership row, so a list that showed only
		// rows would leave out the one person who certainly has access.
		owner, err := s.db.Queries().GetUserByID(ctx, project.OwnerID)
		if err != nil {
			return nil, fmt.Errorf("load project owner: %w", err)
		}
		members = append([]Member{{
			UserID:    owner.ID,
			Email:     owner.Email,
			Name:      owner.Name,
			Role:      role.Role(owner.Role),
			IsOwner:   true,
			CreatedAt: project.CreatedAt,
		}}, members...)
	}
	return members, nil
}

// AddMember grants access, or changes an existing member's project role.
func (s *Service) AddMember(
	ctx context.Context,
	actor httpx.Principal,
	id, userID uuid.UUID,
	projectRole role.Role,
) (Member, error) {
	if !projectRole.Valid() {
		return Member{}, apierr.Validation("Choose a valid role.", map[string]any{"field": "role"})
	}

	project, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return Member{}, err
	}
	if project.Archived() {
		return Member{}, apierr.ProjectArchived()
	}

	user, err := s.db.Queries().GetUserByID(ctx, userID)
	if err != nil {
		if store.IsNotFound(err) {
			return Member{}, apierr.UserNotFound(userID)
		}
		return Member{}, fmt.Errorf("load user %s: %w", userID, err)
	}

	if err := s.db.Queries().AddProjectMember(ctx, dbgen.AddProjectMemberParams{
		ProjectID: id, UserID: userID, Role: dbgen.UserRole(projectRole),
	}); err != nil {
		return Member{}, fmt.Errorf("add project member: %w", err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectMemberAdded,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    user.Email,
		ProjectID:  &id,
		Detail:     map[string]any{"role": string(projectRole), "project": project.Name},
	})

	return Member{
		UserID:  user.ID,
		Email:   user.Email,
		Name:    user.Name,
		Role:    projectRole,
		IsOwner: user.ID == project.OwnerID,
	}, nil
}

// RemoveMember revokes access.
//
// The owner cannot be removed: a project with nobody who can administer it is a
// support call, and transferring ownership is the operation somebody actually
// wants.
func (s *Service) RemoveMember(ctx context.Context, actor httpx.Principal, id, userID uuid.UUID) error {
	project, err := s.GetForActor(ctx, actor, id)
	if err != nil {
		return err
	}
	if project.Archived() {
		return apierr.ProjectArchived()
	}
	if project.OwnerID == userID {
		return apierr.Conflict(
			"The project owner cannot be removed. Transfer ownership first.")
	}

	if _, err := s.db.Queries().RemoveProjectMember(ctx, dbgen.RemoveProjectMemberParams{
		ProjectID: id, UserID: userID,
	}); err != nil {
		return fmt.Errorf("remove project member: %w", err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionProjectMemberRemoved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    userID.String(),
		ProjectID:  &id,
		Detail:     map[string]any{"project": project.Name},
	})
	return nil
}

// EnsureMember is the authorisation check for everything scoped to a project.
//
// It is called by the membership middleware for routes that carry a project ID,
// and by the services that own a nested resource, which resolve the owning project
// first. Both paths land here so the rule lives in one place.
func (s *Service) EnsureMember(ctx context.Context, actor httpx.Principal, id uuid.UUID) error {
	if seesEveryProject(actor.Role) {
		// A QA Lead and an Admin answer for the whole platform. The project still
		// has to exist, so this is not a skip.
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		return nil
	}

	row, err := s.db.Queries().GetProjectMembership(ctx, dbgen.GetProjectMembershipParams{
		ID: id, UserID: actor.UserID,
	})
	if err != nil {
		if store.IsNotFound(err) {
			return apierr.ProjectNotFound(id)
		}
		return fmt.Errorf("load project membership: %w", err)
	}

	if row.OwnerID == actor.UserID || row.MemberRole.Valid {
		return nil
	}
	return apierr.NotProjectMember()
}

// Name returns a project's display name.
//
// Exported for the places that put a project's name into something a user reads:
// an export README, a Postman collection title. They ask for the name rather than
// the project, so a column added here does not ripple into them.
func (s *Service) Name(ctx context.Context, id uuid.UUID) (string, error) {
	project, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return project.Name, nil
}

// ExternalAIApproved reports whether the project may be processed by a provider
// that is not local (F-16.13).
//
// The AI layer asks this rather than reading the column, because the flag only
// means anything alongside the rules this service owns: it is admin-set, audited,
// and checked before any AI job is enqueued.
func (s *Service) ExternalAIApproved(ctx context.Context, id uuid.UUID) (bool, error) {
	project, err := s.Get(ctx, id)
	if err != nil {
		return false, err
	}
	return project.ExternalAIApproved, nil
}

// EnsureActive reports whether work may be submitted against a project.
//
// The job pipeline calls this at enqueue time. Failing here means the user learns
// at submit rather than from a job that failed twenty minutes later
// (backend-standards.md 5).
func (s *Service) EnsureActive(ctx context.Context, id uuid.UUID) error {
	project, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if project.Archived() {
		return apierr.ProjectArchived()
	}
	return nil
}

// seesEveryProject reports whether a role's remit is the whole platform.
//
// An exact set, not a floor: a future role that happens to outrank QA Engineer
// must not silently gain visibility of every project.
func seesEveryProject(r role.Role) bool {
	return r == role.Admin || r == role.QALead
}

// normalizeTestTypes validates the selection and removes duplicates.
//
// An unimplemented type is accepted rather than rejected: it is returned as
// unavailable with a reason, so the choice is remembered and the UI disables it
// until the phase that implements it lands (F-3.12).
func normalizeTestTypes(types []TestType) ([]string, error) {
	out := make([]string, 0, len(types))
	seen := make(map[TestType]struct{}, len(types))

	for _, testType := range types {
		if !testType.Valid() {
			return nil, apierr.Validation(
				fmt.Sprintf("%q is not a test type this platform knows.", testType),
				map[string]any{"field": "testTypes"})
		}
		if _, duplicate := seen[testType]; duplicate {
			continue
		}
		seen[testType] = struct{}{}
		out = append(out, string(testType))
	}
	return out, nil
}
