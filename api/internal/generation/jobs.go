// Package generation wires the ingest and generate chains together.
//
// It exists because the stages cross four packages — artifacts, ingest,
// requirements, testcases, and the AI gateway — and none of them should learn
// about the others to run a pipeline. Each handler here is thin: it orchestrates,
// and every rule it depends on lives in the service that owns the data.
package generation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/requirements"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/testcases"
)

// Job types. The names match the stages the pipeline table declares.
const (
	TypeIngest  = jobs.TypeIngestParse
	TypeExtract = jobs.TypeIngestExtract
	TypeDesign  = jobs.TypeGenerateDesign
)

// Artifacts is the slice of the artifacts service this package needs.
type Artifacts interface {
	StorageKeyFor(ctx context.Context, id uuid.UUID) (ArtifactRef, error)
}

// ArtifactRef is what a stage needs to know about an input.
type ArtifactRef struct {
	ID         uuid.UUID
	ProjectID  uuid.UUID
	Kind       string
	Filename   string
	StorageKey string
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
}

// Chains is how a stage starts the next chain. Declared as one method rather than
// taking the jobs service, so this package cannot accidentally reach for anything
// else it owns.
type Chains interface {
	SubmitTriggered(ctx context.Context, req runtrigger.Request) (uuid.UUID, error)
}

// Deps is everything the handlers share. One struct rather than six arguments
// repeated three times, and it is assembled in main.go like everything else.
type Deps struct {
	Artifacts    Artifacts
	Ingest       *ingest.Service
	Requirements *requirements.Service
	TestCases    *testcases.Service
	Gateway      *llm.Gateway
	Settings     Settings

	// Validator is optional: it needs a container runtime, which the API process
	// does not have and a developer's worker may not either (BE-3.4).
	Validator Validator

	// Chains is optional: the API process wires it, and a worker that only runs
	// stages does too. A nil one means extraction stops after extracting, which is
	// the same outcome as the review gate being on.
	Chains Chains
}

// IngestHandler parses an uploaded specification into the endpoint model.
//
// Deterministic work only: no model is called here. A parse failure is a clean
// job failure carrying the line number, not a retry loop, because the file will
// not become valid on the second attempt (BE-2.5).
type IngestHandler struct {
	deps Deps
}

func NewIngestHandler(deps Deps) *IngestHandler { return &IngestHandler{deps: deps} }

func (h *IngestHandler) Type() string { return TypeIngest }

// IdempotencyKey is the chain's parent and stage, like every other stage. The
// artifact's own content hash is already what makes re-uploading free; this covers
// a redelivered task.
func (h *IngestHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *IngestHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	if payload.ArtifactID == nil {
		return apierr.Validation("This chain needs an artifact to work from.", nil)
	}

	artifact, err := h.deps.Artifacts.StorageKeyFor(ctx, *payload.ArtifactID)
	if err != nil {
		return err
	}
	jc.Event("Parsing %s", artifact.Filename)

	document, err := h.deps.Ingest.Ingest(ctx, ingest.Artifact{
		ID:         artifact.ID,
		ProjectID:  artifact.ProjectID,
		Kind:       artifact.Kind,
		Filename:   artifact.Filename,
		StorageKey: artifact.StorageKey,
	}, func(done, total int) {
		if total > 0 {
			jc.Progress(done * 100 / total)
		}
	})
	if err != nil {
		return err
	}

	for _, warning := range document.Warnings {
		jc.Event("Warning: %s", warning)
	}
	jc.Event("Parsed %d endpoints from %s", len(document.Endpoints), document.Title)
	jc.Progress(100)
	return nil
}

// ExtractHandler turns the parsed model into requirements (F-4.1).
type ExtractHandler struct {
	deps Deps
}

func NewExtractHandler(deps Deps) *ExtractHandler { return &ExtractHandler{deps: deps} }

func (h *ExtractHandler) Type() string { return TypeExtract }

func (h *ExtractHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *ExtractHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	document, endpointIDs, err := h.document(ctx, payload)
	if err != nil {
		return err
	}
	jc.Event("Reading %d endpoints", len(document.Endpoints))
	jc.Progress(10)

	jobID := jc.JobID()
	result, err := h.deps.Gateway.Extract(ctx, llm.AgentCall{
		ProjectID: &payload.ProjectID,
		JobID:     &jobID,
	}, promptDocument(document))
	if err != nil {
		return err
	}
	jc.Progress(70)

	extraction, err := requirements.DecodeExtraction(result.Raw)
	if err != nil {
		return err
	}
	if len(extraction.Requirements) == 0 {
		return apierr.NoRequirementsExtracted()
	}

	saved, err := h.deps.Requirements.Save(ctx, requirements.SaveInput{
		ProjectID:   payload.ProjectID,
		ArtifactID:  payload.ArtifactID,
		EndpointIDs: endpointIDs,
		GeneratedBy: result.ModelName,
		Items:       extraction.Requirements,
	})
	if err != nil {
		return err
	}

	jc.Event("Extracted %d requirements for $%s using %s",
		len(saved), result.Cost.StringFixed(4), result.ModelName)
	jc.Progress(100)

	return h.continueOrPause(ctx, payload, jc)
}

// continueOrPause is the review gate (BE-2.6).
//
// With review off, generation starts by itself: the point of the pipeline is that
// a user uploads a specification and comes back to test cases. With review on, the
// chain stops here and the completion notification is the prompt to go and check
// what was understood before paying to design against it.
func (h *ExtractHandler) continueOrPause(
	ctx context.Context,
	payload jobs.StagePayload,
	jc jobs.JobContext,
) error {
	review, err := h.deps.Settings.Bool(ctx, "generation.review_extraction",
		settings.Target{ProjectID: &payload.ProjectID})
	if err != nil {
		return err
	}

	if review {
		jc.Event("Paused for review. Generation starts when you submit it, " +
			"so corrections here are not paid for twice")
		return nil
	}
	if h.deps.Chains == nil {
		jc.Event("Extraction finished. Submit generation to design test cases")
		return nil
	}

	if _, err := h.deps.Chains.SubmitTriggered(ctx, runtrigger.Request{
		ProjectID:  payload.ProjectID,
		Chain:      string(jobs.ChainGenerate),
		ArtifactID: payload.ArtifactID,
		Reference:  "after extraction",
		Source:     runtrigger.KindManual,
	}); err != nil {
		// Not a failure of this stage: the extraction is stored and usable, and the
		// generate chain can be submitted by hand. Saying so beats failing a stage
		// that did its job.
		jc.Event("Extraction finished, but generation could not be started: %s", err)
		return nil
	}

	jc.Event("Generation started")
	return nil
}

// DesignHandler generates test cases, one call per requirement (F-5.1).
//
// The fan-out is bounded by the configured concurrency: a 400-requirement project
// must not launch 400 concurrent provider calls, and the limit is a setting rather
// than a constant so an operator can lower it when a provider starts rate limiting
// (BE-2.7).
type DesignHandler struct {
	deps Deps
}

func NewDesignHandler(deps Deps) *DesignHandler { return &DesignHandler{deps: deps} }

func (h *DesignHandler) Type() string { return TypeDesign }

func (h *DesignHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *DesignHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	document, _, err := h.document(ctx, payload)
	if err != nil {
		return err
	}

	items, err := h.deps.Requirements.All(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return apierr.NoRequirementsExtracted()
	}

	limit, err := h.deps.Settings.Int(ctx, "jobs.ai_fan_out_limit",
		settings.Target{ProjectID: &payload.ProjectID})
	if err != nil {
		return err
	}
	jc.Event("Designing cases for %d requirements, %d at a time", len(items), limit)

	prompt := promptDocument(document)
	jobID := jc.JobID()

	var (
		mutex      sync.Mutex
		written    int
		duplicates int
		merged     int
		failed     []string
		done       int
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)

	for _, requirement := range items {
		group.Go(func() error {
			// One requirement failing is recorded and skipped rather than failing
			// the chain: 39 good requirements are worth keeping, and the failed one
			// is visible in the log for a re-run (BE-2.7).
			result, err := h.deps.Gateway.Design(groupCtx, llm.AgentCall{
				ProjectID: &payload.ProjectID,
				JobID:     &jobID,
			}, prompt, requirements.ForPrompt(requirement))

			mutex.Lock()
			defer mutex.Unlock()

			done++
			jc.Progress(done * 100 / len(items))

			if err != nil {
				if groupCtx.Err() != nil {
					// Cancelled or shutting down. Not this requirement's fault, and
					// the job is retried whole.
					return err
				}
				failed = append(failed, requirement.Title)
				slog.WarnContext(ctx, "design failed for requirement",
					"requirement", requirement.Title, "error", err)
				return nil
			}

			design, err := testcases.DecodeDesign(result.Raw)
			if err != nil {
				failed = append(failed, requirement.Title)
				return nil
			}

			// The transaction is opened here, after the provider call rather than
			// around it: a transaction held across a 90-second model call exhausts
			// the pool (backend-standards.md 8).
			requirementID := requirement.ID
			saved, err := h.deps.TestCases.Save(groupCtx, testcases.SaveInput{
				ProjectID:     payload.ProjectID,
				RequirementID: &requirementID,
				GeneratedBy:   result.ModelName,
				Items:         design.TestCases,
			})
			if err != nil {
				return err
			}

			written += saved.Written
			duplicates += saved.Duplicates

			// The near-miss pass runs per requirement, on the narrow candidate set,
			// and only when something was actually written. It is the cheap-tier
			// fallback for cases the hash could not catch (F-5.4).
			if saved.Written > 0 {
				fresh, err := h.deps.TestCases.ByFingerprint(
					groupCtx, payload.ProjectID, saved.Fingerprints)
				if err == nil {
					merged += mergeNearMisses(groupCtx, h.deps,
						payload.ProjectID, requirementID, jobID, fresh)
				}
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	jc.Event("Wrote %d test cases, skipped %d exact duplicates, merged %d near misses",
		written, duplicates, merged)
	if len(failed) > 0 {
		jc.Event("%d requirements produced nothing and can be re-run: %v", len(failed), failed)
	}
	jc.Progress(100)
	return nil
}

// document loads the parsed endpoint model for a chain.
func (h *ExtractHandler) document(
	ctx context.Context,
	payload jobs.StagePayload,
) (ingest.Document, map[string]uuid.UUID, error) {
	return loadDocument(ctx, h.deps, payload)
}

func (h *DesignHandler) document(
	ctx context.Context,
	payload jobs.StagePayload,
) (ingest.Document, map[string]uuid.UUID, error) {
	return loadDocument(ctx, h.deps, payload)
}

func loadDocument(
	ctx context.Context,
	deps Deps,
	payload jobs.StagePayload,
) (ingest.Document, map[string]uuid.UUID, error) {
	endpoints, err := deps.Ingest.Endpoints(ctx, payload.ProjectID, payload.ArtifactID)
	if err != nil {
		return ingest.Document{}, nil, err
	}
	if len(endpoints) == 0 {
		return ingest.Document{}, nil, apierr.NoEndpointsIngested()
	}

	ids, err := deps.Ingest.EndpointIDs(ctx, payload.ProjectID, payload.ArtifactID)
	if err != nil {
		return ingest.Document{}, nil, err
	}

	return ingest.Document{Endpoints: endpoints}, ids, nil
}

// promptDocument renders the parsed model for an agent.
//
// Sorted and free of identifiers, because this is the cached prefix every call in
// a fan-out shares: a UUID or a timestamp in here would give each call a different
// prefix and silently turn a cached run into a full-price one
// (ai-architecture.md 3.6).
func promptDocument(document ingest.Document) map[string]any {
	ingest.SortEndpoints(document.Endpoints)

	endpoints := make([]map[string]any, 0, len(document.Endpoints))
	for _, endpoint := range document.Endpoints {
		encoded, err := json.Marshal(endpoint)
		if err != nil {
			continue
		}
		var rendered map[string]any
		if err := json.Unmarshal(encoded, &rendered); err != nil {
			continue
		}
		endpoints = append(endpoints, map[string]any{
			"method":      endpoint.Method,
			"path":        endpoint.Path,
			"operationId": endpoint.OperationID,
			"summary":     endpoint.Summary,
			"description": endpoint.Description,
			"parameters":  rendered["Parameters"],
			"request":     rendered["Request"],
			"responses":   rendered["Responses"],
			"security":    rendered["Security"],
			"sourceRef":   endpoint.SourceRef,
		})
	}

	return map[string]any{"endpoints": endpoints}
}
