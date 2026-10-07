package analyses

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The analyse stage (BE-5.2, BE-5.3).
//
// It runs after a run finishes and explains every failure the run produced. The
// order inside one failure is the design:
//
//  1. Gather the material an explanation may cite: the run's log, the failing test's
//     own source, and the test's recent history. Nothing else is offered, so a
//     citation of something else is provably invented.
//  2. Ask the agent, on the reasoning tier.
//  3. Check every citation against that material. A reference to log line 812 of a
//     44-line log fails, and the failure goes back into the retry as text.
//  4. Store, then compute the stability score from stored results — never from
//     anything the agent said about its own confidence.
//
// A failure whose analysis cannot be made to check out is left unanalysed rather
// than stored with invented evidence. That is the whole point: an explanation nobody
// can verify is worse than the honest absence of one.

// TypeAnalyse is the stage name the pipeline declares.
const TypeAnalyse = jobs.TypeAnalyseRun

// maxEvidenceRetries is how many times a rejected analysis is asked again with the
// findings attached. Two, because the failures this catches are either a
// misunderstanding of the format, which one correction fixes, or a model that is
// inventing, which no number of corrections fixes.
const maxEvidenceRetries = 2

// Runs is the slice of the runs service this stage needs, declared by the consumer.
type Runs interface {
	ProjectFor(ctx context.Context, runID uuid.UUID) (uuid.UUID, error)
	LogFor(ctx context.Context, runID uuid.UUID) (string, error)
}

// Files is how the stage finds the source of a failing test, so an analysis can cite
// the assertion that failed rather than describing it.
type Files interface {
	Get(ctx context.Context, id uuid.UUID) (testfiles.File, error)
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
}

// Deps is everything the stage shares, assembled in main like every other handler's
// dependencies.
type Deps struct {
	Analyses *Service
	Runs     Runs
	Files    Files
	Gateway  *llm.Gateway
	Objects  objectstore.Store
	Settings Settings
}

// AnalyseHandler explains a finished run's failures.
type AnalyseHandler struct {
	deps Deps
}

func NewAnalyseHandler(deps Deps) *AnalyseHandler { return &AnalyseHandler{deps: deps} }

func (h *AnalyseHandler) Type() string { return TypeAnalyse }

// IdempotencyKey is the run. A redelivered task re-analyses the same failures, and
// each analysis is a new row rather than an edit, so the key is what stops a
// duplicate delivery doubling the explanations.
func (h *AnalyseHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return AnalyseIdempotencyKey(payload)
}

// AnalyseIdempotencyKey is the same rule, exported so the API process can declare the
// type as enqueue-only without constructing a handler it cannot run.
//
// The chain's parent is in the key as well as the run, and that pairing is the whole
// point. Deduplicating on the run alone stops a redelivered task, which is what
// idempotency is for, but it also makes "analyse this run again" impossible after a
// failure: the key already exists, so the second request finds the failed row and
// pushes nothing. The parent differs per request and is identical across redeliveries
// of the same task, which is exactly the line to draw.
func AnalyseIdempotencyKey(payload jobs.StagePayload) string {
	if payload.RunID != nil {
		return fmt.Sprintf("%s:%s:run:%s", payload.Chain, payload.ParentJobID, *payload.RunID)
	}
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *AnalyseHandler) Handle(
	ctx context.Context,
	payload jobs.StagePayload,
	jc jobs.JobContext,
) error {
	if payload.RunID == nil {
		return apierr.Validation("This chain needs a run to analyse.", nil)
	}
	runID := *payload.RunID

	failures, err := h.deps.Analyses.FailingResults(ctx, runID)
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		jc.Event("Nothing to analyse: this run produced no failures")
		jc.Progress(100)
		return nil
	}

	// Fetched once for the whole run, not once per failure: it is the same log, and
	// it is also the cacheable prefix of every call below.
	log, err := h.deps.Runs.LogFor(ctx, runID)
	if err != nil {
		// A missing log is not a reason to skip the analysis. It narrows what can be
		// cited, and the evidence check will hold the agent to that.
		slog.WarnContext(ctx, "read the run log for analysis", "run_id", runID, "error", err)
	}

	// One analysis per test, not per attempt: three attempts of one flaky test is one
	// question, and paying for it three times would answer it three times identically.
	unique := firstPerTest(failures)
	jc.Event("Analysing %d failing test(s) from %d result row(s)", len(unique), len(failures))

	analysed, rejected := 0, 0
	for index, failure := range unique {
		jc.Progress(index * 100 / max(len(unique), 1))

		stored, err := h.analyse(ctx, payload.ProjectID, runID, failure, log, jc)
		switch {
		case err != nil:
			return err
		case stored:
			analysed++
		default:
			rejected++
		}
	}

	jc.Event("Explained %d failure(s); %d could not be explained with evidence that checks out",
		analysed, rejected)
	jc.Progress(100)
	return nil
}

// analyse handles one failure, including the evidence retry loop. It reports whether
// an analysis was stored.
func (h *AnalyseHandler) analyse(
	ctx context.Context,
	projectID uuid.UUID,
	runID uuid.UUID,
	failure Result,
	log string,
	jc jobs.JobContext,
) (bool, error) {
	sources := h.sourcesFor(ctx, failure)
	history, err := h.deps.Analyses.History(ctx, projectID, failure.Name)
	if err != nil {
		return false, err
	}

	artifacts := Artifacts{
		Log:           log,
		Sources:       sources,
		HistoryLength: len(history),
	}

	// The material the agent may cite, and the cacheable prefix of the call. It is
	// built once and reused across retries so a correction is a cache read.
	material := map[string]any{
		"log":     log,
		"sources": sources,
	}

	jobID := jc.JobID()
	feedback := ""

	for attempt := 1; attempt <= maxEvidenceRetries+1; attempt++ {
		result, err := h.deps.Gateway.Analyse(ctx, llm.AgentCall{
			ProjectID: &projectID,
			JobID:     &jobID,
		}, llm.AnalyseInput{
			Context:        material,
			TestName:       failure.Name,
			Status:         failure.Status,
			Attempt:        failure.Attempt,
			FailureMessage: failure.FailureMessage,
			History:        historyForPrompt(history),
			Feedback:       feedback,
		})
		if err != nil {
			return false, err
		}

		decoded, err := decodeAgentAnalysis(result.Raw)
		if err != nil {
			feedback = err.Error()
			continue
		}

		problems := decoded.Evidence.Validate(artifacts)
		if len(problems) > 0 {
			// Stated in the job log as well as fed back, because a model that keeps
			// inventing citations is something an operator should be able to see.
			jc.Event("Analysis of %q was rejected on attempt %d: %s",
				failure.Name, attempt, strings.Join(problems, "; "))
			feedback = strings.Join(problems, "\n")
			continue
		}

		saved, err := h.deps.Analyses.Save(ctx, SaveInput{
			RunResultID:   failure.ID,
			Reason:        decoded.Reason,
			RootCause:     decoded.RootCause,
			SuggestedFix:  decoded.SuggestedFix,
			Evidence:      decoded.Evidence,
			PromptVersion: promptVersion,
			ModelName:     result.ModelName,
		})
		if err != nil {
			return false, err
		}

		// The score is computed after the analysis, from stored results, because it is
		// the platform's arithmetic and not part of what the agent returned (BE-5.4).
		stability, err := h.deps.Analyses.StabilityFor(ctx, projectID, runID, failure.Name)
		if err != nil {
			return false, err
		}
		if err := h.deps.Analyses.ApplyStability(ctx, saved.ID, stability); err != nil {
			return false, err
		}

		jc.Event("%s: %s (%s)", failure.Name, decoded.Reason, stability.Basis)
		return true, nil
	}

	// Out of attempts. Nothing is stored: an explanation whose citations do not check
	// out is worse than the honest absence of one (BE-5.3.1).
	jc.Event("%s was left unanalysed: the explanation could not be supported by evidence",
		failure.Name)
	return false, nil
}

// sourcesFor is the failing test's own file, which is what lets an analysis cite the
// assertion rather than paraphrase it.
//
// Best effort: a result the platform could not map to a file is analysed from the log
// alone, and the evidence check simply refuses any source citation.
func (h *AnalyseHandler) sourcesFor(ctx context.Context, failure Result) map[string]string {
	if failure.TestFileID == nil || h.deps.Files == nil {
		return map[string]string{}
	}

	file, err := h.deps.Files.Get(ctx, *failure.TestFileID)
	if err != nil {
		slog.WarnContext(ctx, "read the test file for analysis",
			"file_id", *failure.TestFileID, "error", err)
		return map[string]string{}
	}
	return map[string]string{file.Path: file.Content}
}

// agentAnalysis is the agent's envelope. Decoded into a named type rather than a map,
// so a field the schema stopped returning is a compile-time question.
type agentAnalysis struct {
	Reason       string   `json:"reason"`
	RootCause    string   `json:"root_cause"`
	SuggestedFix string   `json:"suggested_fix"`
	Verdict      string   `json:"verdict"`
	Evidence     Evidence `json:"evidence"`
}

// promptVersion mirrors the agent's own constant. Stored with every analysis and
// every vote, so a prompt change is measured rather than argued (BE-5.8).
const promptVersion = "analyse/v1"

func decodeAgentAnalysis(raw json.RawMessage) (agentAnalysis, error) {
	var decoded agentAnalysis
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return agentAnalysis{}, fmt.Errorf("the analysis could not be read: %w", err)
	}
	if strings.TrimSpace(decoded.RootCause) == "" {
		return agentAnalysis{}, fmt.Errorf("the analysis had no root cause")
	}
	return decoded, nil
}

// firstPerTest keeps one row per test name, preferring the earliest attempt: that is
// the one whose failure message describes the original problem rather than a retry's.
func firstPerTest(results []Result) []Result {
	seen := map[string]bool{}
	unique := make([]Result, 0, len(results))

	for _, result := range results {
		if seen[result.Name] {
			continue
		}
		seen[result.Name] = true
		unique = append(unique, result)
	}
	return unique
}

// historyForPrompt renders the history in the shape the agent's schema expects.
func historyForPrompt(points []HistoryPoint) []map[string]any {
	out := make([]map[string]any, 0, len(points))
	for _, point := range points {
		out = append(out, map[string]any{
			"status":  point.Status,
			"attempt": point.Attempt,
			"at":      point.At.Format("2006-01-02 15:04"),
		})
	}
	return out
}
