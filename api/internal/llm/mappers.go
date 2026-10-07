package llm

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Mappers to the response types.
//
// Every response goes through one, so adding a column cannot silently change the
// API and a credential cannot leak by being on a struct somebody serialised
// (backend-standards.md 4). The credential mapper here is the sharpest example:
// it can only ever produce {isSet, updatedAt, hint}, because it has nothing else
// to work from.

func toAPIProvider(provider Provider) api.AIProvider {
	config := provider.Config
	if config == nil {
		config = map[string]any{}
	}
	detail := provider.HealthDetail

	out := api.AIProvider{
		Id:            provider.ID,
		Name:          provider.Name,
		Kind:          api.AIProviderKind(provider.Kind),
		Config:        &config,
		DataResidency: api.AIDataResidency(provider.Residency),
		Enabled:       provider.Enabled,
		IsDefault:     provider.Default,
		Health:        api.AIHealth(provider.Health),
		HealthDetail:  &detail,
		CreatedAt:     provider.CreatedAt,
		UpdatedAt:     &provider.UpdatedAt,
		Credentials: api.SecretRead{
			IsSet: provider.CredentialsSet,
		},
	}

	if provider.CredentialsHint != "" {
		hint := provider.CredentialsHint
		out.Credentials.Hint = &hint
	}
	if provider.CredentialsSet {
		// The stored envelope carries no separate timestamp, so the provider's own
		// updated_at is the honest answer to "when was this last changed".
		updated := provider.UpdatedAt
		out.Credentials.UpdatedAt = &updated
	}
	if provider.HealthCheckedAt != nil {
		out.HealthCheckedAt.Set(*provider.HealthCheckedAt)
	}
	return out
}

func toAPIModel(model Model) api.AIModel {
	display := model.DisplayName

	out := api.AIModel{
		Id:           model.ID,
		ProviderId:   model.ProviderID,
		ModelId:      model.ModelID,
		DisplayName:  &display,
		Tiers:        make([]api.AITier, 0, len(model.Tiers)),
		Capabilities: toAPICapabilityMatrix(model.Capabilities),
		PriceInput:   model.PriceInput.String(),
		PriceOutput:  model.PriceOutput.String(),
		Enabled:      model.Enabled,
	}

	for _, tier := range model.Tiers {
		out.Tiers = append(out.Tiers, api.AITier(tier))
	}
	if model.PriceCacheRead != nil {
		out.PriceCacheRead.Set(model.PriceCacheRead.String())
	}
	if model.PriceCacheWrite != nil {
		out.PriceCacheWrite.Set(model.PriceCacheWrite.String())
	}
	if model.MaxInputTokens != nil {
		out.MaxInputTokens.Set(*model.MaxInputTokens)
	}
	if model.MaxOutputTokens != nil {
		out.MaxOutputTokens.Set(*model.MaxOutputTokens)
	}
	return out
}

func toAPICapabilityMatrix(capabilities Capabilities) api.AICapabilities {
	structured := capabilities.StructuredOutput
	if structured == "" {
		structured = StructuredPromptOnly
	}
	caching := capabilities.PromptCaching
	if caching == "" {
		caching = CachingNone
	}
	iterations := capabilities.MaxToolIterations
	if iterations <= 0 {
		iterations = 30
	}

	return api.AICapabilities{
		ToolUse:           capabilities.ToolUse,
		Vision:            capabilities.Vision,
		StructuredOutput:  api.AICapabilitiesStructuredOutput(structured),
		PromptCaching:     api.AICapabilitiesPromptCaching(caching),
		EffortControl:     capabilities.EffortControl,
		MaxToolIterations: iterations,
	}
}

func fromAPICapabilityMatrix(capabilities api.AICapabilities) Capabilities {
	return Capabilities{
		ToolUse:           capabilities.ToolUse,
		Vision:            capabilities.Vision,
		StructuredOutput:  StructuredOutput(capabilities.StructuredOutput),
		PromptCaching:     Caching(capabilities.PromptCaching),
		EffortControl:     capabilities.EffortControl,
		MaxToolIterations: capabilities.MaxToolIterations,
	}
}

func toAPIAssignment(assignment Assignment) api.AITierAssignment {
	out := api.AITierAssignment{
		Id:        assignment.ID,
		Scope:     api.AITierAssignmentScope(assignment.Scope),
		Tier:      api.AITier(assignment.Tier),
		ModelId:   assignment.ModelID,
		UpdatedAt: &assignment.UpdatedAt,
	}

	if assignment.ProjectID != nil {
		out.ProjectId.Set(*assignment.ProjectID)
	}
	if assignment.FallbackModelID != nil {
		out.FallbackModelId.Set(*assignment.FallbackModelID)
	}
	if assignment.Effort != nil {
		out.Effort.Set(*assignment.Effort)
	}
	return out
}

// spendGroup is one row of an aggregate, before it knows what it is grouped by.
type spendGroup struct {
	Key             string
	Label           string
	Calls           int64
	InputTokens     int64
	OutputTokens    int64
	CacheReadTokens int64
	Cost            decimal.Decimal
}

func toAPISpendGroup(group spendGroup) api.AISpendGroup {
	return api.AISpendGroup{
		Key:             group.Key,
		Label:           group.Label,
		Calls:           group.Calls,
		InputTokens:     group.InputTokens,
		OutputTokens:    group.OutputTokens,
		CacheReadTokens: group.CacheReadTokens,
		CostUsd:         group.Cost.StringFixed(6),
	}
}

// nilUUID is what the aggregates report for a call whose provider or project row
// has since been deleted. Spend history outlives its subject on purpose: a bill
// that forgets what it was for is not a bill.
var nilUUID = uuid.Nil

// spendWindow is the range an aggregate covers, carried together so the two
// halves cannot drift apart between queries.
type spendWindow struct {
	From time.Time
	To   time.Time
}
