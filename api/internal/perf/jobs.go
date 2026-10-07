package perf

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/runs"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The performance run stage (BE-9.1).
//
// It generates a k6 script, validates it, then runs it as one container and stores the
// measured series. Standalone rather than a chain: a load run is generate-then-execute
// as a single act a person authorised, and splitting it would let a script be generated
// and then run against a host the authorisation did not cover.
//
// It is a focused execute path rather than the shared one on purpose. A load run has no
// retries — re-running a script that failed under load is not flake detection, it is
// twice the load — and no per-test results, so the shared handler's retry, flake, and
// quarantine machinery is machinery this run does not want.

// TypeRun is the stage name the pipeline declares.
const TypeRun = jobs.TypePerfRun

// Runs is the slice of the run service this stage needs.
type Runs interface {
	Create(ctx context.Context, input runs.CreateInput) (runs.Run, targets.Target, error)
	MarkStarted(ctx context.Context, runID uuid.UUID, image string) error
	Finish(ctx context.Context, runID uuid.UUID, outcome runs.Outcome) (runs.Run, error)
	MarkFailed(ctx context.Context, id uuid.UUID, reason string) error
	RecordCommand(ctx context.Context, runID uuid.UUID, command runs.Command) error

	Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error)
	Runtime(ctx context.Context) (runner.Runtime, error)
	ImageFor(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) (string, error)
	TargetCredential(ctx context.Context, projectID uuid.UUID) (string, error)
}

// Validator compiles the script in the k6 image before it makes a request.
type Validator interface {
	Validate(
		ctx context.Context,
		projectID uuid.UUID,
		framework testfiles.Framework,
		files map[string]string,
	) (map[string][]runner.ValidationProblem, error)
}

// Files stores the generated script, so a run is traceable to what it executed.
type Files interface {
	Save(ctx context.Context, input testfiles.SaveInput) (testfiles.File, error)
	MarkValidated(ctx context.Context, id uuid.UUID, note string) error
	MarkRejected(ctx context.Context, id uuid.UUID, note string) error
}

// Deps is everything the stage shares.
type Deps struct {
	Service   *Service
	Runs      Runs
	Driver    runner.Driver
	Validator Validator
	Files     Files
}

// RunHandler runs one performance test.
type RunHandler struct {
	deps Deps
}

func NewRunHandler(deps Deps) *RunHandler { return &RunHandler{deps: deps} }

func (h *RunHandler) Type() string { return TypeRun }

// Payload names the run.
type Payload struct {
	ProjectID uuid.UUID `json:"projectId"`
	TargetURL string    `json:"targetUrl,omitempty"`

	Profile Profile   `json:"profile"`
	ActorID uuid.UUID `json:"actorId"`

	// RequestID is the per-request nonce the idempotency key is built from: running a
	// load test again is work somebody asks for.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *RunHandler) IdempotencyKey(payload Payload) string { return IdempotencyKey(payload) }

// IdempotencyKey is exported so the API can declare the type as enqueue-only.
func IdempotencyKey(payload Payload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("perf:%s", payload.ProjectID)
	}
	return fmt.Sprintf("perf:%s", payload.RequestID)
}

const entryFile = "script.js"

func (h *RunHandler) Handle(ctx context.Context, payload Payload, jc jobs.JobContext) error {
	profile := payload.Profile.WithDefaults()
	jobID := jc.JobID()

	jc.Event("Generating a load script for %s", profile.Describe())

	script, err := h.deps.Service.Generate(ctx, payload.ProjectID, jobID, profile)
	if err != nil {
		return err
	}
	jc.Event("Generated a k6 script exercising %d endpoint(s)", len(script.Exercises))

	// Validated in the k6 image before a single request: k6 archive resolves every
	// import without making a call, and a script that does not compile must fail here
	// rather than partway through a run somebody is watching a real server for
	// (BE-3.4, BE-9.1.1).
	if h.deps.Validator != nil {
		problems, err := h.deps.Validator.Validate(ctx, payload.ProjectID,
			testfiles.FrameworkK6, map[string]string{entryFile: script.Content})
		if err != nil {
			return err
		}
		if len(problems[entryFile]) > 0 {
			return apierr.Validation(
				"The generated load script did not compile: "+problems[entryFile][0].String(),
				map[string]any{"stage": "validation"})
		}
	}

	// Stored so a run is traceable to the exact script it executed, and reviewable.
	stored, err := h.deps.Files.Save(ctx, testfiles.SaveInput{
		ProjectID:   payload.ProjectID,
		Framework:   testfiles.FrameworkK6,
		Path:        entryFile,
		Content:     script.Content,
		GeneratedBy: script.Model,
	})
	if err != nil {
		return err
	}
	if err := h.deps.Files.MarkValidated(ctx, stored.ID, "load script for a performance run"); err != nil {
		jc.Event("Could not mark the script validated: %v", err)
	}

	// The run row, with the target re-checked against the allowlist. This is the
	// reject-before-traffic gate: a target outside the allowlist fails here, before the
	// container that would send the load is ever created (BE-9.5.4).
	run, checked, err := h.deps.Runs.Create(ctx, runs.CreateInput{
		ProjectID:   payload.ProjectID,
		Framework:   testfiles.FrameworkK6,
		Trigger:     runs.TriggerManual,
		Kind:        runs.KindPerformance,
		LoadProfile: profile.JSON(),
		TargetURL:   payload.TargetURL,
		TriggeredBy: &payload.ActorID,
		JobID:       &jobID,
	})
	if err != nil {
		return err
	}

	runtime, err := h.deps.Runs.Runtime(ctx)
	if err != nil {
		return err
	}
	run.Runtime = runtime

	if err := h.deps.Runs.MarkStarted(ctx, run.ID, run.Image); err != nil {
		return err
	}

	jc.Event("Running the load test against %s", checked.URL)
	if err := h.execute(ctx, run, checked, profile, script.Content, jc); err != nil {
		if _, markErr := h.finishFailed(ctx, run.ID, err); markErr != nil {
			jc.Event("Could not record the failure: %v", markErr)
		}
		return err
	}

	jc.Progress(100)
	return nil
}

// execute runs the one container and stores the outcome.
func (h *RunHandler) execute(
	ctx context.Context,
	run runs.Run,
	target targets.Target,
	profile Profile,
	content string,
	jc jobs.JobContext,
) error {
	credential, err := h.deps.Runs.TargetCredential(ctx, run.ProjectID)
	if err != nil {
		return err
	}

	limits, err := h.deps.Runs.Limits(ctx, run.ProjectID)
	if err != nil {
		return err
	}
	// A load run's wall clock has to cover the whole profile plus a margin for k6 to
	// start and to write its summary; the default per-run timeout is sized for a
	// functional suite and would kill a two-minute hold.
	limits.Timeout = time.Duration(profile.TotalSeconds()+60) * time.Second

	env := map[string]string{
		"QAVIA_TARGET_URL": target.URL,
		"QAVIA_ENTRY_FILE": entryFile,
		"CI":               "true",
	}
	if credential != "" {
		env["QAVIA_AUTH_TOKEN"] = credential
	}

	spec := runner.Spec{
		RunID:     run.ID.String(),
		Image:     run.Image,
		Command:   []string{"qavia-run", "test"},
		Workspace: map[string]string{entryFile: content},
		Env:       env,
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
		Command: runner.RedactedCommand(spec.Command, spec.Env),
	}); err != nil {
		jc.Event("Could not record the command: %v", err)
	}

	logs := &bytes.Buffer{}
	result, err := h.deps.Driver.Run(ctx, spec, logs)
	if err != nil {
		return err
	}

	raw, found := result.Reports[runs.ReportPath]
	if !found {
		if result.Killed() {
			return apierr.Internal(fmt.Errorf("the load run was stopped: %s", result.Reason()))
		}
		return apierr.Internal(fmt.Errorf("the load run produced no report"))
	}

	report, err := runs.ParseReport(raw)
	if err != nil {
		return err
	}

	outcome := runs.Outcome{
		Status:   runs.StatusPassed,
		Duration: time.Since(started),
		Results:  reportResults(run.ID, report),
		Metrics:  report.Metrics,
	}
	// A crossed threshold fails the run, the same way a failed assertion fails a
	// functional one: the thresholds are the load test's pass condition.
	for _, item := range report.Results {
		if item.Status == "failed" {
			outcome.Status = runs.StatusFailed
			break
		}
	}
	if outcome.Metrics != nil {
		outcome.Metrics.DurationMs = time.Since(started).Milliseconds()
	}

	finished, err := h.deps.Runs.Finish(ctx, run.ID, outcome)
	if err != nil {
		return err
	}

	if finished.LoadProfile != nil && outcome.Metrics != nil {
		jc.Event("Sent %d request(s) at %.1f/s; p95 %.0fms, error rate %.1f%%",
			outcome.Metrics.Requests, outcome.Metrics.Throughput,
			outcome.Metrics.LatencyP95Ms, outcome.Metrics.ErrorRate*100)
	}
	return nil
}

// reportResults maps k6's thresholds onto run results, so a load run's pass condition
// is inspectable the same way a functional run's is.
func reportResults(runID uuid.UUID, report runs.Report) []runs.Result {
	results := make([]runs.Result, 0, len(report.Results))
	for _, item := range report.Results {
		results = append(results, runs.Result{
			RunID:          runID,
			Name:           item.Name,
			Status:         runs.ResultStatus(mapStatus(item.Status)),
			Duration:       time.Duration(item.DurationMs) * time.Millisecond,
			Attempt:        1,
			FailureMessage: item.FailureMessage,
		})
	}
	return results
}

func mapStatus(status string) string {
	if status == "failed" {
		return string(runs.ResultFailed)
	}
	return string(runs.ResultPassed)
}

func (h *RunHandler) finishFailed(ctx context.Context, runID uuid.UUID, cause error) (runs.Run, error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := h.deps.Runs.MarkFailed(writeCtx, runID, cause.Error()); err != nil {
		return runs.Run{}, err
	}
	return runs.Run{}, nil
}
