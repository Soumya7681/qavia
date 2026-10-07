package users

import (
	"context"

	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the users slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) ListUsers(
	ctx context.Context,
	request api.ListUsersRequestObject,
) (api.ListUsersResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}

	page, err := h.service.List(ctx, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.UserPage{Items: make([]api.User, 0, len(page.Items))}
	for _, user := range page.Items {
		body.Items = append(body.Items, auth.ToAPIUser(user))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListUsers200JSONResponse(body), nil
}

func (h *Handler) SetUserRole(
	ctx context.Context,
	request api.SetUserRoleRequestObject,
) (api.SetUserRoleResponseObject, error) {
	actor := actorFrom(ctx)

	user, err := h.service.SetRole(ctx, actor, request.UserID, role.Role(request.Body.Role))
	if err != nil {
		return nil, err
	}
	return api.SetUserRole200JSONResponse(auth.ToAPIUser(user)), nil
}

func (h *Handler) SetUserStatus(
	ctx context.Context,
	request api.SetUserStatusRequestObject,
) (api.SetUserStatusResponseObject, error) {
	actor := actorFrom(ctx)

	user, err := h.service.SetDisabled(ctx, actor, request.UserID, request.Body.Disabled)
	if err != nil {
		return nil, err
	}
	return api.SetUserStatus200JSONResponse(auth.ToAPIUser(user)), nil
}

func (h *Handler) RevokeUserSessions(
	ctx context.Context,
	request api.RevokeUserSessionsRequestObject,
) (api.RevokeUserSessionsResponseObject, error) {
	actor := actorFrom(ctx)

	revoked, err := h.service.RevokeSessions(ctx, actor, request.UserID)
	if err != nil {
		return nil, err
	}
	return api.RevokeUserSessions200JSONResponse{Revoked: int(revoked)}, nil
}

func (h *Handler) UnlockUser(
	ctx context.Context,
	request api.UnlockUserRequestObject,
) (api.UnlockUserResponseObject, error) {
	actor := actorFrom(ctx)

	if err := h.service.Unlock(ctx, actor, request.UserID); err != nil {
		return nil, err
	}
	return api.UnlockUser204Response{}, nil
}

// actorFrom builds the acting user from the principal.
//
// Only the identity is carried, because that is all these operations audit or check.
// A handler that needs more asks the service for it rather than widening the
// principal, which would put a domain type in a platform package.
func actorFrom(ctx context.Context) auth.User {
	principal := httpx.MustCurrentUser(ctx)
	return auth.User{ID: principal.UserID, Email: principal.Email, Role: principal.Role}
}
