package audit

import (
	"context"
	"strconv"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the audit slice of the generated server interface.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) ListAuditEntries(
	ctx context.Context,
	request api.ListAuditEntriesRequestObject,
) (api.ListAuditEntriesResponseObject, error) {
	filter := Filter{
		ActorID:   request.Params.ActorID,
		ProjectID: request.Params.ProjectID,
		From:      request.Params.From,
		To:        request.Params.To,
	}
	if request.Params.Limit != nil {
		filter.Limit = *request.Params.Limit
	}
	if request.Params.Cursor != nil {
		filter.Cursor = *request.Params.Cursor
	}
	if request.Params.Action != nil {
		action, err := ParseAction(*request.Params.Action)
		if err != nil {
			return nil, err
		}
		filter.Action = string(action)
	}

	page, err := h.service.List(ctx, filter)
	if err != nil {
		return nil, err
	}

	body := api.AuditEntryPage{Items: make([]api.AuditEntry, 0, len(page.Items))}
	for _, record := range page.Items {
		body.Items = append(body.Items, ToAPI(record))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListAuditEntries200JSONResponse(body), nil
}

// ToAPI maps an audit record to its response shape.
//
// The id is a string in the contract because it is a cursor as much as a number,
// and a client that does arithmetic on it is doing something wrong.
func ToAPI(record Record) api.AuditEntry {
	out := api.AuditEntry{
		Id:      strconv.FormatInt(record.ID, 10),
		Action:  record.Action,
		Subject: record.Subject,
		At:      record.At,
	}

	if record.ActorEmail != "" {
		email := record.ActorEmail
		out.ActorEmail = &email
	}
	if record.ActorID != nil {
		out.ActorId.Set(*record.ActorID)
	}
	if record.ProjectID != nil {
		out.ProjectId.Set(*record.ProjectID)
	}
	if record.IP != nil {
		out.Ip.Set(record.IP.String())
	}
	if len(record.Detail) > 0 {
		detail := record.Detail
		out.Detail = &detail
	}
	return out
}
