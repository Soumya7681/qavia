package runs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The execute stage (BE-4.9).
//
// It is the one stage in the platform that runs code it did not write, so the order
// of operations is the design:
//
//  1. Read the run row and refuse to start unless it is still queued. A cancelled
//     run must not start a container after the fact.
//  2. Re-check the target. The allowlist was checked before enqueue; between then
//     and now DNS could have moved, and the runner's firewall is built from what is
//     true at start.
//  3. Assemble the workspace in memory and copy it in. Never mount the host.
//  4. Run, with the driver's own wall clock. Stream logs as they arrive.
//  5. Retry only the tests that failed, N times from settings, and count changes
//     across attempts as flake rather than as failure.
//  6. Persist once, after the run, in one transaction with a CopyFrom.

// TypeExecute is the stage name the pipeline declares.
const TypeExecute = jobs.TypeExecuteRun

// TestFiles is the slice of the test-file service this stage needs.
type TestFiles interface {
	All(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) ([]testfiles.File, error)
}

// Slots is the global concurrency limit (BE-4.10). Declared as an interface so the
// worker owns the semaphore and this stage only waits on it.
type Slots interface {
	// Acquire blocks until a runner slot is free. The returned release must be
	// called exactly once.
	Acquire(ctx context.Context) (release func(), err error)

	// Waiting reports how many stages are queued behind the limit, so the log can
	// say "waiting for a runner slot" rather than nothing.
	Waiting() int
}

// ExecuteDeps is everything the stage needs, assembled in main like every other
// handler's dependencies.
type ExecuteDeps struct {
	Runs     *Service
	Files    TestFiles
	Driver   runner.Driver
	Objects  objectstore.Store
	Settings Settings
	Targets  Targets
	Slots    Slots

	// Chains starts the analysis of what the run just found. Optional: a worker
	// without it still runs suites, and the failures wait for somebody to ask for an
	// explanation. Same interface the handler uses to start a run.
	Chains Chains
}

// Publisher carries live log lines to the SSE hub (BE-4.11).
//
// Satisfied by LogWriter, which the stage opens per run. Declared as an interface so
// the stage can be exercised without a database behind it.
type Publisher interface {
	PublishRunLog(ctx context.Context, runID uuid.UUID, line string)
}

// ExecuteHandler runs one suite.
type ExecuteHandler struct {
	deps ExecuteDeps
}

func NewExecuteHandler(deps ExecuteDeps) *ExecuteHandler { return &ExecuteHandler{deps: deps} }

func (h *ExecuteHandler) Type() string { return TypeExecute }

// IdempotencyKey is the run, not the task. A redelivered task for the same run must
// not produce a second container or a second set of results (BE-4.9.5).
func (h *ExecuteHandler) IdempotencyKey(payload jobs.StagePayload) string {
	return ExecuteIdempotencyKey(payload)
}

// ExecuteIdempotencyKey is the same rule, exported so the API process can declare
// the type as enqueue-only without constructing a handler it cannot run.
func ExecuteIdempotencyKey(payload jobs.StagePayload) string {
	if payload.RunID != nil {
		return fmt.Sprintf("%s:run:%s", payload.Chain, *payload.RunID)
	}
	return fmt.Sprintf("%s:%s:%d", payload.Chain, payload.ParentJobID, payload.Stage)
}

func (h *ExecuteHandler) Handle(ctx context.Context, payload jobs.StagePayload, jc jobs.JobContext) error {
	if payload.RunID == nil {
		return apierr.Validation("This chain needs a run to execute.", nil)
	}
	runID := *payload.RunID

	run, err := h.deps.Runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status.Terminal() {
		// Cancelled or already finished while the task sat in the queue. Nothing to
		// do, and starting a container now would be a run nobody asked for.
		jc.Event("Run %s is already %s, so nothing was executed", runID, run.Status)
		return nil
	}

	// The slot is taken before anything expensive happens, and the wait is reported:
	// a queued run that says nothing looks broken (BE-4.10.2).
	release, err := h.acquireSlot(ctx, jc)
	if err != nil {
		return err
	}
	defer release()

	files, err := h.deps.Files.All(ctx, run.ProjectID, run.Framework)
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the suite: %w", err))
	}
	if len(files) == 0 {
		return h.finishEmpty(ctx, run, jc,
			fmt.Sprintf("This project has no %s files to run. Generate a suite first.", run.Framework))
	}

	// Re-checked at start, not trusted from enqueue time: the firewall rules are
	// built from the addresses that are true now (BE-4.7.3).
	target, err := h.deps.Targets.CheckURL(ctx, run.ProjectID, run.TargetURL)
	if err != nil {
		return h.failRun(ctx, run, jc, err)
	}

	limits, err := h.deps.Runs.Limits(ctx, run.ProjectID)
	if err != nil {
		return err
	}

	runtime, err := h.deps.Runs.Runtime(ctx)
	if err != nil {
		return err
	}
	run.Runtime = runtime

	if err := h.deps.Runs.MarkStarted(ctx, runID, run.Image); err != nil {
		// Something else already took it, or it was cancelled. Not this stage's
		// problem to force.
		jc.Event("Run %s was not startable: %v", runID, err)
		return nil
	}
	jc.Event("Running %d %s file(s) against %s", len(files), run.Framework, target.Host)
	jc.Progress(10)

	// One writer per run, closed when the run ends: it batches output into
	// run_log_lines and wakes every watching stream (BE-4.11).
	//
	// The writer's own flush runs on a detached context by design (logs.go): a
	// cancelled run's last lines are the ones that explain the cancellation.
	live := h.deps.Runs.LogWriterFor(run.ID, run.JobID) //nolint:contextcheck // see above
	defer live.Close()

	// Cancellation crosses processes: the API marks the row and this watches for it,
	// so a cancel works even when the request never reaches the worker holding the
	// container (BE-4.12).
	runCtx, stopWatching := h.watchForCancel(ctx, run.ID, jc)
	defer stopWatching()
	ctx = runCtx

	started := time.Now()
	execution, err := h.execute(ctx, run, target, limits, files, live, jc)
	if err != nil {
		switch {
		case errors.Is(err, runner.ErrUnavailable):
			// Retryable: the runtime being down is not the suite's fault, so the run
			// stays queued for another attempt rather than being reported as failed.
			return apierr.RunnerUnavailable().WithCause(err)

		case errors.Is(err, context.Canceled):
			// Cancelled, by this run's own watchdog or by shutdown. Whatever the
			// container produced before it was stopped is kept and labelled, because a
			// run that got halfway still tells you something (BE-4.12.3).
			return h.finishCancelled(ctx, run, started, execution, jc)

		default:
			return h.failRun(ctx, run, jc, err)
		}
	}
	jc.Progress(85)

	// Flushed before the log is stored so the stored copy and the live tail agree.
	live.Close()
	logKey := h.storeLog(ctx, run, execution.Logs)

	counts := countResults(execution.Results)
	status := StatusFor(counts)
	if execution.Killed {
		status = StatusError
	}

	// The run's error column is for a run that could not report, not for a run that
	// reported failures. A suite exiting non-zero because three tests failed is fully
	// described by those three rows, and copying "the suite exited 1" onto the run as
	// well produces a passed run carrying an error, which is a contradiction a reader
	// has to resolve.
	reason := ""
	if execution.Killed || counts.Total == 0 {
		reason = execution.Reason
	}

	metrics := execution.Metrics
	if metrics != nil {
		// The wall clock is a truer duration than k6's own for the load phase, and the
		// summary export does not carry a reliable one anyway (BE-9.1.3).
		metrics.DurationMs = time.Since(started).Milliseconds()
	}

	finished, err := h.deps.Runs.Finish(ctx, runID, Outcome{
		Status:   status,
		Duration: time.Since(started),
		Error:    reason,
		LogKey:   logKey,
		Results:  execution.Results,
		Metrics:  metrics,
	})
	if err != nil {
		return err
	}

	if logKey != "" {
		// The tail is only pruned once the archive exists. If the upload failed, those
		// rows are the only copy of the output.
		if err := h.deps.Runs.PruneLogLines(ctx, runID); err != nil {
			slog.WarnContext(ctx, "prune the run log tail", "run_id", runID, "error", err)
		}
	}

	jc.Event("Run %s: %d passed, %d failed, %d flaky, %d skipped, %d quarantined in %s",
		status, finished.Passed, finished.Failed, finished.Flaky, finished.Skipped,
		finished.Quarantined, finished.Duration.Round(time.Millisecond))
	jc.Progress(100)

	// After the run rather than during it: the arithmetic reads the finished runs,
	// and this run has to be one of them or a test that just flaked for the third time
	// would be counted as having flaked twice (BE-7.6.1).
	h.quarantineFlakes(ctx, finished, jc)
	h.analyseFailures(ctx, finished, jc)
	return nil
}

// quarantineFlakes excuses the tests that have flaked too often.
//
// Automatic, because a policy that needs somebody to notice and act is a policy that
// runs when somebody has time — and a suite nobody trusts is exactly what happens in
// the meantime (F-7.12). The threshold and the window come from settings, and the
// decision is recorded with the arithmetic behind it, so "three of the last ten runs"
// is a sentence a reviewer can check rather than a score they have to believe.
func (h *ExecuteHandler) quarantineFlakes(ctx context.Context, run Run, jc jobs.JobContext) {
	policy, err := h.deps.Runs.QuarantinePolicyFor(ctx, run.ProjectID)
	if err != nil {
		slog.WarnContext(ctx, "read the quarantine policy", "run_id", run.ID, "error", err)
		return
	}
	if !policy.Enabled {
		return
	}

	candidates, err := h.deps.Runs.FlakeCandidates(ctx, run.ProjectID, policy.Window)
	if err != nil {
		slog.WarnContext(ctx, "count flakes", "run_id", run.ID, "error", err)
		return
	}

	excused, err := h.deps.Runs.ActiveQuarantines(ctx, run.ProjectID)
	if err != nil {
		slog.WarnContext(ctx, "read the quarantine list", "run_id", run.ID, "error", err)
		return
	}

	for _, candidate := range candidates {
		if candidate.FlakyRuns < policy.Threshold {
			continue
		}
		if _, already := excused[candidate.TestName]; already {
			// Already excused. The row's counts are refreshed rather than left stale, so a
			// reviewer sees whether it is still flaking, and the age stays the age of the
			// original decision.
			h.refreshQuarantine(ctx, run, candidate, policy)
			continue
		}

		reason := fmt.Sprintf("Flaked in %d of the last %d runs, above the threshold of %d.",
			candidate.FlakyRuns, candidate.SeenRuns, policy.Threshold)

		quarantine, err := h.deps.Runs.QuarantineTest(ctx, QuarantineInput{
			ProjectID:  run.ProjectID,
			TestName:   candidate.TestName,
			Reason:     reason,
			FlakeCount: candidate.FlakyRuns,
			WindowRuns: candidate.SeenRuns,
			Source:     QuarantineAuto,
		})
		if err != nil {
			slog.WarnContext(ctx, "quarantine a flaky test",
				"run_id", run.ID, "test", candidate.TestName, "error", err)
			continue
		}

		// Said in the run's own log, because a test that silently stops failing is the
		// thing this feature could most easily become.
		jc.Event("Quarantined %q: %s It will keep running and keep recording results, "+
			"and it needs an owner (quarantine %s)",
			candidate.TestName, reason, quarantine.ID)
	}
}

// refreshQuarantine updates the counts on a quarantine that is still flaking.
func (h *ExecuteHandler) refreshQuarantine(
	ctx context.Context,
	run Run,
	candidate FlakeCandidate,
	policy QuarantinePolicy,
) {
	reason := fmt.Sprintf("Still flaking: %d of the last %d runs, threshold %d.",
		candidate.FlakyRuns, candidate.SeenRuns, policy.Threshold)

	if _, err := h.deps.Runs.QuarantineTest(ctx, QuarantineInput{
		ProjectID:  run.ProjectID,
		TestName:   candidate.TestName,
		Reason:     reason,
		FlakeCount: candidate.FlakyRuns,
		WindowRuns: candidate.SeenRuns,
		Source:     QuarantineAuto,
	}); err != nil {
		slog.WarnContext(ctx, "refresh a quarantine",
			"run_id", run.ID, "test", candidate.TestName, "error", err)
	}
}

// analyseFailures starts the analysis chain when a run produced something to explain.
//
// A separate chain rather than a stage of this one, for two reasons: the analysis is
// worth retrying without re-running the suite, and a run whose analysis fails is
// still a run whose results are correct (F-9.1).
func (h *ExecuteHandler) analyseFailures(ctx context.Context, run Run, jc jobs.JobContext) {
	if h.deps.Chains == nil || run.Failed+run.Flaky == 0 {
		return
	}

	auto, err := h.deps.Settings.Bool(ctx, "analysis.auto_analyse",
		settings.Target{ProjectID: &run.ProjectID})
	if err != nil {
		slog.WarnContext(ctx, "read the auto-analyse setting", "run_id", run.ID, "error", err)
		return
	}
	if !auto {
		jc.Event("Automatic analysis is off, so the %d failure(s) are waiting to be explained",
			run.Failed+run.Flaky)
		return
	}

	runID := run.ID
	if _, err := h.deps.Chains.SubmitTriggered(ctx, runtrigger.Request{
		ProjectID: run.ProjectID,
		Chain:     string(jobs.ChainAnalyse),
		Source:    runtrigger.KindSchedule,
		RunID:     &runID,
		Reference: fmt.Sprintf("run %s", run.ID),
	}); err != nil {
		// Not fatal to the run: the results are stored and correct, and an analysis
		// that did not start can be asked for again.
		jc.Event("The failures could not be queued for analysis: %v", err)
		slog.WarnContext(ctx, "submit the analyse chain", "run_id", run.ID, "error", err)
		return
	}

	jc.Event("Queued %d failure(s) for analysis", run.Failed+run.Flaky)
}

// cancelPoll is how often the stage checks whether its run was cancelled. Seconds,
// not minutes: "cancelling stops the container within seconds" is the requirement.
const cancelPoll = 2 * time.Second

// watchForCancel returns a context that ends when the run's row leaves the running
// state.
//
// Polling rather than listening, because this has to work when the notification
// path is the thing that is broken, and because a two-second poll on one row is
// cheaper than the machinery to avoid it.
func (h *ExecuteHandler) watchForCancel(
	ctx context.Context,
	runID uuid.UUID,
	jc jobs.JobContext,
) (context.Context, func()) {
	watched, cancel := context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(cancelPoll)
		defer ticker.Stop()

		for {
			select {
			case <-watched.Done():
				return
			case <-ticker.C:
				current, err := h.deps.Runs.Get(watched, runID)
				if err != nil {
					// A read failure is not a cancellation. The run continues and the
					// driver's own wall clock still bounds it.
					continue
				}
				if current.Status == StatusCancelled {
					jc.Event("Cancelled: stopping the container")
					cancel()
					return
				}
			}
		}
	}()

	return watched, cancel
}

// acquireSlot waits for the global concurrency limit.
func (h *ExecuteHandler) acquireSlot(ctx context.Context, jc jobs.JobContext) (func(), error) {
	if h.deps.Slots == nil {
		return func() {}, nil
	}

	if waiting := h.deps.Slots.Waiting(); waiting > 0 {
		jc.Event("Waiting for a runner slot, %d run(s) ahead", waiting)
	}

	release, err := h.deps.Slots.Acquire(ctx)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("wait for a runner slot: %w", err))
	}
	return release, nil
}

// execution is what one run produced, across attempts.
type execution struct {
	Results []Result
	Logs    string
	Killed  bool
	Reason  string

	// Metrics is set by a performance run's report, and nil for a functional one.
	Metrics *Metrics
}

// execute runs the suite, then re-runs what failed.
func (h *ExecuteHandler) execute(
	ctx context.Context,
	run Run,
	target targets.Target,
	limits runner.Limits,
	files []testfiles.File,
	live Publisher,
	jc jobs.JobContext,
) (execution, error) {
	retries, err := h.deps.Settings.Int(ctx, "runner.retry_failed_tests",
		settings.Target{ProjectID: &run.ProjectID})
	if err != nil {
		return execution{}, apierr.Internal(fmt.Errorf("read the retry count: %w", err))
	}

	// Read once per run, not per attempt: it is a decrypted secret, and the fewer
	// times it is read the fewer places it can be logged from.
	credential, err := h.deps.Runs.TargetCredential(ctx, run.ProjectID)
	if err != nil {
		return execution{}, err
	}

	// A browser suite signs in through a form, which needs an account rather than a
	// token. Read once per run for the same reason the credential is: the fewer times a
	// secret is read, the fewer places it can be logged from (BE-7.3.2).
	username, password, err := h.deps.Runs.UILogin(ctx, run.ProjectID)
	if err != nil {
		return execution{}, err
	}
	login := UILogin{Username: username, Password: password}

	// Read once per run rather than per result: a four-hundred-test suite would
	// otherwise ask the database four hundred times for a list that cannot change
	// mid-run (BE-7.6.2).
	excused, err := h.deps.Runs.ActiveQuarantines(ctx, run.ProjectID)
	if err != nil {
		return execution{}, err
	}
	if len(excused) > 0 {
		jc.Event("%d test(s) are quarantined and will not fail this run", len(excused))
	}

	byName := map[string][]Result{}
	logs := &strings.Builder{}
	outcome := execution{}

	for attempt := 1; attempt <= retries+1; attempt++ {
		only := failedNames(byName)
		if attempt > 1 {
			if len(only) == 0 {
				break
			}
			jc.Event("Retrying %d failing test(s), attempt %d", len(only), attempt)
		}

		result, err := h.runOnce(ctx, run, target, limits, files, attempt, only,
			credential, login, excused, live, jc)
		if err != nil {
			return execution{}, err
		}

		logs.WriteString(result.Logs)
		outcome.Killed = outcome.Killed || result.Killed
		if result.Reason != "" {
			outcome.Reason = result.Reason
		}

		for _, item := range result.Results {
			byName[item.Name] = append(byName[item.Name], item)
		}

		// A killed run is not retried: whatever the limit was, it will be hit again,
		// and the second container costs the same as the first.
		if result.Killed {
			break
		}
	}

	outcome.Results = flatten(byName)
	outcome.Logs = logs.String()
	return outcome, nil
}

// runOnce is one container.
func (h *ExecuteHandler) runOnce(
	ctx context.Context,
	run Run,
	target targets.Target,
	limits runner.Limits,
	files []testfiles.File,
	attempt int,
	only []string,
	credential string,
	login UILogin,
	excused map[string]uuid.UUID,
	live Publisher,
	jc jobs.JobContext,
) (execution, error) {
	workspace := make(map[string]string, len(files))
	fileIDs := make(map[string]uuid.UUID, len(files))
	caseIDs := make(map[string][]uuid.UUID, len(files))
	for _, file := range files {
		workspace[file.Path] = file.Content
		fileIDs[file.Path] = file.ID
		caseIDs[file.Path] = file.TestCaseIDs
	}

	spec := runner.Spec{
		RunID:     fmt.Sprintf("%s-%d", run.ID, attempt),
		Image:     run.Image,
		Command:   commandFor(run.Framework, only),
		Workspace: workspace,
		Env:       environmentFor(target, credential, login),
		Limits:    limits,
		Runtime:   run.Runtime,
		Reports:   []string{ReportPath, ArtifactsPath},
		Egress: runner.Egress{AllowedHosts: []runner.HostAddress{{
			Host: target.Host,
			IP:   target.Primary().String(),
		}}},
	}

	// The command is logged before it runs, redacted, so a run that never returns is
	// still reconstructable (BE-4.6).
	commandStarted := time.Now()
	if err := h.deps.Runs.RecordCommand(ctx, run.ID, Command{
		Command: runner.RedactedCommand(spec.Command, spec.Env),
	}); err != nil {
		slog.WarnContext(ctx, "record the run command", "run_id", run.ID, "error", err)
	}

	stream := &publishWriter{
		ctx:       ctx,
		runID:     run.ID,
		publisher: live,
		events:    jc,
	}
	result, err := h.deps.Driver.Run(ctx, spec, stream)

	// Flushed whether the run succeeded or not: the last line of a failed run is
	// usually the one worth reading, and it rarely ends with a newline.
	stream.Flush()

	if err != nil {
		return execution{}, err
	}

	exit := result.ExitCode
	if err := h.deps.Runs.RecordCommand(ctx, run.ID, Command{
		Command:       runner.RedactedCommand(spec.Command, spec.Env),
		ExitCode:      &exit,
		Duration:      time.Since(commandStarted),
		OutputExcerpt: tail(result.Logs, 4<<10),
	}); err != nil {
		slog.WarnContext(ctx, "record the run outcome", "run_id", run.ID, "error", err)
	}

	outcome := execution{
		Logs:   result.Logs,
		Killed: result.Killed(),
		Reason: result.Reason(),
	}

	raw, found := result.Reports[ReportPath]
	if !found {
		if result.Killed() {
			// The container was stopped before it could write anything. The reason is
			// already recorded and there is nothing to parse.
			return outcome, nil
		}
		return outcome, fmt.Errorf("the runner produced no report: %s", tail(result.Logs, 2<<10))
	}

	report, err := ParseReport(raw)
	if err != nil {
		return outcome, err
	}

	// A retry names the tests to re-run, and a framework that filters still reports
	// the ones it skipped. Those are dropped here: a test that already passed does
	// not need a skipped row from every later attempt, and keeping them would make
	// the results table mostly noise on a suite with one flaky test.
	retried := map[string]bool{}
	for _, name := range only {
		retried[name] = true
	}

	for _, item := range report.Results {
		if attempt > 1 && len(retried) > 0 && !retried[item.Name] {
			continue
		}

		result := Result{
			RunID:          run.ID,
			Name:           item.Name,
			Status:         resultStatus(item.Status),
			Duration:       time.Duration(item.DurationMs) * time.Millisecond,
			Attempt:        attempt,
			FailureMessage: item.FailureMessage,

			// The status still says what happened. This flag is what stops it failing
			// the run, and it is stored so a report can say "excused" rather than
			// leaving somebody to work out why a red test did not turn the run red.
			Quarantined: quarantined(excused, item.Name),
		}
		if item.Attempt > 0 && attempt == 1 {
			// A framework that retries internally, such as Playwright, reports its own
			// attempt numbers. Those are kept: they are the same fact this stage is
			// recording.
			result.Attempt = item.Attempt
		}
		if id, ok := fileIDs[item.File]; ok {
			result.TestFileID = &id
		}
		if cases := caseIDs[item.File]; len(cases) == 1 {
			// A file that implements exactly one case maps unambiguously. A file with
			// several is left unmapped rather than guessed: a wrong mapping is worse
			// than none, because it puts a passing result on a case that never ran
			// (BE-4.9.2).
			result.TestCaseID = &cases[0]
		}
		outcome.Results = append(outcome.Results, result)
	}

	// A performance run's report carries the measured series. Kept on the outcome so
	// Finish can write it in the same transaction as the counts (BE-9.1.3).
	if report.Metrics != nil {
		outcome.Metrics = report.Metrics
	}

	// The recordings come through the same channel as the report and are stored before
	// the results are written, because the keys travel on the result rows: uploading
	// afterwards would mean a second pass over a table that has already been read
	// (BE-7.5.2).
	if manifest, found := result.Reports[ArtifactsPath]; found {
		h.storeArtifacts(ctx, run, manifest, outcome.Results, jc)
	}

	return outcome, nil
}

// storeArtifacts puts each recording in object storage and stamps its key on the
// result it belongs to.
//
// Best effort, like the log: a run whose video could not be stored is still a run with
// results, and losing the evidence for one failure is not a reason to lose the outcome
// of the suite. What it will not do is stay quiet about it — a dropped artifact is a
// line in the run's own log, because "there is no video" and "the video was too big"
// are different facts to somebody looking for one.
func (h *ExecuteHandler) storeArtifacts(
	ctx context.Context,
	run Run,
	manifest []byte,
	results []Result,
	jc jobs.JobContext,
) {
	if h.deps.Objects == nil {
		return
	}

	parsed, err := ParseArtifacts(manifest)
	if err != nil {
		slog.WarnContext(ctx, "read the artifact manifest", "run_id", run.ID, "error", err)
		return
	}

	for _, dropped := range parsed.Dropped {
		jc.Event("No %s for %q attempt %d: %s",
			dropped.Kind, dropped.Test, dropped.Attempt, dropped.Why)
	}
	if len(parsed.Items) == 0 {
		return
	}

	stored := 0
	for _, item := range parsed.Items {
		target := matchResult(results, item)
		if target == nil {
			// A recording whose test is not in the results is a recording nobody can reach
			// through the UI, so storing it would spend space for nothing.
			slog.DebugContext(ctx, "an artifact matched no result",
				"run_id", run.ID, "test", item.Test, "attempt", item.Attempt, "kind", item.Kind)
			continue
		}

		content, err := item.Decode()
		if err != nil {
			slog.WarnContext(ctx, "decode an artifact", "run_id", run.ID, "error", err)
			continue
		}

		key := objectstore.Key(run.ProjectID, "runs",
			run.ID, fmt.Sprintf("%s-%d-%s", item.Kind, item.Attempt, item.Name))

		if _, err := h.deps.Objects.Put(ctx, key, bytes.NewReader(content),
			objectstore.PutOptions{ContentType: item.ContentType, Size: int64(len(content))}); err != nil {
			slog.WarnContext(ctx, "store an artifact",
				"run_id", run.ID, "kind", item.Kind, "error", err)
			continue
		}

		switch item.Kind {
		case ArtifactVideo:
			target.VideoKey = key
		case ArtifactTrace:
			target.TraceKey = key
		case ArtifactScreenshot:
			target.ScreenshotKey = key
		default:
			// A kind this platform does not have a column for is stored and then
			// unreferenced, which is worse than not storing it: it would be a file nothing
			// can ever fetch or expire.
			slog.DebugContext(ctx, "an artifact kind this platform does not record",
				"run_id", run.ID, "kind", item.Kind)
			continue
		}
		stored++
	}

	if stored > 0 {
		jc.Event("Stored %d artifact(s) from failing tests", stored)
	}
}

// matchResult finds the result a recording belongs to.
//
// By name and attempt, because both come from the same reporter output as the report
// itself. A pointer into the slice, so the key lands on the row that is about to be
// written rather than on a copy.
func matchResult(results []Result, item ArtifactItem) *Result {
	for index := range results {
		if results[index].Name == item.Test && results[index].Attempt == item.Attempt {
			return &results[index]
		}
	}

	// Playwright numbers its own retries and the platform numbers its attempts, and a
	// suite run without internal retries reports attempt 1 for everything. Falling back
	// to the name alone keeps the evidence attached in that case, which is the common
	// one.
	for index := range results {
		if results[index].Name == item.Test {
			return &results[index]
		}
	}
	return nil
}

// quarantined reports whether a test is on the project's excused list.
//
// Matched on the exact name the report carried, because that is the same name the
// quarantine was recorded from: a fuzzy match would excuse a test nobody decided to
// excuse, which is the one mistake this feature cannot afford.
func quarantined(excused map[string]uuid.UUID, name string) bool {
	if len(excused) == 0 {
		return false
	}
	_, found := excused[name]
	return found
}

// commandFor is the runner image's entry point plus a filter for a retry.
//
// The images take the same two verbs, so this does not branch per framework beyond
// naming the entry file a load script needs.
func commandFor(framework testfiles.Framework, only []string) []string {
	command := []string{"qavia-run", "test"}

	for _, name := range only {
		// Retries name the tests to re-run rather than the files, because a file
		// usually holds several tests and re-running all of them would report a
		// passing neighbour twice.
		switch framework {
		case testfiles.FrameworkPytest:
			command = append(command, "-k", name)
		case testfiles.FrameworkK6:
			// A load script is one script: there is nothing to filter, so a retry
			// re-runs it whole.
		default:
			command = append(command, "-t", name)
		}
	}
	return command
}

// environmentFor is how a suite learns its target and its credential.
//
// The names are the ones the exported scaffolding documents (internal/testfiles
// export.go), and that is the point: a suite that runs here runs the same way on a
// developer's machine with the same two variables, because nothing about the target
// is baked into the generated files.
//
// Environment, not command line: a credential on a command line ends up in the
// command log, and the command log is a table people export.
// UILogin is the account a browser suite signs in as, carried into the container's
// environment and never into a generated file.
type UILogin struct {
	Username string
	Password string
}

func environmentFor(target targets.Target, credential string, login UILogin) map[string]string {
	env := map[string]string{
		"QAVIA_TARGET_URL": target.URL,
		"CI":               "true",
		"NODE_ENV":         "test",

		// The entry script for a k6 load run, which is one script rather than a
		// directory of tests.
		"QAVIA_ENTRY_FILE": "script.js",
	}
	if credential != "" {
		env["QAVIA_AUTH_TOKEN"] = credential
	}

	// The names a generated Playwright spec reads, which are the same names the browser
	// session substitutes during discovery: a spec written from a discovered flow runs
	// with the same account that walked it (BE-7.3.2, BE-7.4).
	if login.Username != "" {
		env["QAVIA_AUTH_USERNAME"] = login.Username
	}
	if login.Password != "" {
		env["QAVIA_AUTH_PASSWORD"] = login.Password
	}
	return env
}

// failedNames lists the tests that have not passed on any attempt so far.
func failedNames(byName map[string][]Result) []string {
	var failing []string
	for name, attempts := range byName {
		passed := false
		for _, attempt := range attempts {
			if attempt.Status == ResultPassed || attempt.Status == ResultSkipped {
				passed = true
				break
			}
		}
		if !passed {
			failing = append(failing, name)
		}
	}
	return failing
}

// flatten returns every attempt of every test, which is what the results table
// stores: a flaky result has to be inspectable attempt by attempt (BE-4.13.4).
func flatten(byName map[string][]Result) []Result {
	var all []Result
	for _, attempts := range byName {
		all = append(all, attempts...)
	}
	return all
}

// finishCancelled records a cancelled run, keeping whatever ran before the stop.
//
// Written on a context that is already cancelled, so it uses its own: the row has to
// be updated precisely because the run's context ended.
func (h *ExecuteHandler) finishCancelled(
	ctx context.Context,
	run Run,
	started time.Time,
	partial execution,
	jc jobs.JobContext,
) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	reason := "Cancelled before the suite finished."
	if partial.Reason != "" {
		reason = partial.Reason
	}

	if _, err := h.deps.Runs.Finish(writeCtx, run.ID, Outcome{
		Status:   StatusCancelled,
		Duration: time.Since(started),
		Error:    reason,
		Results:  partial.Results,
	}); err != nil {
		return err
	}

	jc.Event("Cancelled after %s, %d partial result(s) kept",
		time.Since(started).Round(time.Second), len(partial.Results))
	return nil
}

// finishEmpty records a run that had nothing to execute. Not an error in the
// platform, and not a pass either: zero tests passing is the outcome most likely to
// be misread as success.
func (h *ExecuteHandler) finishEmpty(
	ctx context.Context,
	run Run,
	jc jobs.JobContext,
	reason string,
) error {
	jc.Event("%s", reason)
	if _, err := h.deps.Runs.Finish(ctx, run.ID, Outcome{
		Status: StatusError,
		Error:  reason,
	}); err != nil {
		return err
	}
	return nil
}

// failRun records a run that could not execute, with the reason a user can act on.
func (h *ExecuteHandler) failRun(ctx context.Context, run Run, jc jobs.JobContext, cause error) error {
	reason := cause.Error()
	var domain *apierr.Error
	if errors.As(cause, &domain) {
		reason = domain.Message
	}

	jc.Event("Run failed: %s", reason)
	if _, err := h.deps.Runs.Finish(ctx, run.ID, Outcome{
		Status: StatusError,
		Error:  reason,
	}); err != nil {
		return err
	}

	// The run row carries the failure, so the job itself succeeded at what it was
	// asked to do. Returning the error here would retry a run that will fail the
	// same way, and hide the reason behind a retry count.
	return nil
}

// storeLog puts the full log in object storage and returns its key.
//
// Best effort: a run whose log could not be stored is still a run with results, and
// losing the log is not a reason to lose them.
func (h *ExecuteHandler) storeLog(ctx context.Context, run Run, logs string) string {
	if h.deps.Objects == nil || strings.TrimSpace(logs) == "" {
		return ""
	}

	key := objectstore.Key(run.ProjectID, "runs", run.ID, "run.log")
	if _, err := h.deps.Objects.Put(ctx, key, strings.NewReader(logs), objectstore.PutOptions{
		ContentType: "text/plain; charset=utf-8",
		Size:        int64(len(logs)),
	}); err != nil {
		slog.WarnContext(ctx, "store the run log", "run_id", run.ID, "error", err)
		return ""
	}
	return key
}

// tail returns the last n bytes, which is where a failure's reason lives.
func tail(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return "…" + text[len(text)-n:]
}
