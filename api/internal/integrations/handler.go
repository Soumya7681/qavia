package integrations

import (
	"context"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the integrations slice of the generated server interface.
type Handler struct {
	registries Registries
}

func NewHandler(registries Registries) *Handler {
	return &Handler{registries: registries}
}

// ListIntegrations reports the health of every integration.
func (h *Handler) ListIntegrations(
	ctx context.Context,
	_ api.ListIntegrationsRequestObject,
) (api.ListIntegrationsResponseObject, error) {
	report := Report(ctx, h.registries)

	items := make([]api.Integration, 0, len(report))
	for _, integration := range report {
		items = append(items, api.Integration{
			Capability: integration.Capability,
			Id:         integration.ID,
			Builtin:    integration.Builtin,
			State:      api.IntegrationState(integration.State),
		})
	}
	return api.ListIntegrations200JSONResponse(api.IntegrationList{Items: items}), nil
}
