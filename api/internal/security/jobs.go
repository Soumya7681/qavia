package security

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/analyses"
	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/runs"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The security scan stage (BE-9.3, BE-9.4).
//
// It plans, resolves, runs, and records. The plan is the agent's — which endpoint, which
// parameter, which library payload ID — and everything after it is the platform's: the
// payload strings come from the reviewed library, the requests are built here, the
// container sends them, and the findings are stored against the results. No attack
// string in the traffic was ever chosen by a model (BE-9.3.2).

// TypeScan is the stage name the pipeline declares.
const TypeScan = jobs.TypeSecurityScan

// Endpoints reads the project's specification.
type Endpoints interface {
	Endpoints(ctx context.Context, projectID uuid.UUID, artifactID *uuid.UUID) ([]ingest.Endpoint, error)
}

// Gateway is the slice of the AI gateway this stage needs.
type Gateway interface {
	ProbeSelect(ctx context.Context, call llm.AgentCall, input llm.ProbeSelectInput) (llm.AgentResult, error)
}

// Runs is the slice of the run service this stage needs.
type Runs interface {
	Create(ctx context.Context, input runs.CreateInput) (runs.Run, targets.Target, error)
	MarkStarted(ctx context.Context, runID uuid.UUID, image string) error
	Finish(ctx context.Context, runID uuid.UUID, outcome runs.Outcome) (runs.Run, error)
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
	RecordCommand(ctx context.Context, runID uuid.UUID, command runs.Command) error
	Results(ctx context.Context, runID uuid.UUID, filter runs.ResultFilter) (runs.ResultPage, error)

	Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error)
	Runtime(ctx context.Context) (runner.Runtime, error)
	ImageFor(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) (string, error)
	TargetCredential(ctx context.Context, projectID uuid.UUID) (string, error)
}

// Analyses stores a minimal analysis per finding, so a finding is promotable to a
// defect through the same path a failed test uses (BE-9.4.2). The finding is the
// analysis: there is no model call, and the evidence is the detection match.
type Analyses interface {
	Save(ctx context.Context, input analyses.SaveInput) (analyses.Analysis, error)
}

// Deps is everything the stage shares.
type Deps struct {
	Endpoints Endpoints
	Gateway   Gateway
	Findings  *Service
	Analyses  Analyses
	Runs      Runs
	Driver    runner.Driver
}

// ScanHandler runs one security scan.
type ScanHandler struct {
	deps Deps
}

func NewScanHandler(deps Deps) *ScanHandler { return &ScanHandler{deps: deps} }

func (h *ScanHandler) Type() string { return TypeScan }

// Payload names the scan.
type ScanPayload struct {
	ProjectID  uuid.UUID `json:"projectId"`
	TargetURL  string    `json:"targetUrl,omitempty"`
	Categories []string  `json:"categories,omitempty"`
	ActorID    uuid.UUID `json:"actorId"`
	RequestID  uuid.UUID `json:"requestId"`
}

func (h *ScanHandler) IdempotencyKey(payload ScanPayload) string { return IdempotencyKey(payload) }

// IdempotencyKey is exported so the API can declare the type as enqueue-only.
func IdempotencyKey(payload ScanPayload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("security:%s", payload.ProjectID)
	}
	return fmt.Sprintf("security:%s", payload.RequestID)
}

func (h *ScanHandler) Handle(ctx context.Context, payload ScanPayload, jc jobs.JobContext) error {
	jobID := jc.JobID()

	endpoints, err := h.deps.Endpoints.Endpoints(ctx, payload.ProjectID, nil)
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the project's endpoints: %w", err))
	}
	if len(endpoints) == 0 {
		return apierr.NoEndpointsToProbe()
	}

	jc.Event("Planning probes across %d endpoint(s)", len(endpoints))

	plan, err := h.plan(ctx, payload, jobID, endpoints)
	if err != nil {
		return err
	}
	jc.Event("The agent proposed %d probe(s): %s", len(plan.Probes), plan.Summary)

	// The run row, with the target re-checked against the allowlist. This is the
	// reject-before-traffic gate: a target outside the allowlist fails here, before the
	// container that would send the probes is created (BE-9.5.4).
	run, checked, err := h.deps.Runs.Create(ctx, runs.CreateInput{
		ProjectID:   payload.ProjectID,
		Framework:   testfiles.FrameworkSecurity,
		Trigger:     runs.TriggerManual,
		Kind:        runs.KindSecurity,
		TargetURL:   payload.TargetURL,
		TriggeredBy: &payload.ActorID,
		JobID:       &jobID,
	})
	if err != nil {
		return err
	}

	credential, err := h.deps.Runs.TargetCredential(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	resolved, dropped := Resolve(plan, endpoints, checked.URL, credential)
	if len(dropped) > 0 {
		// Said out loud: a plan entry the platform refused to send is worth seeing,
		// because "the model named an unknown payload" and "the endpoint was destructive"
		// are both things a reviewer should know happened (BE-9.3.2).
		jc.Event("Refused %d plan entr(y/ies): %v", len(dropped), dropped)
	}
	if len(resolved) == 0 {
		if _, markErr := h.finishFailed(ctx, run.ID, fmt.Errorf("no probe could be built from the plan")); markErr != nil {
			jc.Event("Could not record the failure: %v", markErr)
		}
		return apierr.Validation("No probe could be built from the plan.", nil)
	}

	runtime, err := h.deps.Runs.Runtime(ctx)
	if err != nil {
		return err
	}
	run.Runtime = runtime

	if err := h.deps.Runs.MarkStarted(ctx, run.ID, run.Image); err != nil {
		return err
	}

	jc.Event("Sending %d probe(s) at %s", len(resolved), checked.URL)
	report, err := h.execute(ctx, run, checked, resolved, jc)
	if err != nil {
		if _, markErr := h.finishFailed(ctx, run.ID, err); markErr != nil {
			jc.Event("Could not record the failure: %v", markErr)
		}
		return err
	}

	if err := h.record(ctx, run, report, jc); err != nil {
		return err
	}

	jc.Progress(100)
	return nil
}

// plan asks the agent which endpoints and payloads to probe.
func (h *ScanHandler) plan(
	ctx context.Context,
	payload ScanPayload,
	jobID uuid.UUID,
	endpoints []ingest.Endpoint,
) (Plan, error) {
	categories := payload.Categories
	if len(categories) == 0 {
		for _, category := range Categories() {
			categories = append(categories, string(category))
		}
	}

	catalogue := make([]map[string]any, 0)
	for _, entry := range Catalogue() {
		catalogue = append(catalogue, map[string]any{
			"id":       entry.ID,
			"category": string(entry.Category),
			"purpose":  entry.Purpose,
			"delivery": string(entry.Delivery),
		})
	}

	result, err := h.deps.Gateway.ProbeSelect(ctx, llm.AgentCall{ProjectID: &payload.ProjectID, JobID: &jobID},
		llm.ProbeSelectInput{
			Endpoints:  endpointSummary(endpoints),
			Catalogue:  catalogue,
			Categories: categories,
		})
	if err != nil {
		return Plan{}, err
	}

	plan, err := DecodePlan(result.Raw)
	if err != nil {
		return Plan{}, apierr.Internal(err)
	}
	return plan, nil
}

// execute runs the one container and returns the parsed report.
func (h *ScanHandler) execute(
	ctx context.Context,
	run runs.Run,
	target targets.Target,
	probes []ResolvedProbe,
	jc jobs.JobContext,
) (runs.Report, error) {
	limits, err := h.deps.Runs.Limits(ctx, run.ProjectID)
	if err != nil {
		return runs.Report{}, err
	}

	config, err := json.Marshal(Config{Schema: ConfigSchema, Probes: probes})
	if err != nil {
		return runs.Report{}, apierr.Internal(fmt.Errorf("encode the probe list: %w", err))
	}

	spec := runner.Spec{
		RunID:   run.ID.String(),
		Image:   run.Image,
		Command: []string{"qavia-run"},
		// The probe list is delivered as a workspace file, the same channel the driver
		// uses for a suite. It holds the resolved payloads and the credential and lives
		// only in the container's tmpfs.
		Workspace: map[string]string{"probes.json": string(config)},
		Limits:    limits,
		Runtime:   run.Runtime,
		Reports:   []string{runs.ReportPath},
		Egress: runner.Egress{AllowedHosts: []runner.HostAddress{{
			Host: target.Host,
			IP:   target.Primary().String(),
		}}},
	}

	started := time.Now()
	if err := h.deps.Runs.RecordCommand(ctx, run.ID, runs.Command{
		Command: "qavia-run (security probes)",
	}); err != nil {
		jc.Event("Could not record the command: %v", err)
	}

	logs := &bytes.Buffer{}
	result, err := h.deps.Driver.Run(ctx, spec, logs)
	if err != nil {
		return runs.Report{}, err
	}
	_ = started

	raw, found := result.Reports[runs.ReportPath]
	if !found {
		return runs.Report{}, apierr.Internal(fmt.Errorf("the security scan produced no report"))
	}
	return runs.ParseReport(raw)
}

// record writes the results and the findings.
func (h *ScanHandler) record(
	ctx context.Context,
	run runs.Run,
	report runs.Report,
	jc jobs.JobContext,
) error {
	results := make([]runs.Result, 0, len(report.Results))
	for _, item := range report.Results {
		status := runs.ResultPassed
		if item.Status == "failed" {
			status = runs.ResultFailed
		}
		results = append(results, runs.Result{
			RunID:          run.ID,
			Name:           item.Name,
			Status:         status,
			Attempt:        1,
			FailureMessage: item.FailureMessage,
		})
	}

	status := runs.StatusPassed
	if len(report.Findings) > 0 {
		// A scan that found something is a failed run: a green security run with findings
		// would be the most misleading outcome the platform could produce.
		status = runs.StatusFailed
	}

	if _, err := h.deps.Runs.Finish(ctx, run.ID, runs.Outcome{
		Status:   status,
		Results:  results,
		Duration: time.Since(run.CreatedAt),
	}); err != nil {
		return err
	}

	if len(report.Findings) == 0 {
		jc.Event("No findings: every probe was handled correctly")
		return nil
	}

	// The results are read back to get their IDs — Finish writes them with CopyFrom,
	// which does not return them — so each finding attaches to its own result row and
	// can be promoted to a defect the same way a failed test is (BE-9.4.2).
	page, err := h.deps.Runs.Results(ctx, run.ID, runs.ResultFilter{Limit: 1000})
	if err != nil {
		return err
	}
	resultByName := make(map[string]uuid.UUID, len(page.Items))
	for _, result := range page.Items {
		resultByName[result.Name] = result.ID
	}

	stored := 0
	for _, finding := range report.Findings {
		name := finding.PayloadID + " " + finding.Endpoint
		resultID, ok := resultByName[name]
		if !ok {
			jc.Event("A finding matched no result row: %s", name)
			continue
		}

		if _, err := h.deps.Findings.Store(ctx, StoreInput{
			RunResultID:  resultID,
			RunID:        run.ID,
			PayloadID:    finding.PayloadID,
			Category:     finding.Category,
			Endpoint:     finding.Endpoint,
			Parameter:    finding.Parameter,
			Severity:     finding.Severity,
			Evidence:     finding.Evidence,
			Reproduction: finding.Reproduction,
		}); err != nil {
			return err
		}
		stored++
		jc.Event("Finding (%s): %s on %s — %s",
			finding.Severity, finding.PayloadID, finding.Endpoint, finding.Evidence)

		// A minimal analysis, so the finding can be promoted to a defect through the
		// path a failed test already uses. The finding is the analysis: the evidence is
		// the detection match, checked against the database as history rather than an
		// external artifact (BE-9.4.2).
		if h.deps.Analyses != nil {
			if _, err := h.deps.Analyses.Save(ctx, analyses.SaveInput{
				RunResultID:   resultID,
				Reason:        finding.Evidence,
				RootCause:     purposeFor(finding.PayloadID),
				SuggestedFix:  fixFor(finding.Category),
				PromptVersion: "security/v1",
				ModelName:     "security-library",
				Evidence: analyses.Evidence{{
					Kind:   analyses.EvidenceHistory,
					Detail: finding.Reproduction,
				}},
			}); err != nil {
				jc.Event("Could not record the finding's analysis: %v", err)
			}
		}
	}

	jc.Event("Recorded %d finding(s)", stored)
	return nil
}

// purposeFor returns the reviewed payload's purpose, which is the root cause a finding
// cites: it is a sentence a person approved, not a model's guess.
func purposeFor(payloadID string) string {
	if payload, ok := Lookup(payloadID); ok {
		return payload.Purpose
	}
	return "a security probe succeeded"
}

// fixFor is category-level remediation guidance. Deliberately generic: the fix for a
// class of weakness is known and stable, and a per-finding suggestion would be a model
// guessing about a client's code it has not seen.
func fixFor(category string) string {
	switch Category(category) {
	case CategorySQLi:
		return "Use parameterised queries; never build SQL by string concatenation of input."
	case CategoryXSS:
		return "Escape output for its context, and set a Content-Security-Policy."
	case CategoryCSRF:
		return "Require an anti-CSRF token on state-changing requests, or a SameSite cookie."
	case CategoryJWT:
		return "Verify the signature and the algorithm server-side; reject alg:none and expired tokens."
	case CategoryIDOR:
		return "Check that the authenticated caller owns the resource before returning it."
	case CategoryRateLimit:
		return "Rate-limit the endpoint per client, returning 429 past the limit."
	case CategoryBrokenAuth:
		return "Require and verify a credential on every protected endpoint."
	default:
		return "Review the endpoint's handling of the probed input."
	}
}

func (h *ScanHandler) finishFailed(ctx context.Context, runID uuid.UUID, cause error) (runs.Run, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := h.deps.Runs.MarkFailed(writeCtx, runID, cause.Error()); err != nil {
		return runs.Run{}, err
	}
	return runs.Run{}, nil
}

// endpointSummary is the shape the agent reads: methods, paths, and parameters, without
// the full schemas a probe planner does not need.
func endpointSummary(endpoints []ingest.Endpoint) map[string]any {
	summarised := make([]map[string]any, 0, len(endpoints))
	for _, endpoint := range endpoints {
		params := make([]string, 0, len(endpoint.Parameters))
		for _, parameter := range endpoint.Parameters {
			params = append(params, parameter.In+":"+parameter.Name)
		}
		summarised = append(summarised, map[string]any{
			"method":     endpoint.Method,
			"path":       endpoint.Path,
			"summary":    endpoint.Summary,
			"parameters": params,
			"security":   len(endpoint.Security) > 0,
		})
	}
	return map[string]any{"endpoints": summarised}
}
