package health

import (
	"context"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Handler implements the health slice of the generated server interface.
//
// It is HTTP only: it calls one service method and maps the result to a response
// type. There is no logic here, which is the rule for every handler in the
// codebase (backend-standards.md 2).
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetLiveness(
	_ context.Context,
	_ api.GetLivenessRequestObject,
) (api.GetLivenessResponseObject, error) {
	return api.GetLiveness200JSONResponse{
		Status:  api.LivenessStatusOk,
		Version: h.service.Version(),
	}, nil
}

func (h *Handler) GetReadiness(
	ctx context.Context,
	_ api.GetReadinessRequestObject,
) (api.GetReadinessResponseObject, error) {
	ready, checks := h.service.Readiness(ctx)

	body := api.Readiness{
		Status: api.ReadinessStatusOk,
		Checks: toAPIChecks(checks),
	}
	if !ready {
		body.Status = api.ReadinessStatusUnavailable
		return api.GetReadiness503JSONResponse(body), nil
	}
	return api.GetReadiness200JSONResponse(body), nil
}

// toAPIChecks is the mapper. No internal type crosses into a response without
// passing through one (backend-standards.md 4).
func toAPIChecks(checks []Check) []api.DependencyCheck {
	out := make([]api.DependencyCheck, 0, len(checks))
	for _, check := range checks {
		mapped := api.DependencyCheck{
			Name:   check.Name,
			Status: api.CheckStatus(check.Status),
		}
		if check.Detail != "" {
			detail := check.Detail
			mapped.Detail = &detail
		}
		out = append(out, mapped)
	}
	return out
}
