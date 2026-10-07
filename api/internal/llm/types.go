package llm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/llm/aigen"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Kind is a provider kind (ai-architecture.md 3.1).
//
// The last one is the row that makes "any other provider" real: one adapter
// reaches Ollama, vLLM, LiteLLM, OpenRouter, Together, Groq, Fireworks, and
// DeepSeek, so adding one of those is a URL and a key rather than a release.
type Kind string

const (
	KindAnthropic        Kind = "anthropic"
	KindBedrock          Kind = "bedrock"
	KindVertex           Kind = "vertex"
	KindOpenAI           Kind = "openai"
	KindAzureOpenAI      Kind = "azure-openai"
	KindGemini           Kind = "gemini"
	KindOpenAICompatible Kind = "openai-compatible"
)

// AllKinds is every kind, in the order a form should offer them.
var AllKinds = []Kind{
	KindAnthropic, KindBedrock, KindVertex, KindOpenAI,
	KindAzureOpenAI, KindGemini, KindOpenAICompatible,
}

func (k Kind) Valid() bool {
	for _, candidate := range AllKinds {
		if candidate == k {
			return true
		}
	}
	return false
}

// Residency is where a provider processes data.
//
// This is the server-side half of the NDA problem (requirements.md 8.1): a project
// without external_ai_approved may only be assigned a provider marked local.
type Residency string

const (
	ResidencyLocal    Residency = "local"
	ResidencyRegional Residency = "regional"
	ResidencyExternal Residency = "external"
)

func (r Residency) Valid() bool {
	switch r {
	case ResidencyLocal, ResidencyRegional, ResidencyExternal:
		return true
	default:
		return false
	}
}

// Tier is what a call is for, rather than which model runs it.
type Tier string

const (
	TierReasoning Tier = "reasoning"
	TierCode      Tier = "code"
	TierCheap     Tier = "cheap"
	TierVision    Tier = "vision"
)

// AllTiers is every tier, in the order the settings screen lists them.
var AllTiers = []Tier{TierReasoning, TierCode, TierCheap, TierVision}

func (t Tier) Valid() bool {
	for _, candidate := range AllTiers {
		if candidate == t {
			return true
		}
	}
	return false
}

// Health is what the last probe found.
type Health string

const (
	HealthUnknown Health = "unknown"
	HealthOK      Health = "ok"
	HealthFailing Health = "failing"
)

// StructuredOutput is how well a model can be held to a schema.
type StructuredOutput string

const (
	StructuredNative     StructuredOutput = "native"
	StructuredToolBased  StructuredOutput = "tool_based"
	StructuredPromptOnly StructuredOutput = "prompt_only"
)

// Caching is how a provider caches a prompt prefix, which is the difference
// between a fan-out that is affordable and one that is not.
type Caching string

const (
	CachingExplicit  Caching = "explicit"
	CachingAutomatic Caching = "automatic"
	CachingNone      Caching = "none"
)

// Capabilities is what a model can actually do (ai-architecture.md 3.4).
//
// A named type rather than a map, because every jsonb column maps to one
// (backend-standards.md 9), and because features are gated on these fields: a
// wrong shape here disables the wrong things.
type Capabilities struct {
	ToolUse           bool             `json:"tool_use"`
	Vision            bool             `json:"vision"`
	StructuredOutput  StructuredOutput `json:"structured_output"`
	PromptCaching     Caching          `json:"prompt_caching"`
	EffortControl     bool             `json:"effort_control"`
	MaxToolIterations int              `json:"max_tool_iterations"`
}

// Provider is one configured provider.
//
// Credentials are deliberately absent: they are read only by the gateway, at the
// point of use, and a domain type that carried them would eventually be logged.
type Provider struct {
	ID   uuid.UUID
	Name string
	Kind Kind

	// Config holds the non-secret settings: base URL, region, project, API
	// version, deployment.
	Config map[string]any

	Residency Residency

	Enabled bool
	Default bool

	CredentialsSet   bool
	CredentialsHint  string
	CredentialsSetAt *time.Time
	Health           Health
	HealthDetail     string
	HealthCheckedAt  *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Model is one model on a provider, with its prices and capabilities.
type Model struct {
	ID         uuid.UUID
	ProviderID uuid.UUID

	ModelID     string
	DisplayName string

	Tiers        []Tier
	Capabilities Capabilities

	PriceInput      decimal.Decimal
	PriceOutput     decimal.Decimal
	PriceCacheRead  *decimal.Decimal
	PriceCacheWrite *decimal.Decimal

	MaxInputTokens  *int
	MaxOutputTokens *int

	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ServesTier reports whether the model is offered for a tier.
func (m Model) ServesTier(tier Tier) bool {
	for _, candidate := range m.Tiers {
		if candidate == tier {
			return true
		}
	}
	return false
}

// Assignment is a tier pointing at a model, at global or project scope.
type Assignment struct {
	ID    uuid.UUID
	Scope string

	// ProjectID is nil for the global assignment, which is the one everything
	// falls back to.
	ProjectID *uuid.UUID

	Tier            Tier
	ModelID         uuid.UUID
	FallbackModelID *uuid.UUID

	// Effort is provider-specific and nil where the provider has no such control.
	Effort *string

	UpdatedAt time.Time
}

// Call is one recorded provider call.
type Call struct {
	ID int64

	ProviderID   *uuid.UUID
	ModelID      *uuid.UUID
	ProviderKind string
	ModelName    string

	Tier  Tier
	Agent string

	ProjectID *uuid.UUID
	JobID     *uuid.UUID

	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int

	CostUSD   decimal.Decimal
	LatencyMS int

	// FallbackUsed matters to attribution: the fallback has its own price list, so
	// a report that assumed the primary served the call would be wrong.
	FallbackUsed bool

	At time.Time
}

func toProvider(row dbgen.LlmProvider) Provider {
	provider := Provider{
		ID:              row.ID,
		Name:            row.Name,
		Kind:            Kind(row.Kind),
		Residency:       Residency(row.DataResidency),
		Enabled:         row.IsEnabled,
		Default:         row.IsDefault,
		Health:          Health(row.HealthStatus),
		HealthDetail:    row.HealthDetail,
		HealthCheckedAt: row.HealthCheckedAt,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}

	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &provider.Config); err != nil {
			// Not worth refusing the read over: the config is non-secret display
			// data, and an empty map reads as "nothing configured" rather than
			// breaking the settings screen an admin needs to fix it from.
			slog.Warn("decode provider config", "provider_id", row.ID, "error", err)
		}
	}

	stored, err := decodeCredentials(row.Credentials)
	if err == nil && len(stored) > 0 {
		provider.CredentialsSet = true
		provider.CredentialsHint = hintOf(stored)
	}
	return provider
}

func toModel(row dbgen.LlmModel) Model {
	model := Model{
		ID:          row.ID,
		ProviderID:  row.ProviderID,
		ModelID:     row.ModelID,
		DisplayName: row.DisplayName,
		PriceInput:  row.PriceInput,
		PriceOutput: row.PriceOutput,
		Enabled:     row.IsEnabled,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	for _, tier := range row.Tiers {
		model.Tiers = append(model.Tiers, Tier(tier))
	}
	if len(row.Capabilities) > 0 {
		if err := json.Unmarshal(row.Capabilities, &model.Capabilities); err != nil {
			// The zero matrix claims nothing, which is the safe direction: features
			// gate on these, so an unreadable row disables rather than enables.
			slog.Warn("decode model capabilities", "model_id", row.ID, "error", err)
		}
	}

	model.PriceCacheRead = row.PriceCacheRead
	model.PriceCacheWrite = row.PriceCacheWrite
	model.MaxInputTokens = intFrom(row.MaxInputTokens)
	model.MaxOutputTokens = intFrom(row.MaxOutputTokens)
	return model
}

func toAssignment(row dbgen.TierAssignment) Assignment {
	return Assignment{
		ID:              row.ID,
		Scope:           string(row.Scope),
		ProjectID:       row.ScopeID,
		Tier:            Tier(row.Tier),
		ModelID:         row.ModelID,
		FallbackModelID: row.FallbackModelID,
		Effort:          row.Effort,
		UpdatedAt:       row.UpdatedAt,
	}
}

func toCall(row dbgen.LlmCall) Call {
	return Call{
		ID:               row.ID,
		ProviderID:       row.ProviderID,
		ModelID:          row.ModelID,
		ProviderKind:     row.ProviderKind,
		ModelName:        row.ModelName,
		Tier:             Tier(row.Tier),
		Agent:            row.Agent,
		ProjectID:        row.ProjectID,
		JobID:            row.JobID,
		InputTokens:      int(row.InputTokens),
		OutputTokens:     int(row.OutputTokens),
		CacheReadTokens:  int(row.CacheReadTokens),
		CacheWriteTokens: int(row.CacheWriteTokens),
		CostUSD:          row.CostUsd,
		LatencyMS:        int(row.LatencyMs),
		FallbackUsed:     row.FallbackUsed,
		At:               row.At,
	}
}

func intFrom(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

// toAPICapabilities maps the capability matrix onto the service contract.
//
// Every field is sent explicitly rather than relying on the service's own
// defaults: a model whose tool use was probed as false must arrive as false, and
// an omitted field would let the far side decide it was true.
func toAPICapabilities(c Capabilities) aigen.Capabilities {
	toolUse := c.ToolUse
	vision := c.Vision
	effort := c.EffortControl
	iterations := c.MaxToolIterations
	if iterations <= 0 {
		iterations = 30
	}

	structured := aigen.StructuredOutputMode(c.StructuredOutput)
	if c.StructuredOutput == "" {
		structured = aigen.StructuredOutputMode(StructuredPromptOnly)
	}
	caching := aigen.PromptCaching(c.PromptCaching)
	if c.PromptCaching == "" {
		caching = aigen.PromptCaching(CachingNone)
	}

	return aigen.Capabilities{
		ToolUse:           &toolUse,
		Vision:            &vision,
		StructuredOutput:  &structured,
		PromptCaching:     &caching,
		EffortControl:     &effort,
		MaxToolIterations: &iterations,
	}
}

// decimalFromSetting reads a numeric setting as money.
//
// Settings store JSON numbers; money is decimal everywhere else in this package,
// and going through the string form avoids a float ever holding a price.
func decimalFromSetting(value settings.Value) (decimal.Decimal, error) {
	if len(value.Raw) == 0 {
		return decimal.Zero, nil
	}

	parsed, err := decimal.NewFromString(strings.Trim(string(value.Raw), `"`))
	if err != nil {
		return decimal.Zero, fmt.Errorf("settings: %q is not a number: %w", value.Key, err)
	}
	return parsed, nil
}
