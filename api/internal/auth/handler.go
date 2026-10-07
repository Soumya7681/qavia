package auth

import (
	"context"
	"net/url"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/role"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the auth slice of the generated server interface.
//
// Every method is short enough to read at a glance: take the generated request
// type, call one service method, map the result. An `if` that is not an error check
// belongs in the service (backend-standards.md 2).
type Handler struct {
	service *Service

	// appURL builds the invitation link. It comes from the bootstrap config
	// because a notification may be sent before an admin has visited settings.
	appURL *url.URL
}

func NewHandler(service *Service, appURL *url.URL) *Handler {
	return &Handler{service: service, appURL: appURL}
}

func (h *Handler) Login(
	ctx context.Context,
	request api.LoginRequestObject,
) (api.LoginResponseObject, error) {
	user, err := h.service.Login(ctx, string(request.Body.Email), request.Body.Password, RequestFrom(ctx))
	if err != nil {
		return nil, err
	}
	return api.Login200JSONResponse(ToAPIUser(user)), nil
}

func (h *Handler) Logout(
	ctx context.Context,
	_ api.LogoutRequestObject,
) (api.LogoutResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	if err := h.service.Logout(ctx, User{ID: principal.UserID, Email: principal.Email}); err != nil {
		return nil, err
	}
	return api.Logout204Response{}, nil
}

func (h *Handler) GetCurrentUser(
	ctx context.Context,
	_ api.GetCurrentUserRequestObject,
) (api.GetCurrentUserResponseObject, error) {
	user, found, err := h.service.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.Unauthenticated()
	}
	return api.GetCurrentUser200JSONResponse(ToAPIUser(user)), nil
}

func (h *Handler) AcceptInvite(
	ctx context.Context,
	request api.AcceptInviteRequestObject,
) (api.AcceptInviteResponseObject, error) {
	name := ""
	if request.Body.Name != nil {
		name = *request.Body.Name
	}

	user, err := h.service.AcceptInvite(ctx,
		request.Body.Token, name, request.Body.Password, RequestFrom(ctx))
	if err != nil {
		return nil, err
	}
	return api.AcceptInvite200JSONResponse(ToAPIUser(user)), nil
}

func (h *Handler) ChangePassword(
	ctx context.Context,
	request api.ChangePasswordRequestObject,
) (api.ChangePasswordResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	err := h.service.ChangePassword(ctx,
		User{ID: principal.UserID, Email: principal.Email},
		request.Body.CurrentPassword, request.Body.NewPassword)
	if err != nil {
		return nil, err
	}
	return api.ChangePassword204Response{}, nil
}

func (h *Handler) InviteUser(
	ctx context.Context,
	request api.InviteUserRequestObject,
) (api.InviteUserResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	name := ""
	if request.Body.Name != nil {
		name = *request.Body.Name
	}

	invitation, err := h.service.Invite(ctx,
		User{ID: principal.UserID, Email: principal.Email},
		string(request.Body.Email), name, role.Role(request.Body.Role))
	if err != nil {
		return nil, err
	}

	return api.InviteUser201JSONResponse(api.Invitation{
		User:      ToAPIUser(invitation.User),
		AcceptUrl: h.acceptURL(invitation.Token),
		ExpiresAt: invitation.ExpiresAt,
	}), nil
}

// acceptURL builds the link an admin passes to the invitee. Email is optional, so
// on a zero-integration install this is the delivery mechanism.
func (h *Handler) acceptURL(token string) string {
	link := *h.appURL
	link.Path = "/accept-invite"
	link.RawQuery = url.Values{"token": {token}}.Encode()
	return link.String()
}

// ToAPIUser is the mapper for User. It is exported because the users package
// returns the same type and two mappers for one schema would drift
// (backend-standards.md 4).
func ToAPIUser(user User) api.User {
	out := api.User{
		Id:        user.ID,
		Email:     openapi_types.Email(user.Email),
		Name:      user.Name,
		Role:      api.Role(user.Role),
		Timezone:  user.Timezone,
		Disabled:  user.Disabled,
		CreatedAt: user.CreatedAt,
	}
	if user.LastLoginAt != nil {
		out.LastLoginAt.Set(*user.LastLoginAt)
	}
	return out
}
