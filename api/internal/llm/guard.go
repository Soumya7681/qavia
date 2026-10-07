package llm

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
)

// chainTiers says which tiers a chain's stages need, for the enqueue-time check.
//
// Every tier a chain will use, not just the first: the ingest chain parses on no
// model at all and then extracts on the reasoning tier, and checking only one of
// them would let a submission through that fails two stages later — which is the
// exact failure this check exists to prevent.
//
// A chain absent from this map does no AI work, and the guard passes it through
// rather than inventing a requirement: the noop chain must not need a provider
// configured to run.
var chainTiers = map[jobs.Chain][]Tier{
	jobs.ChainSmoke:    {TierCheap},
	jobs.ChainIngest:   {TierReasoning},
	jobs.ChainGenerate: {TierReasoning},
	jobs.ChainAnalyse:  {TierReasoning},
}

// Guard refuses AI work that cannot succeed, at submit rather than in a worker.
//
// Everything it checks is knowable before the job exists: a provider is
// configured, the tier is assigned to a usable model, the project is allowed to
// use that provider's residency, and the budget has room. A user learns about a
// misconfiguration when they press submit, not twenty minutes later from a failed
// job (backend-standards.md 5).
type Guard struct {
	gateway *Gateway
}

func NewGuard(gateway *Gateway) *Guard { return &Guard{gateway: gateway} }

func (g *Guard) EnsureReady(ctx context.Context, chain jobs.Chain, projectID uuid.UUID) error {
	for _, tier := range chainTiers[chain] {
		if err := g.gateway.EnsureReady(ctx, tier, &projectID); err != nil {
			return err
		}
	}
	return nil
}
