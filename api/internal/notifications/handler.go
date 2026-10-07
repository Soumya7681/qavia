package notifications

import (
	"context"

	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the notifications slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) ListNotifications(
	ctx context.Context,
	request api.ListNotificationsRequestObject,
) (api.ListNotificationsResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	cursor := ""
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	unreadOnly := false
	if request.Params.UnreadOnly != nil {
		unreadOnly = *request.Params.UnreadOnly
	}

	page, err := h.service.List(ctx, principal.UserID, limit, cursor, unreadOnly)
	if err != nil {
		return nil, err
	}

	body := api.NotificationPage{
		Items:  make([]api.Notification, 0, len(page.Items)),
		Unread: int(page.Unread),
	}
	for _, item := range page.Items {
		body.Items = append(body.Items, ToAPI(item))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListNotifications200JSONResponse(body), nil
}

func (h *Handler) GetUnreadNotificationCount(
	ctx context.Context,
	_ api.GetUnreadNotificationCountRequestObject,
) (api.GetUnreadNotificationCountResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	unread, err := h.service.UnreadCount(ctx, principal.UserID)
	if err != nil {
		return nil, err
	}
	return api.GetUnreadNotificationCount200JSONResponse{Unread: int(unread)}, nil
}

func (h *Handler) MarkNotificationRead(
	ctx context.Context,
	request api.MarkNotificationReadRequestObject,
) (api.MarkNotificationReadResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	if err := h.service.MarkRead(ctx, principal.UserID, request.NotificationID); err != nil {
		return nil, err
	}
	return api.MarkNotificationRead204Response{}, nil
}

func (h *Handler) MarkAllNotificationsRead(
	ctx context.Context,
	_ api.MarkAllNotificationsReadRequestObject,
) (api.MarkAllNotificationsReadResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)

	updated, err := h.service.MarkAllRead(ctx, principal.UserID)
	if err != nil {
		return nil, err
	}
	return api.MarkAllNotificationsRead200JSONResponse{Updated: int(updated)}, nil
}
