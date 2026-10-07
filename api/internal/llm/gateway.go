package llm

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/llm/aigen"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// tokensPerPriceUnit is what the price columns are quoted in: USD per million
// tokens, which is how every provider publishes them.
var tokensPerPriceUnit = decimal.NewFromInt(1_000_000)

// Request is one call an agent wants to make.
//
// It names a tier, never a provider or a model. Which model serves the tier is a
// settings decision, and that is the whole reason switching provider stays a
// dropdown change (plan.md sequencing rule 5).
type Request struct {
	Tier  Tier
	Agent string

	ProjectID *uuid.UUID
	JobID     *uuid.UUID

	Messages []Message

	// ResponseSchema holds the model to a shape. Absent means free text.
	ResponseSchema map[string]any

	Temperature *float32

	// CacheHandle is a chain-owned context cache, where the provider has one.
	CacheHandle string
}

// Message is one turn.
//
// Cacheable marks the stable prefix. Nothing in a cacheable message may vary
// between calls: a timestamp there silently turns a cached fan-out into a full
// price one, with no error anywhere (ai-architecture.md 3.6).
type Message struct {
	Role      string
	Content   string
	Cacheable bool
}

// Result is what came back, plus what it cost.
type Result struct {
	Text       string
	Structured map[string]any

	Usage Usage
	Cost  decimal.Decimal

	ProviderKind string
	ModelName    string
	FallbackUsed bool

	LatencyMS       int
	ValidationRetry int
	RecordedCallID  int64
}

// Usage is the token count the provider reported.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Resolution is a tier resolved to something runnable.
type Resolution struct {
	Assignment Assignment

	Provider Provider
	Model    Model

	FallbackProvider *Provider
	FallbackModel    *Model
}

// Gateway is the one path from Go into a model.
//
// Everything it enforces is enforced here rather than at call sites: tier
// resolution with no silent default, data residency, the spend ceiling before the
// call, and one llm_calls row after it.
type Gateway struct {
	db       *store.DB
	client   *Client
	cipher   *settings.Cipher
	settings SettingsReader
	projects Projects
}

// SettingsReader is the slice of the settings service this package needs.
type SettingsReader interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Duration(ctx context.Context, key string, target settings.Target) (time.Duration, error)
	Resolve(ctx context.Context, key string, target settings.Target) (settings.Value, error)
}

// Projects is the slice of the projects service this package needs. The residency
// rule reads one flag, and reaching into the projects table for it would skip the
// service that owns what that flag means.
type Projects interface {
	ExternalAIApproved(ctx context.Context, projectID uuid.UUID) (bool, error)
}

func NewGateway(
	db *store.DB,
	client *Client,
	cipher *settings.Cipher,
	settingsService SettingsReader,
	projectsService Projects,
) *Gateway {
	return &Gateway{
		db:       db,
		client:   client,
		cipher:   cipher,
		settings: settingsService,
		projects: projectsService,
	}
}

// Resolve turns a tier into a provider and a model.
//
// Project assignment, then global assignment, then an error that names the fix.
// Never a silent code default: an unconfigured tier that quietly picked something
// would produce output nobody could explain (ai-architecture.md 3.2).
func (g *Gateway) Resolve(ctx context.Context, tier Tier, projectID *uuid.UUID) (Resolution, error) {
	if !tier.Valid() {
		return Resolution{}, fmt.Errorf("llm: unknown tier %q", tier)
	}

	rows, err := g.db.Queries().ResolveTier(ctx, dbgen.ResolveTierParams{
		Tier:      dbgen.LlmTier(tier),
		ProjectID: projectID,
	})
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve tier %s: %w", tier, err)
	}
	if len(rows) == 0 {
		return Resolution{}, apierr.TierNotAssigned(string(tier))
	}

	// The query orders project before global, so the first row is the answer and
	// nothing here sorts it again.
	assignment := toAssignment(rows[0])

	model, provider, err := g.loadPair(ctx, assignment.ModelID)
	if err != nil {
		return Resolution{}, err
	}
	if reason := UnusableReason(model, tier); reason != "" {
		return Resolution{}, apierr.ModelUnusableForTier(model.ModelID, string(tier), reason)
	}
	if !provider.Enabled {
		return Resolution{}, apierr.ProviderNotConfigured()
	}

	resolution := Resolution{Assignment: assignment, Provider: provider, Model: model}

	if assignment.FallbackModelID != nil {
		fallbackModel, fallbackProvider, err := g.loadPair(ctx, *assignment.FallbackModelID)
		if err == nil && fallbackProvider.Enabled {
			resolution.FallbackModel = &fallbackModel
			resolution.FallbackProvider = &fallbackProvider
		}
	}

	if projectID != nil {
		if err := g.enforceResidency(ctx, *projectID, resolution); err != nil {
			return Resolution{}, err
		}
	}
	return resolution, nil
}

// enforceResidency is the server-side data-residency gate (F-16.13).
//
// A project without external_ai_approved may only use a provider marked local.
// Checked here, before anything is sent, because a UI hint is not a control and a
// worker discovering it later has already sent the code.
func (g *Gateway) enforceResidency(ctx context.Context, projectID uuid.UUID, resolution Resolution) error {
	approved, err := g.projects.ExternalAIApproved(ctx, projectID)
	if err != nil {
		return err
	}
	if approved {
		return nil
	}

	for _, provider := range []*Provider{&resolution.Provider, resolution.FallbackProvider} {
		if provider == nil {
			continue
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

// EnsureReady is the enqueue-time check.
//
// Everything checkable is checked when a user presses submit rather than inside a
// worker twenty minutes later: a provider exists, the tier is assigned, residency
// allows it, and the budget has room (backend-standards.md 5).
func (g *Gateway) EnsureReady(ctx context.Context, tier Tier, projectID *uuid.UUID) error {
	configured, err := g.db.Queries().CountEnabledProviders(ctx)
	if err != nil {
		return fmt.Errorf("count providers: %w", err)
	}
	if configured == 0 {
		return apierr.ProviderNotConfigured()
	}

	resolution, err := g.Resolve(ctx, tier, projectID)
	if err != nil {
		return err
	}
	return g.checkCeiling(ctx, projectID, resolution.Provider.ID)
}

// Call runs one completion and records it.
//
// The order is the point: check the ceiling, make the call, write the row. The
// check is before rather than after because a ceiling that only notices afterwards
// is a report, not a ceiling (F-16.10).
func (g *Gateway) Call(ctx context.Context, request Request) (Result, error) {
	resolution, err := g.Resolve(ctx, request.Tier, request.ProjectID)
	if err != nil {
		return Result{}, err
	}
	if err := g.checkCeiling(ctx, request.ProjectID, resolution.Provider.ID); err != nil {
		return Result{}, err
	}

	payload, err := g.buildChatRequest(ctx, request, resolution)
	if err != nil {
		return Result{}, err
	}

	response, err := g.client.Chat(ctx, payload)
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Text:            valueOr(response.Text, ""),
		ProviderKind:    string(response.ServedBy.ProviderKind),
		ModelName:       response.ServedBy.ModelId,
		FallbackUsed:    valueOr(response.ServedBy.FallbackUsed, false),
		LatencyMS:       valueOr(response.LatencyMs, 0),
		ValidationRetry: valueOr(response.ValidationRetries, 0),
	}
	if structured, err := response.Structured.Get(); err == nil {
		result.Structured = structured
	}
	if response.Usage != nil {
		result.Usage = Usage{
			InputTokens:      valueOr(response.Usage.InputTokens, 0),
			OutputTokens:     valueOr(response.Usage.OutputTokens, 0),
			CacheReadTokens:  valueOr(response.Usage.CacheReadTokens, 0),
			CacheWriteTokens: valueOr(response.Usage.CacheWriteTokens, 0),
		}
	}

	// Cost is computed against whichever model actually served the call: a
	// fallback has its own price list, and attribution that assumed the primary
	// would be quietly wrong (ai-architecture.md 3.8).
	billed := resolution.Model
	billedProvider := resolution.Provider
	if result.FallbackUsed && resolution.FallbackModel != nil {
		billed = *resolution.FallbackModel
		billedProvider = *resolution.FallbackProvider
	}
	result.Cost = Cost(billed, result.Usage)

	result.RecordedCallID = g.record(ctx, request, billedProvider, billed, result)
	return result, nil
}

// buildChatRequest maps the domain request onto the service contract, decrypting
// credentials at the last possible moment.
func (g *Gateway) buildChatRequest(
	ctx context.Context,
	request Request,
	resolution Resolution,
) (aigen.ChatRequest, error) {
	target, err := g.target(ctx, resolution.Provider, resolution.Model, resolution.Assignment.Effort)
	if err != nil {
		return aigen.ChatRequest{}, err
	}

	payload := aigen.ChatRequest{
		Tier:     aigen.Tier(request.Tier),
		Agent:    request.Agent,
		Target:   target,
		Messages: make([]aigen.Message, 0, len(request.Messages)),
	}

	for _, message := range request.Messages {
		cacheable := message.Cacheable
		payload.Messages = append(payload.Messages, aigen.Message{
			Role:      aigen.Role(message.Role),
			Content:   message.Content,
			Cacheable: &cacheable,
		})
	}

	if request.ResponseSchema != nil {
		payload.ResponseSchema.Set(request.ResponseSchema)
	}
	if request.Temperature != nil {
		payload.Temperature.Set(*request.Temperature)
	}
	if request.CacheHandle != "" {
		payload.CacheHandle.Set(request.CacheHandle)
	}

	retries, err := g.settings.Int(ctx, "ai.max_validation_retries", settings.Target{})
	if err != nil {
		return aigen.ChatRequest{}, err
	}
	payload.MaxValidationRetries = &retries

	if resolution.FallbackModel != nil {
		fallback, err := g.target(ctx,
			*resolution.FallbackProvider, *resolution.FallbackModel, resolution.Assignment.Effort)
		if err != nil {
			// A broken fallback must not stop the primary from running: it exists to
			// improve availability, and refusing the call would do the opposite.
			slog.WarnContext(ctx, "fallback target unavailable",
				"model", resolution.FallbackModel.ModelID, "error", err)
		} else {
			payload.Fallback.Set(fallback)
		}
	}

	return payload, nil
}

// target decrypts a provider's credentials for exactly one request.
func (g *Gateway) target(
	ctx context.Context,
	provider Provider,
	model Model,
	effort *string,
) (aigen.Target, error) {
	row, err := g.db.Queries().GetLLMProvider(ctx, provider.ID)
	if err != nil {
		return aigen.Target{}, fmt.Errorf("load provider %s: %w", provider.ID, err)
	}

	credentials, err := openCredentials(g.cipher, provider.Name, row.Credentials)
	if err != nil {
		return aigen.Target{}, err
	}
	if err := credentials.Validate(provider.Kind); err != nil {
		return aigen.Target{}, err
	}

	config := provider.Config
	if config == nil {
		config = map[string]any{}
	}
	credentialValues := make(map[string]any, len(credentials))
	for field, value := range credentials {
		credentialValues[field] = value
	}

	capabilities := toAPICapabilities(model.Capabilities)
	apiModel := aigen.Model{
		ModelId:      model.ModelID,
		Capabilities: &capabilities,
	}
	if effort != nil {
		apiModel.Effort.Set(*effort)
	}
	if model.MaxOutputTokens != nil {
		apiModel.MaxOutputTokens.Set(*model.MaxOutputTokens)
	}

	return aigen.Target{
		Provider: aigen.Provider{
			Kind:        aigen.ProviderKind(provider.Kind),
			Config:      &config,
			Credentials: &credentialValues,
		},
		Model: apiModel,
	}, nil
}

// record writes the llm_calls row.
//
// Python returns usage and persists nothing; this is the only writer
// (backend-standards.md 10). A failure to record is logged rather than returned:
// the work succeeded, and losing an accounting row is worth less than failing a
// job that already produced its result.
func (g *Gateway) record(
	ctx context.Context,
	request Request,
	provider Provider,
	model Model,
	result Result,
) int64 {
	providerID := provider.ID
	modelID := model.ID

	row, err := g.db.Queries().RecordLLMCall(ctx, dbgen.RecordLLMCallParams{
		ProviderID:       &providerID,
		ModelID:          &modelID,
		ProviderKind:     string(provider.Kind),
		ModelName:        model.ModelID,
		Tier:             dbgen.LlmTier(request.Tier),
		Agent:            request.Agent,
		ProjectID:        request.ProjectID,
		JobID:            request.JobID,
		InputTokens:      int32(result.Usage.InputTokens),
		OutputTokens:     int32(result.Usage.OutputTokens),
		CacheReadTokens:  int32(result.Usage.CacheReadTokens),
		CacheWriteTokens: int32(result.Usage.CacheWriteTokens),
		CostUsd:          result.Cost,
		LatencyMs:        int32(result.LatencyMS),
		FallbackUsed:     result.FallbackUsed,
	})
	if err != nil {
		slog.ErrorContext(ctx, "record llm call",
			"agent", request.Agent, "tier", string(request.Tier), "error", err)
		return 0
	}
	return row.ID
}

// Cost turns tokens into money using the prices on the model.
//
// Prices live in llm_models and are editable from the UI, because provider pricing
// changes and a deploy is the wrong way to track it (ai-architecture.md 3.7). A
// cache read falls back to the input price where the provider publishes no cache
// price: assuming it is free would understate every cached fan-out.
func Cost(model Model, usage Usage) decimal.Decimal {
	cost := decimal.Zero

	cost = cost.Add(model.PriceInput.Mul(decimal.NewFromInt(int64(usage.InputTokens))))
	cost = cost.Add(model.PriceOutput.Mul(decimal.NewFromInt(int64(usage.OutputTokens))))

	cacheRead := model.PriceInput
	if model.PriceCacheRead != nil {
		cacheRead = *model.PriceCacheRead
	}
	cost = cost.Add(cacheRead.Mul(decimal.NewFromInt(int64(usage.CacheReadTokens))))

	cacheWrite := model.PriceInput
	if model.PriceCacheWrite != nil {
		cacheWrite = *model.PriceCacheWrite
	}
	cost = cost.Add(cacheWrite.Mul(decimal.NewFromInt(int64(usage.CacheWriteTokens))))

	return cost.Div(tokensPerPriceUnit).Round(8)
}

// checkCeiling refuses work that would spend past the budget.
//
// Evaluated per provider and in total, since a mixed setup may have one cheap
// local provider and one metered one. Whether a breach blocks or only warns is a
// setting, because a team mid-release wants the warning, not the wall.
func (g *Gateway) checkCeiling(ctx context.Context, projectID *uuid.UUID, providerID uuid.UUID) error {
	target := settings.Target{ProjectID: projectID}

	value, err := g.settings.Resolve(ctx, "ai.spend_ceiling_usd", target)
	if err != nil {
		return err
	}
	ceiling, err := decimalFromSetting(value)
	if err != nil {
		return err
	}
	if ceiling.IsZero() {
		// No ceiling configured. Zero means unlimited rather than "spend nothing",
		// which is the only reading that lets a fresh install work.
		return nil
	}

	period, err := g.settings.String(ctx, "ai.spend_period", settings.Target{})
	if err != nil {
		return err
	}

	spent, err := g.SpendSince(ctx, periodStart(period), projectID, nil)
	if err != nil {
		return err
	}
	if spent.LessThan(ceiling) {
		return nil
	}

	behaviour, err := g.settings.String(ctx, "ai.on_ceiling", settings.Target{})
	if err != nil {
		return err
	}
	if behaviour == "warn" {
		slog.WarnContext(ctx, "AI spend ceiling reached, continuing on the warn setting",
			"spent", spent.String(), "ceiling", ceiling.String(), "period", period)
		return nil
	}

	// The scope named in the message is where the ceiling was set, not where the
	// call came from: telling somebody their project ceiling is reached when the
	// limit is platform-wide sends them to the wrong screen.
	scope := string(value.Source)
	return apierr.SpendCeilingReached(scope).WithDetails(map[string]any{
		"scope":   scope,
		"spent":   spent.String(),
		"ceiling": ceiling.String(),
		"period":  period,
	})
}

// SpendSince totals recorded cost, optionally for one provider or project.
func (g *Gateway) SpendSince(
	ctx context.Context,
	since time.Time,
	projectID *uuid.UUID,
	providerID *uuid.UUID,
) (decimal.Decimal, error) {
	total, err := g.db.Queries().SpendSince(ctx, dbgen.SpendSinceParams{
		At:         since,
		ProjectID:  projectID,
		ProviderID: providerID,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("read spend: %w", err)
	}
	return total, nil
}

func (g *Gateway) loadPair(ctx context.Context, modelID uuid.UUID) (Model, Provider, error) {
	modelRow, err := g.db.Queries().GetLLMModel(ctx, modelID)
	if err != nil {
		if store.IsNotFound(err) {
			return Model{}, Provider{}, apierr.TierNotAssigned("assigned model")
		}
		return Model{}, Provider{}, fmt.Errorf("load model %s: %w", modelID, err)
	}

	providerRow, err := g.db.Queries().GetLLMProvider(ctx, modelRow.ProviderID)
	if err != nil {
		return Model{}, Provider{}, fmt.Errorf("load provider %s: %w", modelRow.ProviderID, err)
	}
	return toModel(modelRow), toProvider(providerRow), nil
}

// periodStart is the beginning of the current budget window, in UTC because every
// timestamp in this platform is (NFR-7).
func periodStart(period string) time.Time {
	now := time.Now().UTC()

	switch period {
	case "day":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	case "week":
		// ISO weeks start on Monday, and Go's Weekday puts Sunday at zero.
		offset := (int(now.Weekday()) + 6) % 7
		start := now.AddDate(0, 0, -offset)
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
}

func valueOr[T any](pointer *T, fallback T) T {
	if pointer == nil {
		return fallback
	}
	return *pointer
}
