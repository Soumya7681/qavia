package llm

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/settings"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// defaultSpendWindow is what /ai/spend reports when a caller does not ask for a
// range. Thirty days covers a monthly budget conversation without making the
// default query scan the whole table.
const defaultSpendWindow = 30 * 24 * time.Hour

// Handler implements the AI slice of the generated server interface.
//
// Admin-only at the route group, so nothing here re-checks a role: authorisation
// belongs in one layer (backend-standards.md 11).
type Handler struct {
	service  *Service
	gateway  *Gateway
	settings SettingsWriter
}

// SettingsWriter is the slice of the settings service the budget endpoint needs.
// The budget is stored as ordinary settings so this endpoint and the settings
// screen cannot drift apart.
type SettingsWriter interface {
	SettingsReader
	Write(ctx context.Context, actor settings.Actor, request settings.WriteRequest) (settings.Value, error)
}

func NewHandler(service *Service, gateway *Gateway, settingsService SettingsWriter) *Handler {
	return &Handler{service: service, gateway: gateway, settings: settingsService}
}

func (h *Handler) ListAIProviders(
	ctx context.Context,
	_ api.ListAIProvidersRequestObject,
) (api.ListAIProvidersResponseObject, error) {
	providers, err := h.service.ListProviders(ctx)
	if err != nil {
		return nil, err
	}

	body := api.AIProviderList{
		Items: make([]api.AIProvider, 0, len(providers)),
		Kinds: make([]api.AIProviderKind, 0, len(AllKinds)),
	}
	for _, provider := range providers {
		body.Items = append(body.Items, toAPIProvider(provider))
	}
	for _, kind := range AllKinds {
		body.Kinds = append(body.Kinds, api.AIProviderKind(kind))
	}
	return api.ListAIProviders200JSONResponse(body), nil
}

func (h *Handler) CreateAIProvider(
	ctx context.Context,
	request api.CreateAIProviderRequestObject,
) (api.CreateAIProviderResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := CreateProviderInput{
		Name:        request.Body.Name,
		Kind:        Kind(request.Body.Kind),
		Credentials: Credentials(request.Body.Credentials),
		Enabled:     true,
	}
	if request.Body.Config != nil {
		input.Config = *request.Body.Config
	}
	if request.Body.DataResidency != nil {
		input.Residency = Residency(*request.Body.DataResidency)
	}
	if request.Body.Enabled != nil {
		input.Enabled = *request.Body.Enabled
	}
	if request.Body.MakeDefault != nil {
		input.MakeDefault = *request.Body.MakeDefault
	}

	provider, err := h.service.CreateProvider(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	return api.CreateAIProvider201JSONResponse(toAPIProvider(provider)), nil
}

func (h *Handler) GetAIProvider(
	ctx context.Context,
	request api.GetAIProviderRequestObject,
) (api.GetAIProviderResponseObject, error) {
	provider, err := h.service.GetProvider(ctx, request.ProviderID)
	if err != nil {
		return nil, err
	}
	return api.GetAIProvider200JSONResponse(toAPIProvider(provider)), nil
}

func (h *Handler) UpdateAIProvider(
	ctx context.Context,
	request api.UpdateAIProviderRequestObject,
) (api.UpdateAIProviderResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := UpdateProviderInput{Enabled: true}
	if request.Body.Name != nil {
		input.Name = *request.Body.Name
	}
	if request.Body.Config != nil {
		input.Config = *request.Body.Config
	}
	if request.Body.DataResidency != nil {
		input.Residency = Residency(*request.Body.DataResidency)
	}
	if request.Body.Credentials != nil {
		input.Credentials = Credentials(*request.Body.Credentials)
	}
	if request.Body.Enabled != nil {
		input.Enabled = *request.Body.Enabled
	}
	if request.Body.MakeDefault != nil {
		input.MakeDefault = *request.Body.MakeDefault
	}

	provider, err := h.service.UpdateProvider(ctx, actor, request.ProviderID, input)
	if err != nil {
		return nil, err
	}
	return api.UpdateAIProvider200JSONResponse(toAPIProvider(provider)), nil
}

func (h *Handler) DeleteAIProvider(
	ctx context.Context,
	request api.DeleteAIProviderRequestObject,
) (api.DeleteAIProviderResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.service.DeleteProvider(ctx, actor, request.ProviderID); err != nil {
		return nil, err
	}
	return api.DeleteAIProvider204Response{}, nil
}

func (h *Handler) TestAIProvider(
	ctx context.Context,
	request api.TestAIProviderRequestObject,
) (api.TestAIProviderResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	var modelID *uuid.UUID
	if request.Body != nil && request.Body.ModelId != nil {
		modelID = request.Body.ModelId
	}

	outcome, err := h.service.TestProvider(ctx, actor, request.ProviderID, modelID)
	if err != nil {
		return nil, err
	}

	detail := outcome.Detail
	latency := outcome.LatencyMS
	return api.TestAIProvider200JSONResponse(api.AIProbeResult{
		ProviderId:   outcome.ProviderID,
		ModelId:      outcome.ModelID,
		Reachable:    outcome.Reachable,
		Capabilities: toAPICapabilityMatrix(outcome.Capabilities),
		Detail:       &detail,
		LatencyMs:    &latency,
	}), nil
}

func (h *Handler) ListAIModels(
	ctx context.Context,
	request api.ListAIModelsRequestObject,
) (api.ListAIModelsResponseObject, error) {
	models, err := h.service.ListModels(ctx, request.Params.ProviderID)
	if err != nil {
		return nil, err
	}

	body := api.AIModelList{Items: make([]api.AIModel, 0, len(models))}
	for _, model := range models {
		body.Items = append(body.Items, toAPIModel(model))
	}
	return api.ListAIModels200JSONResponse(body), nil
}

func (h *Handler) CreateAIModel(
	ctx context.Context,
	request api.CreateAIModelRequestObject,
) (api.CreateAIModelResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input, err := toModelInput(request.Body.ProviderId, modelFields{
		ModelID:         &request.Body.ModelId,
		DisplayName:     request.Body.DisplayName,
		Tiers:           &request.Body.Tiers,
		Capabilities:    request.Body.Capabilities,
		PriceInput:      request.Body.PriceInput,
		PriceOutput:     request.Body.PriceOutput,
		PriceCacheRead:  request.Body.PriceCacheRead,
		PriceCacheWrite: request.Body.PriceCacheWrite,
		MaxInputTokens:  request.Body.MaxInputTokens,
		MaxOutputTokens: request.Body.MaxOutputTokens,
		Enabled:         request.Body.Enabled,
	})
	if err != nil {
		return nil, err
	}

	model, err := h.service.CreateModel(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	return api.CreateAIModel201JSONResponse(toAPIModel(model)), nil
}

func (h *Handler) UpdateAIModel(
	ctx context.Context,
	request api.UpdateAIModelRequestObject,
) (api.UpdateAIModelResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	existing, err := h.service.GetModel(ctx, request.ModelID)
	if err != nil {
		return nil, err
	}

	tiers := existing.Tiers
	if request.Body.Tiers != nil {
		tiers = nil
		for _, tier := range *request.Body.Tiers {
			tiers = append(tiers, Tier(tier))
		}
	}

	input := ModelInput{
		ProviderID:      existing.ProviderID,
		ModelID:         existing.ModelID,
		DisplayName:     existing.DisplayName,
		Tiers:           tiers,
		PriceInput:      existing.PriceInput,
		PriceOutput:     existing.PriceOutput,
		PriceCacheRead:  existing.PriceCacheRead,
		PriceCacheWrite: existing.PriceCacheWrite,
		MaxInputTokens:  existing.MaxInputTokens,
		MaxOutputTokens: existing.MaxOutputTokens,
		Enabled:         true,
	}
	if request.Body.DisplayName != nil {
		input.DisplayName = *request.Body.DisplayName
	}
	if request.Body.Capabilities != nil {
		capabilities := fromAPICapabilityMatrix(*request.Body.Capabilities)
		input.Capabilities = &capabilities
	}
	if input.PriceInput, err = priceOr(request.Body.PriceInput, existing.PriceInput); err != nil {
		return nil, err
	}
	if input.PriceOutput, err = priceOr(request.Body.PriceOutput, existing.PriceOutput); err != nil {
		return nil, err
	}
	if input.PriceCacheRead, err = optionalPrice(request.Body.PriceCacheRead, existing.PriceCacheRead); err != nil {
		return nil, err
	}
	if input.PriceCacheWrite, err = optionalPrice(request.Body.PriceCacheWrite, existing.PriceCacheWrite); err != nil {
		return nil, err
	}
	if request.Body.MaxInputTokens != nil {
		input.MaxInputTokens = request.Body.MaxInputTokens
	}
	if request.Body.MaxOutputTokens != nil {
		input.MaxOutputTokens = request.Body.MaxOutputTokens
	}
	if request.Body.Enabled != nil {
		input.Enabled = *request.Body.Enabled
	}

	model, err := h.service.UpdateModel(ctx, actor, request.ModelID, input)
	if err != nil {
		return nil, err
	}
	return api.UpdateAIModel200JSONResponse(toAPIModel(model)), nil
}

func (h *Handler) DeleteAIModel(
	ctx context.Context,
	request api.DeleteAIModelRequestObject,
) (api.DeleteAIModelResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.service.DeleteModel(ctx, actor, request.ModelID); err != nil {
		return nil, err
	}
	return api.DeleteAIModel204Response{}, nil
}

func (h *Handler) ListAITiers(
	ctx context.Context,
	request api.ListAITiersRequestObject,
) (api.ListAITiersResponseObject, error) {
	assignments, err := h.service.ListAssignments(ctx, request.Params.ProjectID)
	if err != nil {
		return nil, err
	}

	body := api.AITierAssignmentList{
		Items: make([]api.AITierAssignment, 0, len(assignments)),
	}
	for _, assignment := range assignments {
		body.Items = append(body.Items, toAPIAssignment(assignment))
	}
	return api.ListAITiers200JSONResponse(body), nil
}

func (h *Handler) AssignAITier(
	ctx context.Context,
	request api.AssignAITierRequestObject,
) (api.AssignAITierResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := AssignInput{
		Tier:            Tier(request.Body.Tier),
		ProjectID:       request.Body.ProjectID,
		ModelID:         request.Body.ModelId,
		FallbackModelID: request.Body.FallbackModelId,
		Effort:          request.Body.Effort,
	}

	assignment, err := h.service.Assign(ctx, actor, input)
	if err != nil {
		return nil, err
	}
	return api.AssignAITier200JSONResponse(toAPIAssignment(assignment)), nil
}

func (h *Handler) UnassignAITier(
	ctx context.Context,
	request api.UnassignAITierRequestObject,
) (api.UnassignAITierResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.service.Unassign(ctx, actor, request.AssignmentID); err != nil {
		return nil, err
	}
	return api.UnassignAITier204Response{}, nil
}

func (h *Handler) GetAIBudget(
	ctx context.Context,
	request api.GetAIBudgetRequestObject,
) (api.GetAIBudgetResponseObject, error) {
	budget, err := h.budget(ctx, request.Params.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.GetAIBudget200JSONResponse(budget), nil
}

func (h *Handler) UpdateAIBudget(
	ctx context.Context,
	request api.UpdateAIBudgetRequestObject,
) (api.UpdateAIBudgetResponseObject, error) {
	principal := httpx.MustCurrentUser(ctx)
	actor := settings.Actor{UserID: principal.UserID, Email: principal.Email, Role: principal.Role}

	projectID := request.Body.ProjectID

	if request.Body.CeilingUsd != nil {
		ceiling, err := decimal.NewFromString(*request.Body.CeilingUsd)
		if err != nil || ceiling.IsNegative() {
			return nil, apierr.Validation("The ceiling must be a number, and not negative.",
				map[string]any{"field": "ceilingUsd"})
		}

		scope := settings.ScopeGlobal
		if projectID != nil {
			scope = settings.ScopeProject
		}
		if _, err := h.settings.Write(ctx, actor, settings.WriteRequest{
			Key:     "ai.spend_ceiling_usd",
			Scope:   scope,
			ScopeID: projectID,
			Raw:     []byte(ceiling.String()),
		}); err != nil {
			return nil, err
		}
	}

	// Period and behaviour are platform-wide: a per-project budget window would
	// make "what did we spend this month" mean different things on one screen.
	if request.Body.Period != nil {
		if _, err := h.settings.Write(ctx, actor, settings.WriteRequest{
			Key: "ai.spend_period", Scope: settings.ScopeGlobal,
			Raw: quoted(string(*request.Body.Period)),
		}); err != nil {
			return nil, err
		}
	}
	if request.Body.OnCeiling != nil {
		if _, err := h.settings.Write(ctx, actor, settings.WriteRequest{
			Key: "ai.on_ceiling", Scope: settings.ScopeGlobal,
			Raw: quoted(string(*request.Body.OnCeiling)),
		}); err != nil {
			return nil, err
		}
	}

	budget, err := h.budget(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return api.UpdateAIBudget200JSONResponse(budget), nil
}

func (h *Handler) budget(ctx context.Context, projectID *uuid.UUID) (api.AIBudget, error) {
	target := settings.Target{ProjectID: projectID}

	value, err := h.settings.Resolve(ctx, "ai.spend_ceiling_usd", target)
	if err != nil {
		return api.AIBudget{}, err
	}
	ceiling, err := decimalFromSetting(value)
	if err != nil {
		return api.AIBudget{}, err
	}

	period, err := h.settings.String(ctx, "ai.spend_period", settings.Target{})
	if err != nil {
		return api.AIBudget{}, err
	}
	behaviour, err := h.settings.String(ctx, "ai.on_ceiling", settings.Target{})
	if err != nil {
		return api.AIBudget{}, err
	}

	start := periodStart(period)
	spent, err := h.gateway.SpendSince(ctx, start, projectID, nil)
	if err != nil {
		return api.AIBudget{}, err
	}

	budget := api.AIBudget{
		// String rather than StringFixed: a ceiling of 0.05 is a real thing to set
		// on a local provider, and rounding it to two places in the echo would say
		// the platform stored something it did not.
		CeilingUsd:  ceiling.String(),
		Period:      api.AIBudgetPeriod(period),
		OnCeiling:   api.AIBudgetOnCeiling(behaviour),
		SpentUsd:    spent.StringFixed(4),
		PeriodStart: start,
	}
	if projectID != nil {
		budget.ProjectId.Set(*projectID)
	}
	return budget, nil
}

func (h *Handler) GetAISpend(
	ctx context.Context,
	request api.GetAISpendRequestObject,
) (api.GetAISpendResponseObject, error) {
	to := time.Now().UTC()
	if request.Params.To != nil {
		to = request.Params.To.UTC()
	}
	from := to.Add(-defaultSpendWindow)
	if request.Params.From != nil {
		from = request.Params.From.UTC()
	}
	if !from.Before(to) {
		return nil, apierr.Validation("The start of the range must be before its end.",
			map[string]any{"field": "from"})
	}

	spend, err := h.service.Spend(ctx, from, to, request.Params.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.GetAISpend200JSONResponse(spend), nil
}

// modelFields is the shared shape of the create and update bodies, so the two
// paths map prices and capabilities the same way rather than nearly the same way.
type modelFields struct {
	ModelID     *string
	DisplayName *string

	Tiers        *[]api.AITier
	Capabilities *api.AICapabilities

	PriceInput      *string
	PriceOutput     *string
	PriceCacheRead  *string
	PriceCacheWrite *string

	MaxInputTokens  *int
	MaxOutputTokens *int

	Enabled *bool
}

func toModelInput(providerID uuid.UUID, fields modelFields) (ModelInput, error) {
	input := ModelInput{ProviderID: providerID, Enabled: true}

	if fields.ModelID != nil {
		input.ModelID = *fields.ModelID
	}
	if fields.DisplayName != nil {
		input.DisplayName = *fields.DisplayName
	}
	if fields.Tiers != nil {
		for _, tier := range *fields.Tiers {
			input.Tiers = append(input.Tiers, Tier(tier))
		}
	}
	if fields.Capabilities != nil {
		capabilities := fromAPICapabilityMatrix(*fields.Capabilities)
		input.Capabilities = &capabilities
	}

	var err error
	if input.PriceInput, err = priceOr(fields.PriceInput, decimal.Zero); err != nil {
		return ModelInput{}, err
	}
	if input.PriceOutput, err = priceOr(fields.PriceOutput, decimal.Zero); err != nil {
		return ModelInput{}, err
	}
	if input.PriceCacheRead, err = optionalPrice(fields.PriceCacheRead, nil); err != nil {
		return ModelInput{}, err
	}
	if input.PriceCacheWrite, err = optionalPrice(fields.PriceCacheWrite, nil); err != nil {
		return ModelInput{}, err
	}

	input.MaxInputTokens = fields.MaxInputTokens
	input.MaxOutputTokens = fields.MaxOutputTokens
	if fields.Enabled != nil {
		input.Enabled = *fields.Enabled
	}
	return input, nil
}

// priceOr parses a price, or keeps the existing one.
//
// Prices cross the boundary as strings because money is decimal and a JSON number
// is a float: 0.1 does not survive that round trip, and a price list that drifts
// in the eighth decimal place is a bill nobody can reconcile.
func priceOr(raw *string, fallback decimal.Decimal) (decimal.Decimal, error) {
	if raw == nil || *raw == "" {
		return fallback, nil
	}

	price, err := decimal.NewFromString(*raw)
	if err != nil || price.IsNegative() {
		return decimal.Zero, apierr.Validation(
			"A price must be a number, and not negative.",
			map[string]any{"field": "price", "value": *raw})
	}
	return price, nil
}

func optionalPrice(raw *string, fallback *decimal.Decimal) (*decimal.Decimal, error) {
	if raw == nil || *raw == "" {
		return fallback, nil
	}

	price, err := decimal.NewFromString(*raw)
	if err != nil || price.IsNegative() {
		return nil, apierr.Validation(
			"A price must be a number, and not negative.",
			map[string]any{"field": "price", "value": *raw})
	}
	return &price, nil
}

func quoted(value string) []byte { return []byte(`"` + value + `"`) }
