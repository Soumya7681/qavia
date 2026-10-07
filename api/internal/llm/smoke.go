package llm

import (
	"context"
	"fmt"

	"github.com/hyscaler/qavia/api/internal/jobs"
)

// SmokeType is the job type that proves the AI layer end to end (BE-1.15).
//
// The name comes from the pipeline table rather than being spelled again here:
// the chain declares which stage it runs, and a second spelling would be a way for
// the two to disagree.
const SmokeType = jobs.TypeAISmoke

// smokeAgent names the caller on the llm_calls row, so a smoke test's spend is
// distinguishable from real work on the dashboard.
const smokeAgent = "smoke"

// smokeSchema is deliberately tiny and closed. A smoke test that asked for
// something open-ended would pass on a model that cannot follow a schema at all,
// which is most of what this is checking.
var smokeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ready":   map[string]any{"type": "boolean"},
		"summary": map[string]any{"type": "string"},
	},
	"required":             []any{"ready", "summary"},
	"additionalProperties": false,
}

// smokeTier is what the check runs on: this proves the plumbing, and paying
// reasoning prices to learn that the plumbing works is a bad trade.
const smokeTier = TierCheap

// SmokeHandler asks the configured model for one fixed structured response and
// checks that it came back valid.
//
// It exercises the whole path in one job: tier resolution, residency, the budget
// check, credential decryption, the provider adapter, structured output, and the
// llm_calls row. When something in phase 2 misbehaves later, this is what says
// whether the AI layer or the agent is at fault.
type SmokeHandler struct {
	gateway *Gateway
}

func NewSmokeHandler(gateway *Gateway) *SmokeHandler {
	return &SmokeHandler{gateway: gateway}
}

func (h *SmokeHandler) Type() string { return SmokeType }

// IdempotencyKey covers a retry of the same request rather than a deliberate
// re-run: the nonce is what makes pressing the button twice two jobs.
func (h *SmokeHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *SmokeHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	tier := smokeTier
	projectID := &payload.ProjectID

	resolution, err := h.gateway.Resolve(ctx, tier, projectID)
	if err != nil {
		return err
	}
	jc.Event("Using %s on %s for the %s tier",
		resolution.Model.ModelID, resolution.Provider.Name, tier)
	jc.Progress(20)

	jobID := jc.JobID()
	result, err := h.gateway.Call(ctx, Request{
		Tier:      tier,
		Agent:     smokeAgent,
		ProjectID: projectID,
		JobID:     &jobID,
		Messages: []Message{
			{
				Role: "system",
				// Marked cacheable so the caching strategy has a prefix to work
				// with, which also proves the breakpoint placement does not break a
				// provider that does not cache.
				Content:   "You are a health check. Answer only with the requested JSON object.",
				Cacheable: true,
			},
			{
				Role:    "user",
				Content: `Return {"ready": true, "summary": "ok"}.`,
			},
		},
		ResponseSchema: smokeSchema,
	})
	if err != nil {
		return err
	}
	jc.Progress(80)

	ready, isBool := result.Structured["ready"].(bool)
	if !isBool || !ready {
		return fmt.Errorf("the model answered but did not report ready: %v", result.Structured)
	}

	jc.Event("%s answered in %dms for $%s (%d input, %d output, %d cached tokens)",
		result.ModelName, result.LatencyMS, result.Cost.StringFixed(6),
		result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.CacheReadTokens)
	if result.FallbackUsed {
		jc.Event("The primary model was unavailable and the fallback served this call")
	}
	if result.ValidationRetry > 0 {
		jc.Event("The response needed %d validation retry(s)", result.ValidationRetry)
	}

	jc.Progress(100)
	return nil
}
