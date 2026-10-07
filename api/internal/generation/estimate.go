package generation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// Estimate assumptions. All of them are returned with the number, because an
// estimate nobody can audit is a number nobody should trust (BE-2.12).
const (
	// charsPerToken is the usual English ratio. Providers tokenize differently and
	// a count measured on one model is not valid for another, which is exactly why
	// this is labelled an estimate rather than measured with somebody's tokenizer.
	charsPerToken = 4

	// outputPerDesignCall is what a design call typically produces: eight cases
	// with steps.
	outputPerDesignCall = 1500

	// outputPerExtractCall is larger: the extraction covers the whole
	// specification in one answer.
	outputPerExtractCall = 4000

	// instructionTokens is the per-call text below the cache boundary.
	instructionTokens = 400
)

// Estimate is the projected cost of the next generation run.
type Estimate struct {
	// Currency is always USD, stated rather than assumed because the prices are.
	ProviderName string
	ModelName    string

	// Caching is what the assigned model actually does, which is the whole reason
	// this cannot be a provider-agnostic number: the same specification differs by
	// roughly an order of magnitude between explicit caching and none
	// (ai-architecture.md 3.6).
	Caching llm.Caching

	Endpoints    int64
	Requirements int64
	Calls        int64

	PrefixTokens int64
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64

	Cost decimal.Decimal
}

// Estimator projects what a generation run will cost.
type Estimator struct {
	deps Deps
}

func NewEstimator(deps Deps) *Estimator { return &Estimator{deps: deps} }

// Estimate projects the cost of generating from what is already ingested.
//
// It uses the assigned provider's caching behaviour rather than an average,
// because averaging the two would produce a number that is wrong for both.
func (e *Estimator) Estimate(ctx context.Context, projectID uuid.UUID) (Estimate, error) {
	resolution, err := e.deps.Gateway.Resolve(ctx, llm.TierReasoning, &projectID)
	if err != nil {
		return Estimate{}, err
	}

	endpoints, err := e.deps.Ingest.Endpoints(ctx, projectID, nil)
	if err != nil {
		return Estimate{}, err
	}
	if len(endpoints) == 0 {
		return Estimate{}, apierr.NoEndpointsIngested()
	}

	requirementCount, err := e.deps.Requirements.Count(ctx, projectID)
	if err != nil {
		return Estimate{}, err
	}
	if requirementCount == 0 {
		// Nothing extracted yet, so the design fan-out is projected from the
		// endpoints instead. Two or three requirements per endpoint is what a
		// specification of this shape usually yields.
		requirementCount = int64(len(endpoints)) * 3
	}

	prefix, err := prefixTokens(endpoints)
	if err != nil {
		return Estimate{}, err
	}

	estimate := Estimate{
		ProviderName: resolution.Provider.Name,
		ModelName:    resolution.Model.ModelID,
		Caching:      resolution.Model.Capabilities.PromptCaching,
		Endpoints:    int64(len(endpoints)),
		Requirements: requirementCount,
		// One extraction plus one design call per requirement.
		Calls:        requirementCount + 1,
		PrefixTokens: prefix,
	}

	switch estimate.Caching {
	case llm.CachingExplicit, llm.CachingAutomatic:
		// The first call pays for the prefix; the rest read it back at the cache
		// price, which is roughly a tenth of input.
		estimate.InputTokens = prefix + estimate.Calls*instructionTokens
		estimate.CachedTokens = prefix * (estimate.Calls - 1)
	default:
		// No caching: every call pays full price for the whole prefix. This is the
		// order-of-magnitude difference the estimate exists to make visible.
		estimate.InputTokens = prefix*estimate.Calls + estimate.Calls*instructionTokens
	}

	estimate.OutputTokens = outputPerExtractCall + (estimate.Calls-1)*outputPerDesignCall

	estimate.Cost = llm.Cost(resolution.Model, llm.Usage{
		InputTokens:     int(estimate.InputTokens),
		OutputTokens:    int(estimate.OutputTokens),
		CacheReadTokens: int(estimate.CachedTokens),
	})

	return estimate, nil
}

// prefixTokens measures the cached prefix by rendering exactly what the agents
// send, rather than guessing from the endpoint count.
func prefixTokens(endpoints []ingest.Endpoint) (int64, error) {
	encoded, err := json.Marshal(promptDocument(ingest.Document{Endpoints: endpoints}))
	if err != nil {
		return 0, fmt.Errorf("render prompt for estimate: %w", err)
	}
	return int64(len(encoded) / charsPerToken), nil
}
