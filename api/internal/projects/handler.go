package projects

import (
	"context"

	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the projects slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) ListProjects(
	ctx context.Context,
	request api.ListProjectsRequestObject,
) (api.ListProjectsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	includeArchived := false
	if request.Params.IncludeArchived != nil {
		includeArchived = *request.Params.IncludeArchived
	}

	page, err := h.service.List(ctx, actor, limit, cursor, includeArchived)
	if err != nil {
		return nil, err
	}

	body := api.ProjectPage{Items: make([]api.Project, 0, len(page.Items))}
	for _, project := range page.Items {
		body.Items = append(body.Items, ToAPI(project))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListProjects200JSONResponse(body), nil
}

func (h *Handler) CreateProject(
	ctx context.Context,
	request api.CreateProjectRequestObject,
) (api.CreateProjectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	project, err := h.service.Create(ctx, actor, toCreateInput(request.Body))
	if err != nil {
		return nil, err
	}
	return api.CreateProject201JSONResponse(ToAPI(project)), nil
}

func (h *Handler) GetProject(
	ctx context.Context,
	request api.GetProjectRequestObject,
) (api.GetProjectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	project, err := h.service.GetForActor(ctx, actor, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.GetProject200JSONResponse(ToAPI(project)), nil
}

func (h *Handler) UpdateProject(
	ctx context.Context,
	request api.UpdateProjectRequestObject,
) (api.UpdateProjectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	project, err := h.service.Update(ctx, actor, request.ProjectID, toUpdateInput(request.Body))
	if err != nil {
		return nil, err
	}
	return api.UpdateProject200JSONResponse(ToAPI(project)), nil
}

func (h *Handler) ArchiveProject(
	ctx context.Context,
	request api.ArchiveProjectRequestObject,
) (api.ArchiveProjectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	project, err := h.service.Archive(ctx, actor, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.ArchiveProject200JSONResponse(ToAPI(project)), nil
}

func (h *Handler) UnarchiveProject(
	ctx context.Context,
	request api.UnarchiveProjectRequestObject,
) (api.UnarchiveProjectResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	project, err := h.service.Unarchive(ctx, actor, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.UnarchiveProject200JSONResponse(ToAPI(project)), nil
}

func (h *Handler) SetExternalAIApproval(
	ctx context.Context,
	request api.SetExternalAIApprovalRequestObject,
) (api.SetExternalAIApprovalResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}

	project, err := h.service.SetExternalAIApproval(
		ctx, actor, request.ProjectID, request.Body.Approved, note)
	if err != nil {
		return nil, err
	}
	return api.SetExternalAIApproval200JSONResponse(ToAPI(project)), nil
}

func (h *Handler) ListProjectMembers(
	ctx context.Context,
	request api.ListProjectMembersRequestObject,
) (api.ListProjectMembersResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	members, err := h.service.Members(ctx, actor, request.ProjectID)
	if err != nil {
		return nil, err
	}

	body := api.ProjectMemberList{Items: make([]api.ProjectMember, 0, len(members))}
	for _, member := range members {
		body.Items = append(body.Items, ToAPIMember(member))
	}
	return api.ListProjectMembers200JSONResponse(body), nil
}

func (h *Handler) AddProjectMember(
	ctx context.Context,
	request api.AddProjectMemberRequestObject,
) (api.AddProjectMemberResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	member, err := h.service.AddMember(
		ctx, actor, request.ProjectID, request.UserID, role.Role(request.Body.Role))
	if err != nil {
		return nil, err
	}
	return api.AddProjectMember200JSONResponse(ToAPIMember(member)), nil
}

func (h *Handler) RemoveProjectMember(
	ctx context.Context,
	request api.RemoveProjectMemberRequestObject,
) (api.RemoveProjectMemberResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.service.RemoveMember(ctx, actor, request.ProjectID, request.UserID); err != nil {
		return nil, err
	}
	return api.RemoveProjectMember204Response{}, nil
}

func toCreateInput(body *api.CreateProjectJSONRequestBody) CreateInput {
	input := CreateInput{Name: body.Name}
	if body.Description != nil {
		input.Description = *body.Description
	}
	if body.TestTypes != nil {
		input.TestTypes = toTestTypes(*body.TestTypes)
	}
	return input
}

func toUpdateInput(body *api.UpdateProjectJSONRequestBody) UpdateInput {
	input := UpdateInput{Name: body.Name, Description: body.Description}
	if body.TestTypes != nil {
		types := toTestTypes(*body.TestTypes)
		input.TestTypes = &types
	}
	return input
}

func toTestTypes(values []api.TestType) []TestType {
	out := make([]TestType, 0, len(values))
	for _, value := range values {
		out = append(out, TestType(value))
	}
	return out
}
