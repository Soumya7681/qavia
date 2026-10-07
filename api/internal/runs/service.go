package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	Int64(ctx context.Context, key string, target settings.Target) (int64, error)
	Float(ctx context.Context, key string, target settings.Target) (float64, error)
	Bool(ctx context.Context, key string, target settings.Target) (bool, error)
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Duration(ctx context.Context, key string, target settings.Target) (time.Duration, error)

	// Secret is the only way a credential reaches a run, and it is read once per run
	// rather than per attempt.
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// Targets is the allowlist and SSRF check, which runs before a run is enqueued.
type Targets interface {
	Check(ctx context.Context, projectID uuid.UUID) (targets.Target, error)
	CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (targets.Target, error)
}

// Service owns run rows, their results, and their command log.
type Service struct {
	db       *store.DB
	settings Settings
	targets  Targets

	// objects is optional: a process that never reads a run's log back does not need
	// it, and the API process does.
	objects objectstore.Store

	// notifier wakes log streams when a batch of lines lands. Optional: the API
	// process sets it because it serves the streams, and a worker sets it because it
	// writes the lines. Without one the stream still updates, on its poll interval.
	notifier LogNotifier
}

// Option configures the service. Variadic so a process that does not serve log
// streams stays a three-argument call.
type Option func(*Service)

// WithLogNotifier attaches the hub that wakes live log streams.
func WithLogNotifier(notifier LogNotifier) Option {
	return func(s *Service) { s.notifier = notifier }
}

// WithObjectStore lets the service read a finished run's log back, which the analysis
// stage needs and the execute stage does not.
func WithObjectStore(objects objectstore.Store) Option {
	return func(s *Service) { s.objects = objects }
}

func NewService(db *store.DB, config Settings, allowlist Targets, options ...Option) *Service {
	service := &Service{db: db, settings: config, targets: allowlist}
	for _, option := range options {
		option(service)
	}
	return service
}

// LogWriterFor opens a batching writer for one run's output. The caller closes it.
func (s *Service) LogWriterFor(runID uuid.UUID, jobID *uuid.UUID) *LogWriter {
	return NewLogWriter(s.db, s.notifier, runID, jobID)
}

// Trigger, Status, and ResultStatus mirror the enums, checked before a write so a
// bad value is a validation error naming the field rather than a constraint
// violation surfacing as a 500.
type (
	Trigger      string
	Status       string
	ResultStatus string
	Kind         string
)

const (
	// KindFunctional is an ordinary suite. KindPerformance is a load run, and
	// KindSecurity a probe run: all three share the execution boundary and differ in
	// what they produce (BE-9.1).
	KindFunctional  Kind = "functional"
	KindPerformance Kind = "performance"
	KindSecurity    Kind = "security"
)

const (
	TriggerManual   Trigger = "manual"
	TriggerWebhook  Trigger = "webhook"
	TriggerSchedule Trigger = "schedule"
	TriggerAPI      Trigger = "api"
)

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPassed    Status = "passed"
	StatusFailed    Status = "failed"
	StatusError     Status = "errored"
	StatusCancelled Status = "cancelled"
)

const (
	ResultPassed  ResultStatus = "passed"
	ResultFailed  ResultStatus = "failed"
	ResultFlaky   ResultStatus = "flaky"
	ResultSkipped ResultStatus = "skipped"
	ResultError   ResultStatus = "errored"
)

// Terminal reports whether a status is final. A run that is terminal cannot be
// cancelled, and its counts will not change again.
func (s Status) Terminal() bool {
	switch s {
	case StatusPassed, StatusFailed, StatusError, StatusCancelled:
		return true
	default:
		return false
	}
}

// Run is one execution.
type Run struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	JobID     *uuid.UUID

	TargetURL string
	Trigger   Trigger
	Status    Status
	Framework testfiles.Framework

	// Image is the digest-pinned reference that executed, recorded so a result can
	// be traced to the exact image that produced it (BE-4.3).
	Image string

	Total, Passed, Failed, Flaky, Skipped int

	// Kind separates a functional run from a performance or security one.
	Kind Kind

	// LoadProfile is the concurrency and duration a performance run used, raw so the
	// caller decodes it into its own type.
	LoadProfile json.RawMessage

	// Quarantined is how many tests the project excused this run. Its own number
	// rather than folded into passed: a suite where six tests are excused is a
	// different thing from one where they all pass (BE-7.6).
	Quarantined int

	Duration time.Duration

	Error  string
	LogKey string

	TriggeredBy *uuid.UUID
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time

	// Runtime is filled in by the execute stage from settings. It is not a column:
	// the image is what identifies what ran, and the runtime is a property of the
	// host, not of the run.
	Runtime runner.Runtime
}

// Result is one test's outcome in one run.
type Result struct {
	ID         uuid.UUID
	RunID      uuid.UUID
	TestCaseID *uuid.UUID
	TestFileID *uuid.UUID

	Name     string
	Status   ResultStatus
	Duration time.Duration

	// Attempt is 1 for the first run of a test and increments per retry, so a flaky
	// result's attempts are individually inspectable (BE-4.13).
	Attempt int

	FailureMessage string
	LogKey         string
	ScreenshotKey  string
	VideoKey       string

	// TraceKey is a Playwright trace: for a UI failure it is usually the most useful
	// of the three, because it shows what the page looked like at each step rather
	// than at the end (BE-7.5).
	TraceKey string

	// Quarantined marks a result the project excuses. The status still says what
	// actually happened — a quarantined test that failed is recorded as failed — and
	// this is what stops it failing the run (BE-7.6.2).
	Quarantined bool

	CreatedAt time.Time
}

// Command is one command the runner executed, for reconstruction (F-17.2).
type Command struct {
	ID            int64
	RunID         uuid.UUID
	Command       string
	ExitCode      *int
	Duration      time.Duration
	OutputExcerpt string
	At            time.Time
}

// CreateInput is a queued run, before anything has executed.
type CreateInput struct {
	ProjectID uuid.UUID
	Framework testfiles.Framework
	Trigger   Trigger

	// Kind separates a functional run from a performance or a security one. They share
	// the execution boundary and differ in what they produce and how they are read
	// (BE-9.1). Empty defaults to functional.
	Kind Kind

	// LoadProfile is the concurrency and duration a performance run executed with, kept
	// so a latency series can be read against the load that produced it.
	LoadProfile json.RawMessage

	// TargetURL overrides the project's configured target. Checked against the
	// allowlist like everything else, because an override is exactly the input that
	// must not be trusted.
	TargetURL string

	TriggeredBy *uuid.UUID
	JobID       *uuid.UUID
}

// Create checks the target, enforces the per-project cap, and writes the run row.
//
// The row exists before the job is pushed, which is the platform's rule for
// anything queued: a row with no task is visible and retryable, and a task with no
// row is invisible (backend-standards.md 8).
func (s *Service) Create(ctx context.Context, input CreateInput) (Run, targets.Target, error) {
	if !input.Framework.Valid() {
		return Run{}, targets.Target{}, apierr.Validation(
			fmt.Sprintf("Framework %q is not one this platform runs.", input.Framework), nil)
	}

	target, err := s.resolveTarget(ctx, input)
	if err != nil {
		return Run{}, targets.Target{}, err
	}

	if err := s.checkProjectCapacity(ctx, input.ProjectID); err != nil {
		return Run{}, targets.Target{}, err
	}

	image, err := s.ImageFor(ctx, input.ProjectID, input.Framework)
	if err != nil {
		return Run{}, targets.Target{}, err
	}

	kind := input.Kind
	if kind == "" {
		kind = KindFunctional
	}
	profile := input.LoadProfile
	if len(profile) == 0 {
		profile = []byte("{}")
	}

	row, err := s.db.Queries().CreateRun(ctx, dbgen.CreateRunParams{
		ProjectID:   input.ProjectID,
		TargetUrl:   target.URL,
		Trigger:     dbgen.RunTrigger(input.Trigger),
		Framework:   dbgen.TestFramework(input.Framework),
		Image:       image,
		JobID:       input.JobID,
		TriggeredBy: input.TriggeredBy,
		Kind:        dbgen.RunKind(kind),
		LoadProfile: profile,
	})
	if err != nil {
		return Run{}, targets.Target{}, apierr.Internal(fmt.Errorf("create the run: %w", err))
	}

	return toRun(row), target, nil
}

// resolveTarget applies the allowlist. A project with no target is a clean refusal
// with its own code, because generation still works without one (BE-2.13).
func (s *Service) resolveTarget(ctx context.Context, input CreateInput) (targets.Target, error) {
	if strings.TrimSpace(input.TargetURL) != "" {
		return s.targets.CheckURL(ctx, input.ProjectID, input.TargetURL)
	}
	return s.targets.Check(ctx, input.ProjectID)
}

// checkProjectCapacity is the per-project half of BE-4.10. The global limit is a
// semaphore in the worker; this stops one project queueing enough runs to starve
// every other project behind them.
func (s *Service) checkProjectCapacity(ctx context.Context, projectID uuid.UUID) error {
	limit, err := s.settings.Int(ctx, "runner.max_runs_per_project",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the per-project run limit: %w", err))
	}
	if limit <= 0 {
		return nil
	}

	active, err := s.db.Queries().CountActiveRunsForProject(ctx, projectID)
	if err != nil {
		return apierr.Internal(fmt.Errorf("count active runs: %w", err))
	}
	if int(active) >= limit {
		return apierr.RunAlreadyActive(int(active), limit)
	}
	return nil
}

// ImageFor picks the runner image for a framework and refuses an unpinned reference
// when the platform is configured to require digests (BE-4.3).
//
// Exported because static validation runs in the same images (BE-3.4) and must not
// get to pick its own.
func (s *Service) ImageFor(
	ctx context.Context,
	projectID uuid.UUID,
	framework testfiles.Framework,
) (string, error) {
	scope := settings.Target{ProjectID: &projectID}

	key, ok := imageSettings[framework]
	if !ok {
		return "", apierr.Validation(
			fmt.Sprintf("There is no runner image for %s, so it cannot be executed here.", framework), nil)
	}

	image, err := s.settings.String(ctx, key, scope)
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the runner image: %w", err))
	}
	if strings.TrimSpace(image) == "" {
		return "", apierr.Validation(
			fmt.Sprintf("No runner image is configured for %s. Set %s in Settings, Runner.",
				framework, key), nil)
	}

	required, err := s.settings.Bool(ctx, "runner.require_digest", scope)
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the digest policy: %w", err))
	}
	if required && !strings.Contains(image, "@sha256:") {
		return "", apierr.Validation(
			fmt.Sprintf("Runner image %q is referenced by tag, and this platform requires a digest so a "+
				"result can be traced to the image that produced it. Pin it as name@sha256:… in "+
				"Settings, Runner, or turn off the digest requirement for local development.", image),
			map[string]any{"setting": key, "image": image})
	}

	return image, nil
}

// imageSettings maps a framework to the setting holding its image. Declared rather
// than derived, so adding a framework is a visible decision about which image runs
// it.
var imageSettings = map[testfiles.Framework]string{
	testfiles.FrameworkSupertest:  "runner.image_node",
	testfiles.FrameworkJest:       "runner.image_node",
	testfiles.FrameworkVitest:     "runner.image_node",
	testfiles.FrameworkPlaywright: "runner.image_playwright",
	testfiles.FrameworkCypress:    "runner.image_playwright",
	testfiles.FrameworkK6:         "runner.image_k6",
	testfiles.FrameworkPytest:     "runner.image_python",
	testfiles.FrameworkSecurity:   "runner.image_security",
}

// Limits reads the run's resource ceiling. Every value is a setting, so tightening
// a runner host is a settings change rather than a deploy (BE-4.5).
func (s *Service) Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error) {
	scope := settings.Target{ProjectID: &projectID}

	cpus, err := s.settings.Float(ctx, "runner.cpus", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the CPU limit: %w", err))
	}

	memory, err := s.settings.Int64(ctx, "runner.memory_mib", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the memory limit: %w", err))
	}
	pids, err := s.settings.Int64(ctx, "runner.pids", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the process limit: %w", err))
	}
	tmpfs, err := s.settings.Int64(ctx, "runner.tmpfs_mib", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the disk limit: %w", err))
	}
	timeout, err := s.settings.Duration(ctx, "runner.timeout", scope)
	if err != nil {
		return runner.Limits{}, apierr.Internal(fmt.Errorf("read the run wall clock: %w", err))
	}

	return runner.Limits{
		CPUs:      cpus,
		MemoryMiB: memory,
		PIDs:      pids,
		TmpfsMiB:  tmpfs,
		Timeout:   timeout,
	}, nil
}

// Runtime is the container runtime the platform is configured to use.
//
// Read per run rather than at boot, so an operator switching a runner host to gVisor
// takes effect on the next run instead of at the next deploy.
func (s *Service) Runtime(ctx context.Context) (runner.Runtime, error) {
	value, err := s.settings.String(ctx, "runner.runtime", settings.Target{})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the container runtime: %w", err))
	}
	return runner.RuntimeFor(value), nil
}

// TargetCredential is the token a suite authenticates to the target with.
//
// Empty is normal: a project whose target needs no authentication is a project with
// no credential, not a misconfigured one. The value is decrypted here and handed
// straight to the container's environment; it is never written to a log, a command
// row, or an error message.
func (s *Service) TargetCredential(ctx context.Context, projectID uuid.UUID) (string, error) {
	credential, found, err := s.settings.Secret(ctx, "targets.auth_credential",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the target credential: %w", err))
	}
	if !found {
		return "", nil
	}
	return credential, nil
}

// UILogin is the account a browser suite signs in as (BE-7.3.2).
//
// Separate from TargetCredential because the two are different things: a bearer token
// is transport authentication a suite sends with every request, and a UI login is a
// form somebody fills in. An application can want a session cookie from a login page
// while its API wants a signed token, and sharing one field would force an
// installation to choose which half of its tests work.
//
// Both values are decrypted here and handed straight to the container's environment.
// Empty is normal: a project whose target needs no sign-in has no login.
func (s *Service) UILogin(ctx context.Context, projectID uuid.UUID) (string, string, error) {
	scope := settings.Target{ProjectID: &projectID}

	username, err := s.settings.String(ctx, "ui.login_username", scope)
	if err != nil {
		return "", "", apierr.Internal(fmt.Errorf("read the UI login username: %w", err))
	}

	password, _, err := s.settings.Secret(ctx, "ui.login_password", scope)
	if err != nil {
		return "", "", apierr.Internal(fmt.Errorf("read the UI login password: %w", err))
	}
	return username, password, nil
}

// MarkStarted moves a queued run to running and records the image that is actually
// executing.
func (s *Service) MarkStarted(ctx context.Context, runID uuid.UUID, image string) error {
	affected, err := s.db.Queries().MarkRunStarted(ctx, dbgen.MarkRunStartedParams{
		ID: runID, Image: image,
	})
	if err != nil {
		return apierr.Internal(fmt.Errorf("mark the run started: %w", err))
	}
	if affected == 0 {
		// Already running, cancelled, or finished. The execute job treats this as a
		// reason to stop rather than to run a second container.
		return apierr.RunNotCancelable("no longer queued")
	}
	return nil
}

// Outcome is what a finished run looked like.
type Outcome struct {
	Status   Status
	Duration time.Duration
	Error    string
	LogKey   string

	Results []Result

	// Metrics is present for a performance run, stored beside the run so a latency
	// series is one row rather than a table of samples (BE-9.1.3).
	Metrics *Metrics
}

// Finish writes the results and the run's counts in one transaction.
//
// One transaction and one CopyFrom, after the run rather than during it: a
// transaction held open for the length of a twenty-minute suite is a lock held for
// twenty minutes (backend-standards.md 8, BE-4.9.4).
func (s *Service) Finish(ctx context.Context, runID uuid.UUID, outcome Outcome) (Run, error) {
	counts := countResults(outcome.Results)

	var finished dbgen.Run
	err := s.db.InTx(ctx, func(queries *dbgen.Queries) error {
		if len(outcome.Results) > 0 {
			// Idempotency: a redelivered execute job must not double the results. The
			// run's own results are cleared first, so the write is "these are the
			// results" rather than "add these results" (BE-4.9.5).
			if err := queries.DeleteRunResults(ctx, runID); err != nil {
				return fmt.Errorf("clear previous results: %w", err)
			}

			rows := make([]dbgen.CreateRunResultsBulkParams, 0, len(outcome.Results))
			for _, result := range outcome.Results {
				rows = append(rows, dbgen.CreateRunResultsBulkParams{
					RunID:          runID,
					TestCaseID:     result.TestCaseID,
					TestFileID:     result.TestFileID,
					Name:           result.Name,
					Status:         dbgen.RunResultStatus(result.Status),
					DurationMs:     int32(result.Duration.Milliseconds()), //nolint:gosec // Bounded by the wall clock.
					Attempt:        int32(result.Attempt),                 //nolint:gosec // Bounded by the retry setting.
					FailureMessage: result.FailureMessage,
					LogKey:         result.LogKey,
					ScreenshotKey:  result.ScreenshotKey,
					VideoKey:       result.VideoKey,
					TraceKey:       result.TraceKey,
					Quarantined:    result.Quarantined,
				})
			}

			if _, err := queries.CreateRunResultsBulk(ctx, rows); err != nil {
				return fmt.Errorf("write run results: %w", err)
			}
		}

		row, err := queries.FinishRun(ctx, dbgen.FinishRunParams{
			ID:          runID,
			Status:      dbgen.RunStatus(outcome.Status),
			Total:       int32(counts.Total),                    //nolint:gosec // A suite this large does not exist.
			Passed:      int32(counts.Passed),                   //nolint:gosec
			Failed:      int32(counts.Failed),                   //nolint:gosec
			Flaky:       int32(counts.Flaky),                    //nolint:gosec
			Skipped:     int32(counts.Skipped),                  //nolint:gosec
			Quarantined: int32(counts.Quarantined),              //nolint:gosec
			DurationMs:  int32(outcome.Duration.Milliseconds()), //nolint:gosec // Bounded by the wall clock.
			Error:       outcome.Error,
			LogKey:      outcome.LogKey,
		})
		if err != nil {
			return fmt.Errorf("finish the run: %w", err)
		}
		finished = row

		if outcome.Metrics != nil {
			// In the same transaction as the counts, so a finished performance run never
			// has its summary read before it is written — the same reason FinishRun writes
			// status and counts together.
			m := outcome.Metrics
			if err := queries.StoreRunMetrics(ctx, dbgen.StoreRunMetricsParams{
				RunID:        runID,
				Requests:     m.Requests,
				Throughput:   m.Throughput,
				ErrorRate:    m.ErrorRate,
				LatencyAvgMs: m.LatencyAvgMs,
				LatencyP50Ms: m.LatencyP50Ms,
				LatencyP90Ms: m.LatencyP90Ms,
				LatencyP95Ms: m.LatencyP95Ms,
				LatencyP99Ms: m.LatencyP99Ms,
				LatencyMaxMs: m.LatencyMaxMs,
				VirtualUsers: int32(m.VirtualUsers), //nolint:gosec // Bounded by the load profile.
				DurationMs:   m.DurationMs,
			}); err != nil {
				return fmt.Errorf("store run metrics: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Run{}, apierr.Internal(err)
	}

	return toRun(finished), nil
}

// MetricsFor reads a performance run's summary, and whether it has one.
func (s *Service) MetricsFor(ctx context.Context, runID uuid.UUID) (Metrics, bool, error) {
	row, err := s.db.Queries().GetRunMetrics(ctx, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Metrics{}, false, nil
		}
		return Metrics{}, false, apierr.Internal(fmt.Errorf("read run metrics: %w", err))
	}

	return Metrics{
		Requests:     row.Requests,
		Throughput:   row.Throughput,
		ErrorRate:    row.ErrorRate,
		LatencyAvgMs: row.LatencyAvgMs,
		LatencyP50Ms: row.LatencyP50Ms,
		LatencyP90Ms: row.LatencyP90Ms,
		LatencyP95Ms: row.LatencyP95Ms,
		LatencyP99Ms: row.LatencyP99Ms,
		LatencyMaxMs: row.LatencyMaxMs,
		VirtualUsers: int(row.VirtualUsers),
		DurationMs:   row.DurationMs,
	}, true, nil
}

// Counts is a run's result tally.
type Counts struct{ Total, Passed, Failed, Flaky, Skipped, Quarantined int }

// countResults tallies the final attempt per test, not every row.
//
// This is where flake detection becomes arithmetic (BE-4.13.2): a test with a
// failed attempt and a passed attempt is one flaky test, not one failure plus one
// pass. Every attempt is still stored; only the count collapses.
func countResults(results []Result) Counts {
	type tally struct {
		passed, failed, skipped, quarantined bool
	}

	byName := map[string]*tally{}
	order := make([]string, 0, len(results))

	for _, result := range results {
		entry, seen := byName[result.Name]
		if !seen {
			entry = &tally{}
			byName[result.Name] = entry
			order = append(order, result.Name)
		}
		if result.Quarantined {
			// A quarantined test is counted, so the total still says how many tests ran,
			// and its failure is not: that is the entire mechanism, and it is arithmetic
			// here rather than a status the report has to interpret (BE-7.6.2).
			entry.quarantined = true
		}
		switch result.Status {
		case ResultPassed:
			entry.passed = true
		case ResultSkipped:
			entry.skipped = true
		case ResultFlaky:
			entry.passed, entry.failed = true, true
		default:
			entry.failed = true
		}
	}

	counts := Counts{Total: len(order)}
	for _, name := range order {
		entry := byName[name]
		switch {
		case entry.quarantined:
			// Excused. Reported as quarantined rather than folded into passed, because a
			// suite where six tests are excused is a different thing from one where they
			// all pass, and a dashboard that cannot tell them apart is a dashboard that
			// hides the problem quarantine was meant to make visible.
			counts.Quarantined++
		case entry.passed && entry.failed:
			counts.Flaky++
		case entry.failed:
			counts.Failed++
		case entry.skipped && !entry.passed:
			counts.Skipped++
		default:
			counts.Passed++
		}
	}
	return counts
}

// StatusFor turns counts into the run's status.
//
// A flaky test does not fail a run: it is reported as flaky, which is the whole
// point of retrying (F-7.11). A run with nothing to report is an error, because a
// green run with zero tests is the most misleading outcome available.
func StatusFor(counts Counts) Status {
	switch {
	case counts.Total == 0:
		return StatusError
	case counts.Failed > 0:
		return StatusFailed
	default:
		return StatusPassed
	}
}

// RecordCommand appends to the run's command log (BE-4.6, F-17.2).
//
// The command arrives already redacted by the runner, because a token on a command
// line would otherwise be stored in a table somebody exports.
func (s *Service) RecordCommand(ctx context.Context, runID uuid.UUID, command Command) error {
	var exit *int32
	if command.ExitCode != nil {
		code := int32(*command.ExitCode) //nolint:gosec // An exit code is one byte.
		exit = &code
	}

	excerpt := command.OutputExcerpt
	if len(excerpt) > maxExcerptBytes {
		excerpt = excerpt[:maxExcerptBytes] + "\n… truncated"
	}

	if err := s.db.Queries().RecordRunCommand(ctx, dbgen.RecordRunCommandParams{
		RunID:         runID,
		Command:       command.Command,
		ExitCode:      exit,
		DurationMs:    int32(command.Duration.Milliseconds()), //nolint:gosec // Bounded by the wall clock.
		OutputExcerpt: excerpt,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("record the run command: %w", err))
	}
	return nil
}

// maxExcerptBytes caps what the command log keeps. The full output is in object
// storage; this column exists so a run can be read without fetching it.
const maxExcerptBytes = 8 << 10

// Get reads one run.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Run, error) {
	row, err := s.db.Queries().GetRun(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Run{}, apierr.RunNotFound()
		}
		return Run{}, apierr.Internal(fmt.Errorf("read the run: %w", err))
	}
	return toRun(row), nil
}

// Cancel stops a queued or running run (BE-4.12).
//
// The row is marked first and the container is killed by the worker watching for
// it: a cancel that depended on reaching the worker would fail exactly when the
// worker is the thing that is stuck.
func (s *Service) Cancel(ctx context.Context, id uuid.UUID) (Run, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return Run{}, err
	}
	if existing.Status.Terminal() {
		return Run{}, apierr.RunNotCancelable(string(existing.Status))
	}

	affected, err := s.db.Queries().CancelRun(ctx, id)
	if err != nil {
		return Run{}, apierr.Internal(fmt.Errorf("cancel the run: %w", err))
	}
	if affected == 0 {
		// It finished between the read and the write. Report what it actually is.
		return s.Get(ctx, id)
	}

	return s.Get(ctx, id)
}

// MarkFailed is the sweeper's write for a run whose worker died (BE-4.2).
func (s *Service) MarkFailed(ctx context.Context, id uuid.UUID, reason string) error {
	if _, err := s.db.Queries().MarkRunFailed(ctx, dbgen.MarkRunFailedParams{
		ID: id, Error: reason,
	}); err != nil {
		return apierr.Internal(fmt.Errorf("mark the run failed: %w", err))
	}
	return nil
}

// ProjectFor is the run's project, for a caller that has a run ID and needs to scope
// a settings read or an authorisation check by it.
func (s *Service) ProjectFor(ctx context.Context, runID uuid.UUID) (uuid.UUID, error) {
	run, err := s.Get(ctx, runID)
	if err != nil {
		return uuid.Nil, err
	}
	return run.ProjectID, nil
}

// LogFor reads a run's stored log back out of object storage.
//
// It lives here rather than in the analysis package because the key is on the run
// row, and a caller that had to know the key layout would be a caller that breaks
// when the layout changes.
func (s *Service) LogFor(ctx context.Context, runID uuid.UUID) (string, error) {
	run, err := s.Get(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.LogKey == "" || s.objects == nil {
		// Not an error: a run that produced no output has no log, and an analysis
		// simply has less to cite.
		return "", nil
	}

	reader, err := s.objects.Get(ctx, run.LogKey)
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the run log: %w", err))
	}
	defer func() {
		if err := reader.Close(); err != nil {
			slog.WarnContext(ctx, "close the run log", "run_id", runID, "error", err)
		}
	}()

	content, err := io.ReadAll(io.LimitReader(reader, maxLogRead))
	if err != nil {
		return "", apierr.Internal(fmt.Errorf("read the run log: %w", err))
	}
	return string(content), nil
}

// maxLogRead caps what an analysis reads back. The driver already truncates a run's
// output; this is the second bound, so a log that somehow grew cannot be pulled whole
// into a worker's memory.
const maxLogRead = 4 << 20

// Stale lists runs a crashed worker left in flight.
func (s *Service) Stale(ctx context.Context, olderThan time.Duration, limit int) ([]uuid.UUID, error) {
	cutoff := time.Now().Add(-olderThan)
	rows, err := s.db.Queries().ListStaleRuns(ctx, dbgen.ListStaleRunsParams{
		StartedAt: &cutoff,
		PageSize:  int32(limit), //nolint:gosec // Caller-supplied and small.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("list stale runs: %w", err))
	}

	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func toRun(row dbgen.Run) Run {
	return Run{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		JobID:       row.JobID,
		TargetURL:   row.TargetUrl,
		Trigger:     Trigger(row.Trigger),
		Status:      Status(row.Status),
		Framework:   testfiles.Framework(row.Framework),
		Image:       row.Image,
		Total:       int(row.Total),
		Passed:      int(row.Passed),
		Failed:      int(row.Failed),
		Flaky:       int(row.Flaky),
		Skipped:     int(row.Skipped),
		Quarantined: int(row.Quarantined),
		Kind:        Kind(row.Kind),
		LoadProfile: row.LoadProfile,
		Duration:    time.Duration(row.DurationMs) * time.Millisecond,
		Error:       row.Error,
		LogKey:      row.LogKey,
		TriggeredBy: row.TriggeredBy,
		StartedAt:   row.StartedAt,
		FinishedAt:  row.FinishedAt,
		CreatedAt:   row.CreatedAt,
	}
}

func toResult(row dbgen.RunResult) Result {
	return Result{
		ID:             row.ID,
		RunID:          row.RunID,
		TestCaseID:     row.TestCaseID,
		TestFileID:     row.TestFileID,
		Name:           row.Name,
		Status:         ResultStatus(row.Status),
		Duration:       time.Duration(row.DurationMs) * time.Millisecond,
		Attempt:        int(row.Attempt),
		FailureMessage: row.FailureMessage,
		LogKey:         row.LogKey,
		ScreenshotKey:  row.ScreenshotKey,
		VideoKey:       row.VideoKey,
		TraceKey:       row.TraceKey,
		Quarantined:    row.Quarantined,
		CreatedAt:      row.CreatedAt,
	}
}

// Result is one result row, for a caller holding an ID rather than a run.
//
// Its project is on the run rather than on the row, so the caller checks membership
// against ProjectFor: authorisation belongs in one layer, and that layer is not this
// one (backend-standards.md 11).
func (s *Service) Result(ctx context.Context, resultID uuid.UUID) (Result, error) {
	row, err := s.db.Queries().GetRunResult(ctx, resultID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, apierr.RunResultNotFound()
		}
		return Result{}, apierr.Internal(fmt.Errorf("read the run result: %w", err))
	}

	return Result{
		ID:             row.ID,
		RunID:          row.RunID,
		TestCaseID:     row.TestCaseID,
		TestFileID:     row.TestFileID,
		Name:           row.Name,
		Status:         ResultStatus(row.Status),
		Duration:       time.Duration(row.DurationMs) * time.Millisecond,
		Attempt:        int(row.Attempt),
		FailureMessage: row.FailureMessage,
		LogKey:         row.LogKey,
		ScreenshotKey:  row.ScreenshotKey,
		VideoKey:       row.VideoKey,
		TraceKey:       row.TraceKey,
		Quarantined:    row.Quarantined,
		CreatedAt:      row.CreatedAt,
	}, nil
}

// ---------------------------------------------------------------- artifacts

// Artifact is one recording a result left behind (BE-7.5.2).
type Artifact struct {
	ResultID uuid.UUID
	TestName string
	Attempt  int

	// Kind is log, screenshot, video, or trace.
	Kind string

	Key         string
	ContentType string
}

// ArtifactsFor lists every recording a run produced.
//
// Read off the result rows rather than by listing the object store, which is the
// difference between "what this run captured" and "what happens to be in a bucket": a
// key with no row is an orphan for retention to remove, not evidence for a UI to
// offer.
func (s *Service) ArtifactsFor(ctx context.Context, runID uuid.UUID) ([]Artifact, error) {
	page, err := s.Results(ctx, runID, ResultFilter{Limit: maxArtifactResults})
	if err != nil {
		return nil, err
	}

	artifacts := make([]Artifact, 0, len(page.Items))
	for _, result := range page.Items {
		for _, candidate := range []struct {
			kind        string
			key         string
			contentType string
		}{
			{ArtifactScreenshot, result.ScreenshotKey, "image/png"},
			{ArtifactVideo, result.VideoKey, "video/webm"},
			{ArtifactTrace, result.TraceKey, "application/zip"},
			{"log", result.LogKey, "text/plain; charset=utf-8"},
		} {
			if candidate.key == "" {
				continue
			}
			artifacts = append(artifacts, Artifact{
				ResultID:    result.ID,
				TestName:    result.Name,
				Attempt:     result.Attempt,
				Kind:        candidate.kind,
				Key:         candidate.key,
				ContentType: candidate.contentType,
			})
		}
	}
	return artifacts, nil
}

// maxArtifactResults bounds the read. A suite with more failing tests than this has a
// problem the artifact list is not going to help with.
const maxArtifactResults = 500

// SignedArtifactURL returns a time-limited URL, or empty when the driver cannot sign
// one. Empty is normal rather than an error: the local-disk driver cannot sign, and
// the platform streams the file instead.
func (s *Service) SignedArtifactURL(ctx context.Context, key string) string {
	if s.objects == nil || key == "" {
		return ""
	}

	signed, err := s.objects.SignedURL(ctx, key, artifactURLTTL)
	switch {
	case err == nil:
		return signed
	case errors.Is(err, objectstore.ErrSignedURLUnsupported):
		return ""
	default:
		slog.WarnContext(ctx, "sign an artifact URL", "key", key, "error", err)
		return ""
	}
}

// artifactURLTTL bounds a link to a recording. Long enough to start a slow download of
// a trace, short enough that a URL left in a browser's history is useless.
const artifactURLTTL = 10 * time.Minute

// OpenArtifact streams one recording of one result.
//
// The result is loaded first so the caller can check the project before a byte is
// read: authorisation on an artifact is authorisation on the run it belongs to, and
// this service does not get to decide that (backend-standards.md 11).
func (s *Service) OpenArtifact(
	ctx context.Context,
	resultID uuid.UUID,
	kind string,
) (Artifact, io.ReadCloser, error) {
	result, err := s.Result(ctx, resultID)
	if err != nil {
		return Artifact{}, nil, err
	}

	artifact := Artifact{
		ResultID: result.ID,
		TestName: result.Name,
		Attempt:  result.Attempt,
		Kind:     kind,
	}

	switch kind {
	case ArtifactScreenshot:
		artifact.Key, artifact.ContentType = result.ScreenshotKey, "image/png"
	case ArtifactVideo:
		artifact.Key, artifact.ContentType = result.VideoKey, "video/webm"
	case ArtifactTrace:
		artifact.Key, artifact.ContentType = result.TraceKey, "application/zip"
	case "log":
		artifact.Key, artifact.ContentType = result.LogKey, "text/plain; charset=utf-8"
	default:
		return Artifact{}, nil, apierr.Validation(
			fmt.Sprintf("%q is not a kind of artifact this platform records.", kind),
			map[string]any{"field": "kind"})
	}

	if artifact.Key == "" {
		return Artifact{}, nil, apierr.RunResultNotFound()
	}
	if s.objects == nil {
		return Artifact{}, nil, apierr.StorageUnreachable(
			errors.New("this process has no object store configured"))
	}

	body, err := s.objects.Get(ctx, artifact.Key)
	if err != nil {
		return Artifact{}, nil, apierr.StorageUnreachable(err)
	}
	return artifact, body, nil
}

// SweepArtifacts deletes run evidence past the retention cutoff (BE-7.5.3).
//
// A video, a trace, and a screenshot are the largest things this platform stores, and
// they are stored per failing attempt: an installation that never expires them fills a
// disk with evidence about tests nobody is still investigating. The retention setting
// already exists and already governs uploaded artifacts; this is the same rule applied
// to the ones the platform produced itself.
//
// The object goes first and the key second, which is the order the artifact sweep uses
// for the same reason: a stored file with no key wastes space, while a key with no file
// is a download that fails, and only one of those is visible to a user.
func (s *Service) SweepArtifacts(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if s.objects == nil {
		return 0, nil
	}
	if limit <= 0 {
		limit = 200
	}

	removed := 0

	results, err := s.db.Queries().ListRunArtifactsOlderThan(ctx,
		dbgen.ListRunArtifactsOlderThanParams{
			FinishedAt: &cutoff,
			PageSize:   int32(limit), //nolint:gosec // Caller-supplied and small.
		})
	if err != nil {
		return 0, apierr.Internal(fmt.Errorf("list run artifacts for retention: %w", err))
	}

	for _, row := range results {
		// Checked between units of work rather than only at the start, so a cancelled
		// sweep stops promptly and tomorrow's picks up where this one left off.
		if err := ctx.Err(); err != nil {
			return removed, err
		}

		failed := false
		for _, key := range []string{row.ScreenshotKey, row.VideoKey, row.TraceKey} {
			if key == "" {
				continue
			}
			if err := s.objects.Delete(ctx, key); err != nil {
				// One unreachable object must not stop the sweep, and the keys stay so the
				// row is retried rather than losing track of a file nobody can now find.
				slog.WarnContext(ctx, "retention could not delete a run artifact",
					"result_id", row.ID, "key", key, "error", err)
				failed = true
				continue
			}
			removed++
		}

		if failed {
			continue
		}
		if err := s.db.Queries().ClearRunResultArtifacts(ctx, row.ID); err != nil {
			return removed, apierr.Internal(fmt.Errorf("clear run artifact keys: %w", err))
		}
	}

	logs, err := s.db.Queries().ListRunLogsOlderThan(ctx, dbgen.ListRunLogsOlderThanParams{
		FinishedAt: &cutoff,
		PageSize:   int32(limit), //nolint:gosec // Caller-supplied and small.
	})
	if err != nil {
		return removed, apierr.Internal(fmt.Errorf("list run logs for retention: %w", err))
	}

	for _, row := range logs {
		if err := ctx.Err(); err != nil {
			return removed, err
		}

		if err := s.objects.Delete(ctx, row.LogKey); err != nil {
			slog.WarnContext(ctx, "retention could not delete a run log",
				"run_id", row.ID, "error", err)
			continue
		}
		if err := s.db.Queries().ClearRunLog(ctx, row.ID); err != nil {
			return removed, apierr.Internal(fmt.Errorf("clear a run log key: %w", err))
		}
		removed++
	}

	return removed, nil
}
