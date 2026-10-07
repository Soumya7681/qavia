package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/llm/aigen"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Service is the AI settings surface: providers, models, tier assignments, and
// the probe that corrects what a model claims about itself.
//
// It is admin-only at the route group, so nothing here re-checks a role.
// Authorisation belongs in one layer (backend-standards.md 11).
type Service struct {
	db       *store.DB
	cipher   *settings.Cipher
	gateway  *Gateway
	client   *Client
	recorder *audit.Recorder
}

func NewService(
	db *store.DB,
	cipher *settings.Cipher,
	gateway *Gateway,
	client *Client,
	recorder *audit.Recorder,
) *Service {
	return &Service{db: db, cipher: cipher, gateway: gateway, client: client, recorder: recorder}
}

// CreateProviderInput is a new provider.
type CreateProviderInput struct {
	Name        string
	Kind        Kind
	Config      map[string]any
	Credentials Credentials
	Residency   Residency
	Enabled     bool
	MakeDefault bool
}

// CreateProvider stores a provider and its credentials.
func (s *Service) CreateProvider(
	ctx context.Context,
	actor httpx.Principal,
	input CreateProviderInput,
) (Provider, error) {
	name := strings.TrimSpace(input.Name)
	switch {
	case name == "":
		return Provider{}, apierr.Validation("Give the provider a name.",
			map[string]any{"field": "name"})
	case !input.Kind.Valid():
		return Provider{}, apierr.Validation(
			fmt.Sprintf("%q is not a provider kind this platform supports.", input.Kind),
			map[string]any{"field": "kind"})
	}

	if err := input.Credentials.Validate(input.Kind); err != nil {
		return Provider{}, err
	}

	residency := input.Residency
	if residency == "" {
		residency = DefaultResidency(input.Kind)
	}
	if !residency.Valid() {
		return Provider{}, apierr.Validation("Choose a valid data residency.",
			map[string]any{"field": "dataResidency"})
	}

	sealed, err := input.Credentials.seal(s.cipher, name)
	if err != nil {
		return Provider{}, err
	}
	config, err := encodeConfig(input.Config)
	if err != nil {
		return Provider{}, err
	}

	row, err := s.db.Queries().CreateLLMProvider(ctx, dbgen.CreateLLMProviderParams{
		Name:          name,
		Kind:          dbgen.LlmProviderKind(input.Kind),
		Config:        config,
		Credentials:   sealed,
		DataResidency: dbgen.LlmDataResidency(residency),
		IsEnabled:     input.Enabled,
		CreatedBy:     &actor.UserID,
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return Provider{}, apierr.Conflict("A provider with that name already exists.")
		}
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}

	if input.MakeDefault {
		if err := s.setDefault(ctx, row.ID); err != nil {
			return Provider{}, err
		}
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    name,
		Detail: map[string]any{
			"kind": string(input.Kind), "residency": string(residency),
			"integration": "ai_provider",
		},
	})

	return s.GetProvider(ctx, row.ID)
}

// UpdateProviderInput changes a provider. Credentials are separate, because a form
// submitted without the secret field must not blank the stored one.
type UpdateProviderInput struct {
	Name        string
	Config      map[string]any
	Residency   Residency
	Enabled     bool
	MakeDefault bool

	// Credentials replaces the stored set when non-empty, and is ignored when
	// empty. That is what makes the read path a Replace action rather than an edit
	// (F-1.8).
	Credentials Credentials
}

func (s *Service) UpdateProvider(
	ctx context.Context,
	actor httpx.Principal,
	id uuid.UUID,
	input UpdateProviderInput,
) (Provider, error) {
	existing, err := s.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = existing.Name
	}
	residency := input.Residency
	if residency == "" {
		residency = existing.Residency
	}
	if !residency.Valid() {
		return Provider{}, apierr.Validation("Choose a valid data residency.",
			map[string]any{"field": "dataResidency"})
	}

	config, err := encodeConfig(input.Config)
	if err != nil {
		return Provider{}, err
	}

	if _, err := s.db.Queries().UpdateLLMProvider(ctx, dbgen.UpdateLLMProviderParams{
		ID:            id,
		Name:          name,
		Config:        config,
		DataResidency: dbgen.LlmDataResidency(residency),
		IsEnabled:     input.Enabled,
	}); err != nil {
		if store.IsUniqueViolation(err) {
			return Provider{}, apierr.Conflict("A provider with that name already exists.")
		}
		return Provider{}, fmt.Errorf("update provider %s: %w", id, err)
	}

	if len(input.Credentials) > 0 {
		if err := input.Credentials.Validate(existing.Kind); err != nil {
			return Provider{}, err
		}
		sealed, err := input.Credentials.seal(s.cipher, name)
		if err != nil {
			return Provider{}, err
		}
		if err := s.db.Queries().SetLLMProviderCredentials(ctx,
			dbgen.SetLLMProviderCredentialsParams{ID: id, Credentials: sealed}); err != nil {
			return Provider{}, fmt.Errorf("update provider credentials: %w", err)
		}

		// A credential change is a secret rotation, and rotations are audited
		// whatever else changed in the same request.
		s.recorder.Record(ctx, audit.Entry{
			Action:     audit.ActionSecretRotated,
			ActorID:    &actor.UserID,
			ActorEmail: actor.Email,
			Subject:    name,
			Detail:     map[string]any{"integration": "ai_provider"},
		})
	}

	if input.MakeDefault && !existing.Default {
		if err := s.setDefault(ctx, id); err != nil {
			return Provider{}, err
		}
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    name,
		Detail:     map[string]any{"integration": "ai_provider", "enabled": input.Enabled},
	})

	return s.GetProvider(ctx, id)
}

// setDefault moves the default flag. Two statements in one transaction, because
// the partial unique index refuses two defaults and the clear has to land first.
func (s *Service) setDefault(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q *dbgen.Queries) error {
		if err := q.ClearDefaultProvider(ctx); err != nil {
			return fmt.Errorf("clear default provider: %w", err)
		}
		if err := q.SetDefaultProvider(ctx, id); err != nil {
			return fmt.Errorf("set default provider: %w", err)
		}
		return nil
	})
}

func (s *Service) GetProvider(ctx context.Context, id uuid.UUID) (Provider, error) {
	row, err := s.db.Queries().GetLLMProvider(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Provider{}, apierr.NotFound("AI provider")
		}
		return Provider{}, fmt.Errorf("load provider %s: %w", id, err)
	}
	return toProvider(row), nil
}

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.Queries().ListLLMProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}

	providers := make([]Provider, 0, len(rows))
	for _, row := range rows {
		providers = append(providers, toProvider(row))
	}
	return providers, nil
}

// DeleteProvider refuses while a tier assignment depends on it.
//
// The refusal names how many assignments are in the way rather than cascading: a
// delete that silently unassigns the reasoning tier turns into "why did every job
// start failing" a week later (BE-1.13).
func (s *Service) DeleteProvider(ctx context.Context, actor httpx.Principal, id uuid.UUID) error {
	provider, err := s.GetProvider(ctx, id)
	if err != nil {
		return err
	}

	using, err := s.db.Queries().CountAssignmentsUsingProvider(ctx, id)
	if err != nil {
		return fmt.Errorf("count assignments using provider: %w", err)
	}
	if using > 0 {
		return apierr.ProviderInUse(int(using))
	}

	if _, err := s.db.Queries().DeleteLLMProvider(ctx, id); err != nil {
		return fmt.Errorf("delete provider %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    provider.Name,
		Detail:     map[string]any{"integration": "ai_provider", "deleted": true},
	})
	return nil
}

// ModelInput is a model on a provider.
type ModelInput struct {
	ProviderID uuid.UUID

	ModelID     string
	DisplayName string

	Tiers        []Tier
	Capabilities *Capabilities

	PriceInput      decimal.Decimal
	PriceOutput     decimal.Decimal
	PriceCacheRead  *decimal.Decimal
	PriceCacheWrite *decimal.Decimal

	MaxInputTokens  *int
	MaxOutputTokens *int

	Enabled bool
}

// CreateModel adds a model, seeding capabilities from the shipped defaults where
// the caller did not supply them (F-16.4). The probe corrects them later.
func (s *Service) CreateModel(ctx context.Context, actor httpx.Principal, input ModelInput) (Model, error) {
	provider, err := s.GetProvider(ctx, input.ProviderID)
	if err != nil {
		return Model{}, err
	}
	if strings.TrimSpace(input.ModelID) == "" {
		return Model{}, apierr.Validation("Give the model its provider-side identifier.",
			map[string]any{"field": "modelId"})
	}
	if err := validateTiers(input.Tiers); err != nil {
		return Model{}, err
	}

	capabilities := DefaultCapabilities(provider.Kind)
	if input.Capabilities != nil {
		capabilities = *input.Capabilities
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return Model{}, fmt.Errorf("encode capabilities: %w", err)
	}

	row, err := s.db.Queries().CreateLLMModel(ctx, dbgen.CreateLLMModelParams{
		ProviderID:      input.ProviderID,
		ModelID:         strings.TrimSpace(input.ModelID),
		DisplayName:     strings.TrimSpace(input.DisplayName),
		Tiers:           tierStrings(input.Tiers),
		Capabilities:    encoded,
		PriceInput:      input.PriceInput,
		PriceOutput:     input.PriceOutput,
		PriceCacheRead:  input.PriceCacheRead,
		PriceCacheWrite: input.PriceCacheWrite,
		MaxInputTokens:  int32From(input.MaxInputTokens),
		MaxOutputTokens: int32From(input.MaxOutputTokens),
		IsEnabled:       input.Enabled,
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			return Model{}, apierr.Conflict("That model is already configured on this provider.")
		}
		return Model{}, fmt.Errorf("create model: %w", err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    row.ModelID,
		Detail:     map[string]any{"integration": "ai_model", "provider": provider.Name},
	})
	return toModel(row), nil
}

func (s *Service) UpdateModel(
	ctx context.Context,
	actor httpx.Principal,
	id uuid.UUID,
	input ModelInput,
) (Model, error) {
	existing, err := s.GetModel(ctx, id)
	if err != nil {
		return Model{}, err
	}
	if err := validateTiers(input.Tiers); err != nil {
		return Model{}, err
	}

	capabilities := existing.Capabilities
	if input.Capabilities != nil {
		capabilities = *input.Capabilities
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		return Model{}, fmt.Errorf("encode capabilities: %w", err)
	}

	row, err := s.db.Queries().UpdateLLMModel(ctx, dbgen.UpdateLLMModelParams{
		ID:              id,
		DisplayName:     strings.TrimSpace(input.DisplayName),
		Tiers:           tierStrings(input.Tiers),
		Capabilities:    encoded,
		PriceInput:      input.PriceInput,
		PriceOutput:     input.PriceOutput,
		PriceCacheRead:  input.PriceCacheRead,
		PriceCacheWrite: input.PriceCacheWrite,
		MaxInputTokens:  int32From(input.MaxInputTokens),
		MaxOutputTokens: int32From(input.MaxOutputTokens),
		IsEnabled:       input.Enabled,
	})
	if err != nil {
		return Model{}, fmt.Errorf("update model %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    row.ModelID,
		Detail:     map[string]any{"integration": "ai_model"},
	})
	return toModel(row), nil
}

func (s *Service) GetModel(ctx context.Context, id uuid.UUID) (Model, error) {
	row, err := s.db.Queries().GetLLMModel(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Model{}, apierr.NotFound("AI model")
		}
		return Model{}, fmt.Errorf("load model %s: %w", id, err)
	}
	return toModel(row), nil
}

func (s *Service) ListModels(ctx context.Context, providerID *uuid.UUID) ([]Model, error) {
	rows, err := s.db.Queries().ListLLMModels(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}

	models := make([]Model, 0, len(rows))
	for _, row := range rows {
		models = append(models, toModel(row))
	}
	return models, nil
}

// DeleteModel refuses while a tier points at it, for the same reason a provider
// delete does.
func (s *Service) DeleteModel(ctx context.Context, actor httpx.Principal, id uuid.UUID) error {
	model, err := s.GetModel(ctx, id)
	if err != nil {
		return err
	}

	using, err := s.db.Queries().CountAssignmentsUsingModel(ctx, id)
	if err != nil {
		return fmt.Errorf("count assignments using model: %w", err)
	}
	if using > 0 {
		return apierr.ProviderInUse(int(using))
	}

	if _, err := s.db.Queries().DeleteLLMModel(ctx, id); err != nil {
		return fmt.Errorf("delete model %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    model.ModelID,
		Detail:     map[string]any{"integration": "ai_model", "deleted": true},
	})
	return nil
}

// AssignInput points a tier at a model.
type AssignInput struct {
	Tier      Tier
	ProjectID *uuid.UUID

	ModelID         uuid.UUID
	FallbackModelID *uuid.UUID
	Effort          *string
}

// Assign validates that the model can actually serve the tier before writing.
//
// A tier pointing at a model that cannot do the job is a runtime failure waiting
// to happen, and the message says which capability is missing so an admin can fix
// it rather than guess (BE-1.13).
func (s *Service) Assign(ctx context.Context, actor httpx.Principal, input AssignInput) (Assignment, error) {
	if !input.Tier.Valid() {
		return Assignment{}, apierr.Validation("Choose a valid tier.",
			map[string]any{"field": "tier"})
	}

	model, err := s.GetModel(ctx, input.ModelID)
	if err != nil {
		return Assignment{}, err
	}
	if reason := UnusableReason(model, input.Tier); reason != "" {
		return Assignment{}, apierr.ModelUnusableForTier(model.ModelID, string(input.Tier), reason)
	}

	if input.FallbackModelID != nil {
		fallback, err := s.GetModel(ctx, *input.FallbackModelID)
		if err != nil {
			return Assignment{}, err
		}
		if reason := UnusableReason(fallback, input.Tier); reason != "" {
			return Assignment{}, apierr.ModelUnusableForTier(
				fallback.ModelID, string(input.Tier), reason)
		}
	}

	// Residency is checked at assignment as well as at call time. Both, because
	// assignment is where an admin can still fix it, and call time is where the
	// guarantee has to hold (F-16.13).
	if input.ProjectID != nil {
		if err := s.checkAssignmentResidency(ctx, *input.ProjectID, model, input.FallbackModelID); err != nil {
			return Assignment{}, err
		}
	}

	var row dbgen.TierAssignment
	if input.ProjectID == nil {
		row, err = s.db.Queries().UpsertGlobalTierAssignment(ctx, dbgen.UpsertGlobalTierAssignmentParams{
			Tier:            dbgen.LlmTier(input.Tier),
			ModelID:         input.ModelID,
			FallbackModelID: input.FallbackModelID,
			Effort:          input.Effort,
			UpdatedBy:       &actor.UserID,
		})
	} else {
		row, err = s.db.Queries().UpsertProjectTierAssignment(ctx, dbgen.UpsertProjectTierAssignmentParams{
			ScopeID:         input.ProjectID,
			Tier:            dbgen.LlmTier(input.Tier),
			ModelID:         input.ModelID,
			FallbackModelID: input.FallbackModelID,
			Effort:          input.Effort,
			UpdatedBy:       &actor.UserID,
		})
	}
	if err != nil {
		return Assignment{}, fmt.Errorf("assign tier %s: %w", input.Tier, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionSettingChanged,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    "ai.tier." + string(input.Tier),
		ProjectID:  input.ProjectID,
		Detail:     map[string]any{"model": model.ModelID},
	})
	return toAssignment(row), nil
}

func (s *Service) checkAssignmentResidency(
	ctx context.Context,
	projectID uuid.UUID,
	model Model,
	fallbackID *uuid.UUID,
) error {
	approved, err := s.gateway.projects.ExternalAIApproved(ctx, projectID)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}

	ids := []uuid.UUID{model.ProviderID}
	if fallbackID != nil {
		fallback, err := s.GetModel(ctx, *fallbackID)
		if err != nil {
			return err
		}
		ids = append(ids, fallback.ProviderID)
	}

	for _, id := range ids {
		provider, err := s.GetProvider(ctx, id)
		if err != nil {
			return err
		}
		if provider.Residency != ResidencyLocal {
			return apierr.ExternalAINotApproved().WithDetails(map[string]any{
				"provider":  provider.Name,
				"residency": string(provider.Residency),
				"projectId": projectID.String(),
			})
		}
	}
	return nil
}

func (s *Service) ListAssignments(ctx context.Context, projectID *uuid.UUID) ([]Assignment, error) {
	rows, err := s.db.Queries().ListTierAssignments(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tier assignments: %w", err)
	}

	assignments := make([]Assignment, 0, len(rows))
	for _, row := range rows {
		assignments = append(assignments, toAssignment(row))
	}
	return assignments, nil
}

func (s *Service) Unassign(ctx context.Context, actor httpx.Principal, id uuid.UUID) error {
	if _, err := s.db.Queries().DeleteTierAssignment(ctx, id); err != nil {
		return fmt.Errorf("delete tier assignment %s: %w", id, err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionSettingChanged,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    "ai.tier.unassigned",
	})
	return nil
}

// TestProvider probes a model and writes back what actually worked.
//
// Detected capability overwrites declared capability (F-16.5): the table shipped
// with the platform is a guess, and this is a measurement. The provider's health
// is recorded at the same time, so the settings screen can show it without another
// round of calls.
func (s *Service) TestProvider(
	ctx context.Context,
	actor httpx.Principal,
	providerID uuid.UUID,
	modelID *uuid.UUID,
) (ProbeOutcome, error) {
	provider, err := s.GetProvider(ctx, providerID)
	if err != nil {
		return ProbeOutcome{}, err
	}

	model, err := s.probeModel(ctx, provider, modelID)
	if err != nil {
		return ProbeOutcome{}, err
	}

	target, err := s.gateway.target(ctx, provider, model, nil)
	if err != nil {
		s.markHealth(ctx, providerID, HealthFailing, err.Error())
		return ProbeOutcome{}, err
	}

	result, err := s.client.Probe(ctx, aigen.ProbeRequest{Target: target})
	if err != nil {
		s.markHealth(ctx, providerID, HealthFailing, "The AI service could not be reached.")
		return ProbeOutcome{}, err
	}

	outcome := ProbeOutcome{
		ProviderID: providerID,
		ModelID:    model.ID,
		Reachable:  result.Reachable,
		Detail:     valueOr(result.Detail, ""),
		LatencyMS:  valueOr(result.LatencyMs, 0),
	}

	detected := model.Capabilities
	detected.ToolUse = valueOr(result.ToolUse, false)
	detected.Vision = valueOr(result.Vision, false)
	if !valueOr(result.StructuredOutput, false) {
		// The model could not be held to a schema, so the gateway must add the
		// repair pass rather than trusting the provider to enforce one.
		detected.StructuredOutput = StructuredPromptOnly
	}
	outcome.Capabilities = detected

	if result.Reachable {
		encoded, err := json.Marshal(detected)
		if err != nil {
			return ProbeOutcome{}, fmt.Errorf("encode detected capabilities: %w", err)
		}
		if err := s.db.Queries().SetLLMModelCapabilities(ctx,
			dbgen.SetLLMModelCapabilitiesParams{ID: model.ID, Capabilities: encoded}); err != nil {
			return ProbeOutcome{}, fmt.Errorf("write detected capabilities: %w", err)
		}
		s.markHealth(ctx, providerID, HealthOK, outcome.Detail)
	} else {
		s.markHealth(ctx, providerID, HealthFailing, outcome.Detail)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionIntegrationSaved,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    provider.Name,
		Detail: map[string]any{
			"integration": "ai_provider", "probe": true, "reachable": result.Reachable,
		},
	})
	return outcome, nil
}

// ProbeOutcome is what a Test connection found.
type ProbeOutcome struct {
	ProviderID uuid.UUID
	ModelID    uuid.UUID

	Reachable    bool
	Capabilities Capabilities

	Detail    string
	LatencyMS int
}

// probeModel picks what to probe: the named model, or the first enabled one.
func (s *Service) probeModel(ctx context.Context, provider Provider, modelID *uuid.UUID) (Model, error) {
	if modelID != nil {
		model, err := s.GetModel(ctx, *modelID)
		if err != nil {
			return Model{}, err
		}
		if model.ProviderID != provider.ID {
			return Model{}, apierr.Validation("That model belongs to a different provider.",
				map[string]any{"field": "modelId"})
		}
		return model, nil
	}

	models, err := s.ListModels(ctx, &provider.ID)
	if err != nil {
		return Model{}, err
	}
	for _, model := range models {
		if model.Enabled {
			return model, nil
		}
	}
	return Model{}, apierr.Validation(
		"Add a model to this provider before testing the connection.", nil)
}

func (s *Service) markHealth(ctx context.Context, providerID uuid.UUID, status Health, detail string) {
	if err := s.db.Queries().SetLLMProviderHealth(ctx, dbgen.SetLLMProviderHealthParams{
		ID:           providerID,
		HealthStatus: dbgen.LlmHealthStatus(status),
		HealthDetail: truncate(detail, 500),
	}); err != nil {
		// The probe result is already the answer to the caller's question; failing
		// to cache it on the row is not worth losing that answer over.
		return
	}
}

// AnyConfigured reports whether any provider is usable, for the setup wizard.
func (s *Service) AnyConfigured(ctx context.Context) (bool, error) {
	count, err := s.db.Queries().CountEnabledProviders(ctx)
	if err != nil {
		return false, fmt.Errorf("count providers: %w", err)
	}
	return count > 0, nil
}

func validateTiers(tiers []Tier) error {
	for _, tier := range tiers {
		if !tier.Valid() {
			return apierr.Validation(
				fmt.Sprintf("%q is not a tier this platform knows.", tier),
				map[string]any{"field": "tiers"})
		}
	}
	return nil
}

func tierStrings(tiers []Tier) []string {
	out := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		out = append(out, string(tier))
	}
	return out
}

func encodeConfig(config map[string]any) ([]byte, error) {
	if config == nil {
		config = map[string]any{}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode provider config: %w", err)
	}
	return encoded, nil
}

func int32From(value *int) *int32 {
	if value == nil {
		return nil
	}
	converted := int32(*value)
	return &converted
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}
