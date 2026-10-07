package jobs

import (
	"context"
	"fmt"
	"time"
)

// Chain is a named sequence of stages.
//
// Every chain in the platform is declared in this file. A handler never calls the
// next handler directly: it finishes, and the runner enqueues whatever this table
// says comes next. That is what keeps each stage independently retryable and makes
// the shape of a submission readable in one place (backend-standards.md 8).
type Chain string

const (
	// ChainNoop proves the pipeline before any real work exists: three stages,
	// each reporting progress, chained through the queue like everything else.
	ChainNoop Chain = "noop"

	// The chains below are declared so the contract, the client, and the
	// submission endpoint all know their names from the start. Each becomes
	// runnable when the phase that implements its stages lands, and until then
	// submitting one is refused at enqueue with a reason rather than accepted and
	// silently dropped.
	// ChainSmoke proves the AI layer the way ChainNoop proves the queue: one
	// stage that asks the cheap tier for a fixed structured answer and checks it
	// came back. When generation misbehaves later, this says whether the AI layer
	// or the agent is at fault (BE-1.15).
	ChainSmoke Chain = "ai_smoke"

	ChainIngest   Chain = "ingest"
	ChainGenerate Chain = "generate"
	ChainExecute  Chain = "execute"
	ChainAnalyse  Chain = "analyse"
)

// Job type names. The registry keys off these, and the type column stores them.
const (
	TypeExecuteRun      = "execute.run"
	TypeAnalyseRun      = "analyse.run"
	TypeReportBuild     = "report.build"
	TypeRepoSync        = "repo.sync"
	TypeRepoComprehend  = "repo.comprehend"
	TypeRepoUnitTests   = "repo.unittests"
	TypeCoverageMeasure = "coverage.measure"

	// UI discovery walks an application in a real browser and records what a user can
	// do in it. Standalone rather than a chain stage: the graph it produces is
	// reviewed before anything is generated from it (BE-7.2.3).
	TypeUIDiscover = "ui.discover"

	// UI generation turns a reviewed flow graph into Playwright specs. Standalone for
	// the same reason: which graph to generate from is a person's decision (BE-7.4).
	TypeUIGenerate = "ui.generate"

	// A mock server is a container that outlives the request that asked for it, so its
	// lifecycle is two standalone jobs on the host that has a runtime (BE-8.6).
	TypeMockStart = "mock.start"
	TypeMockStop  = "mock.stop"

	// A performance run generates a k6 script under an authorised profile and runs it
	// as one container; a security scan builds a probe plan from a reviewed library and
	// executes it. Both are standalone and both are gated (BE-9.1, BE-9.4, BE-9.5).
	TypePerfRun      = "perf.run"
	TypeSecurityScan = "security.scan"

	TypeNoopPrepare  = "noop.prepare"
	TypeNoopWork     = "noop.work"
	TypeNoopFinalise = "noop.finalise"

	// TypeAISmoke is declared here rather than in the llm package so the pipeline
	// table stays the one place a chain's stages are listed. The llm package
	// references this constant, so the two cannot drift.
	TypeAISmoke = "ai.smoke"

	// Ingest and generation. Ingest is deterministic parsing; extract and design
	// are the two agent stages, and they are separate stages so a failed design
	// re-runs without re-reading the specification.
	TypeIngestParse    = "ingest.parse"
	TypeIngestExtract  = "ingest.extract"
	TypeGenerateDesign = "generate.design"
	TypeGenerateCode   = "generate.code"

	TypeChainParent = "chain"
)

// chains declares every chain's stages, in order.
//
// A chain with no stages is one whose phase has not landed. It is listed rather
// than omitted so the API contract, the UI, and the error message all agree about
// what exists and what is merely not ready.
var chains = map[Chain][]string{
	ChainNoop:     {TypeNoopPrepare, TypeNoopWork, TypeNoopFinalise},
	ChainSmoke:    {TypeAISmoke},
	ChainIngest:   {TypeIngestParse, TypeIngestExtract},
	ChainGenerate: {TypeGenerateDesign, TypeGenerateCode},
	ChainExecute:  {TypeExecuteRun},
	ChainAnalyse:  {TypeAnalyseRun},
}

// chainLabels name a chain for a person reading a notification.
var chainLabels = map[Chain]string{
	ChainNoop:     "Pipeline check",
	ChainSmoke:    "AI check",
	ChainIngest:   "Ingest",
	ChainGenerate: "Test generation",
	ChainExecute:  "Test run",
	ChainAnalyse:  "Failure analysis",
}

// StagesFor returns a chain's stages and whether the chain is declared.
func StagesFor(chain Chain) ([]string, bool) {
	stages, declared := chains[chain]
	return stages, declared
}

// Runnable reports whether a chain has stages that can actually run.
func Runnable(chain Chain) bool {
	stages, declared := chains[chain]
	return declared && len(stages) > 0
}

// Label names a chain for a person.
func Label(chain Chain) string {
	if label, known := chainLabels[chain]; known {
		return label
	}
	return string(chain)
}

// ------------------------------------------------------------------- noop chain

// The noop handlers exist to prove the pipeline end to end before there is any
// real work to run: three stages that report progress, chain through the queue,
// and finish with a notification. When a stage of the real pipeline misbehaves
// later, this is what says whether the queue or the work is at fault.

// noopStep is how long each unit of noop work takes. Short enough that a demo is
// not tedious, long enough that progress is visibly incremental rather than
// jumping from nothing to done.
const noopStep = 300 * time.Millisecond

// noopHandler is one stage of the noop chain.
type noopHandler struct {
	typeName string
	label    string
	steps    int
}

// NoopHandlers returns the three stages, ready to register.
func NoopHandlers() []TypeHandler[StagePayload] {
	return []TypeHandler[StagePayload]{
		&noopHandler{typeName: TypeNoopPrepare, label: "Preparing", steps: 5},
		&noopHandler{typeName: TypeNoopWork, label: "Working", steps: 10},
		&noopHandler{typeName: TypeNoopFinalise, label: "Finalising", steps: 5},
	}
}

func (h *noopHandler) Type() string { return h.typeName }

// IdempotencyKey is the chain's parent job plus this stage.
//
// Stable across retries and unique per stage, so a redelivered task finds the
// existing row instead of creating a second one. Every handler in the platform
// follows this shape (backend-standards.md 8).
func (h *noopHandler) IdempotencyKey(payload StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

// Handle sleeps in steps, reporting progress as it goes.
//
// It checks the context between units of work rather than only at the start:
// cancellation is cooperative, and a handler that ignores the context cannot be
// cancelled and gets killed instead, mid-write.
func (h *noopHandler) Handle(ctx context.Context, payload StagePayload, jc JobContext) error {
	jc.Event("%s (stage %d of %d)", h.label, payload.Stage+1, len(chains[payload.Chain]))

	for step := 1; step <= h.steps; step++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(noopStep):
		}

		jc.Progress(step * 100 / h.steps)
	}

	jc.Event("%s finished", h.label)
	return nil
}
