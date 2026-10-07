// Command worker runs Qavia background jobs.
//
// Same wiring rule as cmd/api: the object graph is assembled here, explicitly.
// The worker shares domain code with the API but is deployed and scaled
// separately (tech-stack.md 3).
//
// The difference between the two processes is what they do with the queue. The API
// enqueues and reads; this one runs handlers. Nothing else about the wiring
// changes, which is deliberate: a job handler calls the same services a request
// does, so a rule enforced in a service holds whether a person or a schedule
// triggered the work.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/analyses"
	"github.com/hyscaler/qavia/api/internal/artifacts"
	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/browsers"
	"github.com/hyscaler/qavia/api/internal/capability/browserdriver"
	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/capability/sourceprovider"
	"github.com/hyscaler/qavia/api/internal/comprehension"
	"github.com/hyscaler/qavia/api/internal/coverage"
	"github.com/hyscaler/qavia/api/internal/generation"
	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/mocks"
	"github.com/hyscaler/qavia/api/internal/notifications"
	"github.com/hyscaler/qavia/api/internal/perf"
	"github.com/hyscaler/qavia/api/internal/platform/config"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/platform/observability"
	"github.com/hyscaler/qavia/api/internal/projects"
	"github.com/hyscaler/qavia/api/internal/reports"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/requirements"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/runs"
	"github.com/hyscaler/qavia/api/internal/security"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testcases"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	"github.com/hyscaler/qavia/api/internal/uitests"
	"github.com/hyscaler/qavia/api/internal/users"
)

// version is set by the build via -ldflags.
var version = "dev"

// shutdownGrace bounds draining. A handler that has not finished by then has its
// context cancelled and its job retried, which is safe precisely because every
// handler is idempotent (backend-standards.md 8).
const shutdownGrace = 30 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// Before the logger exists this is the only channel available, which is why
		// cmd/ is exempt from the no-fmt.Print lint rule.
		fmt.Fprintf(os.Stderr, "worker: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logLevel := new(slog.LevelVar)
	logLevel.Set(slog.LevelInfo)

	logger := logging.New(logging.Options{
		Writer:    os.Stdout,
		Level:     logLevel,
		AddSource: !cfg.Env.IsProduction(),
	})
	slog.SetDefault(logger)

	logger.InfoContext(ctx, "starting worker", "version", version, "config", cfg)

	db, err := store.Open(ctx, store.Options{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer db.Close()

	// Migrations are applied by whichever process starts first, and applying them
	// twice is a no-op. The worker runs them too so a deployment that starts the
	// worker first is not stuck waiting for the API.
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}

	recorder := audit.NewRecorder(db)

	cipher, err := settings.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return err
	}
	settingsService := settings.NewService(
		db, settings.Default(), cipher, recorder, settings.DefaultCacheTTL)

	// A setting changed in the API process has to reach this one, or a job would
	// run with a value an operator already replaced (backend-standards.md 6).
	listenerCtx, stopListener := context.WithCancel(ctx)
	defer stopListener()
	go func() {
		if err := settingsService.Listen(listenerCtx); err != nil {
			logger.ErrorContext(ctx, "settings invalidation listener stopped", "error", err)
		}
	}()

	global := settings.Target{}

	if level, err := settingsService.String(ctx, "observability.log_level", global); err == nil {
		logLevel.Set(logging.ParseLevel(level))
	} else {
		logger.WarnContext(ctx, "read log level setting", "error", err)
	}

	traceExporter, err := settingsService.String(ctx, "observability.trace_exporter", global)
	if err != nil {
		return err
	}
	otlpEndpoint, err := settingsService.String(ctx, "observability.otlp_endpoint", global)
	if err != nil {
		return err
	}

	shutdownTracing, err := observability.Setup(ctx, observability.Options{
		ServiceName:    "qavia-worker",
		ServiceVersion: version,
		Environment:    string(cfg.Env),
		Exporter:       observability.Exporter(traceExporter),
		OTLPEndpoint:   otlpEndpoint,
		SampleRatio:    1,
	})
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.WarnContext(ctx, "flush traces", "error", err)
		}
	}()

	objects, err := objectstore.FromSettings(ctx, settingsService)
	if err != nil {
		return err
	}

	sessionStore := auth.NewSessionStore(db)
	usersService := users.NewService(db, sessionStore, recorder)

	notifiers := notifier.NewRegistry()
	notifiers.Register(notifications.NewInApp(db))

	// The optional external channels register unconditionally: Available() reports
	// whether each is configured, and the notification service only sends through an
	// active one. An unconfigured SMTP or Slack is the normal state of a fresh install,
	// and the in-app channel — registered first, so the registry's fallback — always
	// runs (BE-10.4).
	notifiers.Register(notifications.NewSMTP(settingsService, cfg.AppURL.String()))
	notifiers.Register(notifications.NewSlack(settingsService, cfg.AppURL.String()))
	notificationsService := notifications.NewService(db, notifiers, settingsService, usersService)

	projectsService := projects.NewService(db, recorder)
	artifactsService := artifacts.NewService(db, objects, projectsService, settingsService, recorder)

	redisOptions, err := jobs.RedisOptions(cfg.RedisURL)
	if err != nil {
		return err
	}

	// The worker needs the AI layer for real: this is the process that runs agents,
	// and the gateway is the only path from Go into a model.
	aiServiceURL, err := settingsService.String(ctx, "ai.service_url", global)
	if err != nil {
		return err
	}
	aiTimeout, err := settingsService.Duration(ctx, "ai.request_timeout", global)
	if err != nil {
		return err
	}

	aiClient, err := llm.NewClient(aiServiceURL, aiTimeout)
	if err != nil {
		return err
	}
	aiGateway := llm.NewGateway(db, aiClient, cipher, settingsService, projectsService)

	pipeline := generation.Deps{
		Artifacts:    artifactRefs{artifacts: artifactsService},
		Ingest:       ingest.NewService(db, objects),
		Requirements: requirements.NewService(db),
		TestCases:    testcases.NewService(db),
		Gateway:      aiGateway,
		Settings:     settingsService,
	}

	jobRegistry := jobs.NewRegistry()

	// Background loops this process owns, started once everything is wired.
	var sweepers []func(context.Context)

	// The worker publishes job changes so the API processes' streams update. It
	// does not listen: nothing here is watching a job.
	hub := jobs.NewHub(db)

	jobClient := jobs.NewClient(db, redisOptions, jobRegistry, settingsService, hub)
	defer func() {
		if err := jobClient.Close(); err != nil {
			logger.WarnContext(ctx, "close queue client", "error", err)
		}
	}()

	concurrency, err := settingsService.Int(ctx, "jobs.max_concurrency", global)
	if err != nil {
		return err
	}

	// The worker runs the stage that decides whether generation follows extraction,
	// so it needs the same submitter the API has. Deps is a value, so the handlers
	// are built after this is set rather than before.
	pipeline.Chains = jobs.NewService(jobClient, projectsService, recorder, llm.NewGuard(aiGateway))

	// Execution. This is the only process that talks to a container runtime, and it
	// is optional: a worker on a host with no runtime still runs every other stage,
	// and an execute job is refused with a reason rather than failing obscurely
	// (BE-4.2, F-13.6).
	execution, err := registerExecution(ctx, executionWiring{
		db:           db,
		settings:     settingsService,
		projects:     projectsService,
		hub:          hub,
		objects:      objects,
		files:        testfiles.NewService(db),
		registry:     jobRegistry,
		gateway:      aiGateway,
		chains:       pipeline.Chains,
		logger:       logger,
		sweeperGroup: &sweepers,
	})
	if err != nil {
		return err
	}

	// Static validation runs the suite's own toolchain in the runner image, so the
	// generation pipeline gets it from the same driver. Deps is a value, so this has
	// to be set before the codegen handler is built from it (BE-3.4).
	pipeline.Validator = execution.Validator

	for _, handler := range jobs.NoopHandlers() {
		jobs.Register(jobRegistry, handler, jobs.QueueDefault)
	}
	// The run service sweeps its own evidence — logs, videos, traces — under the same
	// retention setting, because those are the largest files this platform stores and
	// they are the ones a fresh installation never thinks about (BE-7.5.3).
	retentionSweepers := []artifacts.Sweeper{}
	if execution.Runs != nil {
		retentionSweepers = append(retentionSweepers, execution.Runs)
	}
	jobs.Register(jobRegistry,
		artifacts.NewRetentionHandler(artifactsService, retentionSweepers...), jobs.QueueLow)
	jobs.Register(jobRegistry, llm.NewSmokeHandler(aiGateway), jobs.QueueCritical)
	jobs.Register(jobRegistry, generation.NewIngestHandler(pipeline), jobs.QueueDefault)
	jobs.Register(jobRegistry, generation.NewExtractHandler(pipeline), jobs.QueueDefault)
	jobs.Register(jobRegistry, generation.NewDesignHandler(pipeline), jobs.QueueDefault)
	jobs.Register(jobRegistry, generation.NewCodegenHandler(pipeline, testfiles.NewService(db)), jobs.QueueDefault)

	// UI discovery drives a browser, which is a container held open for the length of a
	// session. Registered only where there is a runtime to hold one: a worker without
	// Docker refuses the request with a reason rather than queueing work nothing can do
	// (F-13.6, BE-7.1.3).
	if execution.Sessions != nil {
		drivers := browserdriver.NewRegistry()

		// The bundled Playwright container registers first, which makes it the fallback
		// for every later driver. An installation with an empty MCP settings table can
		// still drive a browser, which is the whole rule for a built-in (BE-7.1.2).
		drivers.Register(browsers.NewBundled(
			execution.Sessions,
			runnerSettings{runs: execution.Runs},
			browserTargets{targets: targets.NewService(settingsService, nil)},
		))

		flowsService := uitests.NewService(db)

		jobs.Register(jobRegistry, uitests.NewDiscoverHandler(uitests.Deps{
			Flows:    flowsService,
			Gateway:  aiGateway,
			Settings: settingsService,
			Targets:  targets.NewService(settingsService, nil),
			Drivers:  drivers,
			Objects:  objects,
		}), jobs.QueueDefault)

		// Generation validates every spec in the runner image before it is kept, which
		// is why it is registered here rather than beside the other agent stages: the
		// image that owns the toolchain is the only thing that can compile TypeScript
		// (BE-3.4, BE-7.4).
		jobs.Register(jobRegistry, uitests.NewGenerateHandler(uitests.GenerateDeps{
			Graphs:    flowsService,
			Files:     testfiles.NewService(db),
			Gateway:   aiGateway,
			Validator: execution.Validator,
			Settings:  settingsService,
		}), jobs.QueueDefault)
	} else {
		logger.WarnContext(ctx,
			"no container runtime, so UI flow discovery is unavailable on this worker")
	}

	// Performance and security runs need a container to generate traffic in, so both
	// register only where there is a runtime (BE-9.1, BE-9.4, F-13.6).
	if execution.Runs != nil && execution.Driver != nil {
		jobs.Register(jobRegistry, perf.NewRunHandler(perf.Deps{
			Service:   perf.NewService(ingest.NewService(db, objects), aiGateway),
			Runs:      execution.Runs,
			Driver:    execution.Driver,
			Validator: execution.Validator,
			Files:     testfiles.NewService(db),
		}), jobs.QueueDefault)

		jobs.Register(jobRegistry, security.NewScanHandler(security.Deps{
			Endpoints: ingest.NewService(db, objects),
			Gateway:   aiGateway,
			Findings:  security.NewService(db),
			Analyses:  analyses.NewService(db),
			Runs:      execution.Runs,
			Driver:    execution.Driver,
		}), jobs.QueueDefault)
	} else {
		logger.WarnContext(ctx, "no container runtime, so performance and security testing are unavailable on this worker")
	}

	// The mock server: a container that outlives the request that asked for it, so it
	// lives here with the rest of the container work. Registered only where there is a
	// runtime, and the API refuses the request with a reason rather than queueing work
	// nothing can do (BE-8.6.3, F-13.6).
	if execution.Driver != nil {
		mockService := mocks.NewService(db, ingest.NewService(db, objects),
			mockContainers{driver: execution.Driver}, settingsService,
			mockAllowlist{settings: settingsService, users: usersService})

		jobs.Register(jobRegistry, mocks.NewStartHandler(mockService), jobs.QueueDefault)
		jobs.Register(jobRegistry, mocks.NewStopHandler(mockService), jobs.QueueDefault)

		// A mock that died has to stop being reported as running, and the containers are
		// here rather than in the process that serves the status (BE-4.12's rule again).
		sweepers = append(sweepers, func(ctx context.Context) {
			mockService.Reconcile(ctx, time.Minute)
		})
	} else {
		logger.WarnContext(ctx, "no container runtime, so mock servers are unavailable on this worker")
	}

	// Repository work: the clone happens here, because this is the process with git, a
	// disk, and the token, and none of it reaches a runner container (BE-6.1.3).
	//
	// Registered after execution so the stages that need a container — unit test
	// validation and coverage — can be handed the driver the runner wiring produced.
	if err := registerRepoStages(ctx, repoWiring{
		db:       db,
		settings: settingsService,
		objects:  objects,
		gateway:  aiGateway,
		registry: jobRegistry,
		logger:   logger,
		runtime:  execution,
	}); err != nil {
		return err
	}

	worker := jobs.NewWorker(
		db, jobClient, jobRegistry, hub, settingsService,
		notificationsService, usersService,
		redisOptions,
		jobs.WorkerOptions{Concurrency: concurrency, ShutdownTimeout: shutdownGrace},
	)

	// Recurring work is enqueued by the scheduler rather than by a ticker in each
	// replica: a ticker per process fires the same sweep once per replica, and the
	// scheduler elects one owner (BE-0.20).
	retentionCron, err := settingsService.String(ctx, "triggers.retention_cron", global)
	if err != nil {
		return err
	}

	scheduler := runtrigger.NewScheduler(redisOptions)
	if err := scheduler.Register(runtrigger.Entry{
		Cron:    retentionCron,
		Type:    artifacts.RetentionType,
		Payload: artifacts.RetentionPayload{},
		Queue:   string(jobs.QueueLow),
	}); err != nil {
		return err
	}

	logger.InfoContext(ctx, "worker ready",
		"concurrency", concurrency,
		"execution", execution.Driver != nil,
		"queues", jobs.QueueNames(),
		"job_types", jobRegistry.Types(),
	)

	// Both loops are waited on rather than left to run free: a goroutine that
	// outlives its process writes after shutdown (backend-standards.md 8).
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return worker.Run(groupCtx) })
	group.Go(func() error { return scheduler.Run(groupCtx) })

	// The orphan sweeper is a loop rather than a scheduled job on purpose: it cleans
	// up after a crashed worker, and a queue-backed job would need a healthy worker
	// to run (BE-4.2.4).
	for _, sweep := range sweepers {
		sweeper := sweep
		group.Go(func() error {
			sweeper(groupCtx)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}
	logger.Info("stopped")
	return nil
}

// artifactRefs adapts the artifacts service to what the generation pipeline
// declared it needs, the same way the API process does.
type artifactRefs struct {
	artifacts *artifacts.Service
}

func (a artifactRefs) StorageKeyFor(ctx context.Context, id uuid.UUID) (generation.ArtifactRef, error) {
	artifact, err := a.artifacts.StorageKeyFor(ctx, id)
	if err != nil {
		return generation.ArtifactRef{}, err
	}
	return generation.ArtifactRef{
		ID:         artifact.ID,
		ProjectID:  artifact.ProjectID,
		Kind:       string(artifact.Kind),
		Filename:   artifact.Filename,
		StorageKey: artifact.StorageKey,
	}, nil
}

// executionWiring is what the execute stage needs, gathered so registerExecution
// reads as one decision rather than eight arguments.
type executionWiring struct {
	db       *store.DB
	settings *settings.Service
	projects *projects.Service
	hub      *jobs.Hub
	objects  objectstore.Store
	files    *testfiles.Service
	registry *jobs.Registry
	gateway  *llm.Gateway
	chains   runs.Chains
	logger   *slog.Logger

	// sweeperGroup collects loops the caller starts under its error group.
	sweeperGroup *[]func(context.Context)
}

// registerExecution wires the execute stage if this host can actually run
// containers, and says so plainly if it cannot.
//
// Optional by design (F-13.6, BE-4.2): a developer's worker with no Docker socket,
// or a deployment that deliberately turns execution off, keeps every other stage
// working. What it must not do is register the stage and then fail every run with an
// obscure error, so the check happens here, once, at start-up.
// Runtime is what the execution wiring produced, for the later stages that need it.
//
// Returned as a struct rather than three values because three of the four consumers
// want a different subset, and a positional signature that grows every phase is a
// signature nobody reads.
type Runtime struct {
	Driver    runner.Driver
	Validator generation.Validator
	Runs      *runs.Service

	// Sessions is set when the driver can hold a container open, which is what a
	// browser needs. Nil on a driver that only runs one-shot containers, and the
	// discovery stage is then not registered rather than failing at its first use.
	Sessions runner.SessionDriver
}

func registerExecution(ctx context.Context, wiring executionWiring) (Runtime, error) {
	driver, err := settingsDriver(ctx, wiring)
	if err != nil {
		return Runtime{}, err
	}
	if driver == nil {
		// No runtime: execution is off, and reports still render as HTML. The build
		// handler is registered without a printer, which refuses a PDF with a stated
		// reason rather than handing back HTML under a .pdf name.
		jobs.Register(wiring.registry, reports.NewBuildHandler(reports.Deps{
			Reports:  reports.NewService(wiring.db, wiring.objects),
			Gatherer: reports.NewCollector(wiring.db),
		}), jobs.QueueLow)
		return Runtime{}, nil
	}

	runsService := runs.NewService(
		wiring.db,
		wiring.settings,
		targets.NewService(wiring.settings, nil),
		runs.WithLogNotifier(wiring.hub),
		runs.WithObjectStore(wiring.objects),
	)

	jobs.Register(wiring.registry, runs.NewExecuteHandler(runs.ExecuteDeps{
		Runs:     runsService,
		Files:    wiring.files,
		Driver:   driver,
		Objects:  wiring.objects,
		Settings: wiring.settings,
		Targets:  targets.NewService(wiring.settings, nil),

		// The semaphore, not the queue's concurrency, is what bounds containers: a
		// worker runs every kind of job, and a runner slot is far scarcer than a
		// parse (BE-4.10).
		Slots: runs.NewSemaphore(wiring.settings),

		// A finished run with failures queues its own analysis, unless the project
		// turned that off (F-9.1).
		Chains: wiring.chains,
	}), jobs.QueueDefault)

	// Reports render where the browser is: HTML always, PDF when a container runtime
	// is available (BE-5.9.3).
	jobs.Register(wiring.registry, reports.NewBuildHandler(reports.Deps{
		Reports:  reports.NewService(wiring.db, wiring.objects),
		Gatherer: reports.NewCollector(wiring.db),
		Printer:  reports.NewBrowserPrinter(driver, runnerSettings{runs: runsService}),
	}), jobs.QueueLow)

	// Analysis runs where the runner is, because it reads the run's log back out of
	// object storage and the worker is the process holding that (BE-5.2).
	jobs.Register(wiring.registry, analyses.NewAnalyseHandler(analyses.Deps{
		Analyses: analyses.NewService(wiring.db),
		Runs:     runsService,
		Files:    wiring.files,
		Gateway:  wiring.gateway,
		Objects:  wiring.objects,
		Settings: wiring.settings,
	}), jobs.QueueDefault)

	sweeper := runs.NewSweeper(runsService, driver, wiring.settings)
	*wiring.sweeperGroup = append(*wiring.sweeperGroup, sweeper.Run)

	// The same driver serves static validation, which runs the suite's own toolchain in
	// the same image with no network at all (BE-3.4), and the repository stages that
	// need a container.
	// A driver that can hold a container open is what a browser session needs. The
	// assertion rather than a second constructor: which capabilities a driver has is
	// the driver's business, and this is the one place that has to ask.
	sessions, canHoldOpen := driver.(runner.SessionDriver)
	if !canHoldOpen {
		wiring.logger.WarnContext(ctx, "this container driver cannot hold a session open, "+
			"so UI flow discovery is unavailable here")
	}

	return Runtime{
		Driver:    driver,
		Validator: runs.NewValidator(driver, runsService),
		Runs:      runsService,
		Sessions:  sessions,
	}, nil
}

// settingsDriver builds the runner driver the settings ask for, or nil when
// execution is off or unavailable.
func settingsDriver(ctx context.Context, wiring executionWiring) (runner.Driver, error) {
	global := settings.Target{}

	kind, err := wiring.settings.String(ctx, "runner.driver", global)
	if err != nil {
		return nil, err
	}
	if kind == "disabled" {
		wiring.logger.InfoContext(ctx, "execution is disabled in settings, so no runs will be executed here")
		return nil, nil
	}

	helper, err := wiring.settings.String(ctx, "runner.image_net_helper", global)
	if err != nil {
		return nil, err
	}

	driver, err := runner.NewDocker(helper)
	if err != nil {
		// Not fatal: a worker with no container runtime is a worker that does
		// everything except execute, which is better than a worker that will not
		// start.
		wiring.logger.WarnContext(ctx, "no container runtime, so execution is unavailable here",
			"error", err)
		return nil, nil
	}

	if !driver.Available(ctx) {
		wiring.logger.WarnContext(ctx,
			"the container runtime did not answer, so execution is unavailable here")
		return nil, nil
	}

	runtime, err := wiring.settings.String(ctx, "runner.runtime", global)
	if err != nil {
		return nil, err
	}
	if runner.RuntimeFor(runtime).Unsafe() {
		// Said once, at start-up, at warn level. runc is a development convenience
		// and this platform executes model-generated code (work.md 8, G2).
		wiring.logger.WarnContext(ctx,
			"the runner is configured with runc, which is not a boundary for untrusted code; "+
				"use gvisor for anything but local development")
	}

	return driver, nil
}

// runnerSettings adapts the runs service to what the report printer declared it
// needs: which image, which limits, which runtime. Declared here rather than in
// either package, so neither learns about the other.
type runnerSettings struct {
	runs *runs.Service
}

func (r runnerSettings) PlaywrightImage(ctx context.Context, projectID uuid.UUID) (string, error) {
	return r.runs.ImageFor(ctx, projectID, testfiles.FrameworkPlaywright)
}

// ImageFor is what the browser driver asks for: which image runs this framework for
// this project. Named the way that package declared it rather than the way the report
// printer did, because a consumer-declared interface is the consumer's vocabulary.
func (r runnerSettings) ImageFor(
	ctx context.Context,
	projectID uuid.UUID,
	framework testfiles.Framework,
) (string, error) {
	return r.runs.ImageFor(ctx, projectID, framework)
}

func (r runnerSettings) Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error) {
	return r.runs.Limits(ctx, projectID)
}

func (r runnerSettings) Runtime(ctx context.Context) (runner.Runtime, error) {
	return r.runs.Runtime(ctx)
}

// mockContainers narrows the container driver to the four methods a mock's lifecycle
// needs, and asserts the one capability that is not on the shared Driver interface:
// starting something that listens.
type mockContainers struct {
	driver runner.Driver
}

func (m mockContainers) StartService(
	ctx context.Context,
	spec runner.ServiceSpec,
) (*runner.Service, error) {
	starter, ok := m.driver.(interface {
		StartService(context.Context, runner.ServiceSpec) (*runner.Service, error)
	})
	if !ok {
		return nil, runner.ErrUnavailable
	}
	return starter.StartService(ctx, spec)
}

func (m mockContainers) ServiceStatus(
	ctx context.Context,
	containerID string,
) (runner.ServiceStatus, error) {
	inspector, ok := m.driver.(interface {
		ServiceStatus(context.Context, string) (runner.ServiceStatus, error)
	})
	if !ok {
		return runner.ServiceStatus{}, runner.ErrUnavailable
	}
	return inspector.ServiceStatus(ctx, containerID)
}

func (m mockContainers) StopService(ctx context.Context, containerID string) {
	stopper, ok := m.driver.(interface {
		StopService(context.Context, string)
	})
	if !ok {
		return
	}
	stopper.StopService(ctx, containerID)
}

func (m mockContainers) Available(ctx context.Context) bool { return m.driver.Available(ctx) }

// mockAllowlist adds a started mock's host to the project's allowed targets
// (BE-8.6.5).
//
// The settings service owns validation, permissions, and the audit row for a settings
// change, so this goes through it rather than writing the row itself. What it adds is
// idempotence: a host already on the list is left alone, so restarting a mock does not
// grow the list by one entry every time.
type mockAllowlist struct {
	settings *settings.Service
	users    *users.Service
}

func (m mockAllowlist) Add(
	ctx context.Context,
	projectID uuid.UUID,
	host string,
	actor httpx.Principal,
) error {
	scope := settings.Target{ProjectID: &projectID}

	existing, err := m.settings.StringList(ctx, "targets.allowlist", scope)
	if err != nil {
		return err
	}
	for _, entry := range existing {
		if strings.EqualFold(strings.TrimSpace(entry), host) {
			return nil
		}
	}

	raw, err := json.Marshal(append(append([]string{}, existing...), host))
	if err != nil {
		return fmt.Errorf("encode the allowlist: %w", err)
	}

	// The actor's role is read rather than assumed: the allowlist is a lead's setting,
	// and a contributor starting a mock must not become a way around that. A refusal is
	// logged by the caller and the mock still runs — it is simply not reachable from a
	// generated suite until somebody with the role adds the host.
	account, err := m.users.Get(ctx, actor.UserID)
	if err != nil {
		return err
	}

	_, err = m.settings.Write(ctx, settings.Actor{
		UserID: actor.UserID,
		Email:  account.Email,
		Role:   account.Role,
	}, settings.WriteRequest{
		Key:     "targets.allowlist",
		Scope:   settings.ScopeProject,
		ScopeID: &projectID,
		Raw:     raw,
	})
	return err
}

// browserTargets adapts the target checker to what the bundled browser driver
// declared it needs: the host and the address the platform actually approved. A
// browser is as good an SSRF primitive as a test runner, so it goes through the same
// check (BE-4.7).
type browserTargets struct {
	targets *targets.Service
}

func (b browserTargets) Allowed(
	ctx context.Context,
	projectID uuid.UUID,
	target string,
) ([]runner.HostAddress, error) {
	checked, err := b.targets.CheckURL(ctx, projectID, target)
	if err != nil {
		return nil, err
	}
	return []runner.HostAddress{{
		Host: checked.Host,
		IP:   checked.Primary().String(),
	}}, nil
}

// registerRepoSync wires the clone-and-detect stage.
//
// Both providers are registered: clone-by-URL when git is on the host, and the
// archive expander always. A worker without git still syncs a project whose source
// arrived as a zip, which is the reason the archive provider is the built-in one
// (F-13.6).
// repoWiring is what the repository stages need.
type repoWiring struct {
	db       *store.DB
	settings *settings.Service
	objects  objectstore.Store
	gateway  *llm.Gateway
	registry *jobs.Registry
	logger   *slog.Logger

	// runtime is empty on a worker with no container runtime. Unit test validation and
	// coverage are then not registered, and the API refuses those requests with a
	// reason rather than queueing work nothing can do (F-13.6).
	runtime Runtime
}

func registerRepoStages(ctx context.Context, wiring repoWiring) error {
	db, settingsService := wiring.db, wiring.settings
	objects, gateway := wiring.objects, wiring.gateway
	registry, logger := wiring.registry, wiring.logger

	global := settings.Target{}

	root, err := settingsService.String(ctx, "repo.workspace_root", global)
	if err != nil {
		return err
	}
	depth, err := settingsService.Int(ctx, "repo.clone_depth", global)
	if err != nil {
		return err
	}
	timeout, err := settingsService.Duration(ctx, "repo.clone_timeout", global)
	if err != nil {
		return err
	}

	// The token is fetched per job rather than baked into the provider, because it is
	// per project and because a long-lived process holding a decrypted credential is a
	// process whose memory holds one.
	fetchers := map[repos.Provider]repos.Fetcher{
		repos.ProviderArchive: sourceprovider.NewArchive(objects, sourceprovider.Limits{}),
	}

	// Built without the service so availability can be probed before the service that
	// will use it exists.
	if sourceprovider.NewGit(depth, timeout, "").Available(ctx) {
		fetchers[repos.ProviderGit] = nil
	} else {
		logger.WarnContext(ctx,
			"git is not on this host, so clone-by-URL is unavailable; archive uploads still work")
	}

	reposService := repos.NewService(db, settingsService, repos.WithCloner(repos.Cloner{
		Fetchers: fetchers,
		Target:   repoTargets{targets: targets.NewService(settingsService, nil)},
		Root:     root,
	}))

	if _, hasGit := fetchers[repos.ProviderGit]; hasGit {
		fetchers[repos.ProviderGit] = repos.TokenFetcher{
			Repos: reposService,
			New: func(token string) repos.Fetcher {
				return sourceprovider.NewGit(depth, timeout, token)
			},
		}
	}

	jobs.Register(registry, repos.NewSyncHandler(repos.Deps{Repos: reposService}), jobs.QueueDefault)

	// Comprehension explores the checkout the same clone path produces, one agent step
	// at a time, with the tools running here because this is the process that owns the
	// filesystem and its path validation (BE-6.4).
	jobs.Register(registry, comprehension.NewComprehendHandler(comprehension.Deps{
		Repos:    reposService,
		Gateway:  gateway,
		Settings: settingsService,
	}), jobs.QueueDefault)

	// Unit test generation and coverage both need a container: one to validate what it
	// wrote, the other to run the client's own suite. A worker without a runtime
	// registers neither, and the API refuses the request with a reason rather than
	// queueing work nothing can do (F-13.6).
	if wiring.runtime.Driver != nil {
		jobs.Register(registry, comprehension.NewUnitTestHandler(comprehension.UnitTestDeps{
			Repos:     reposService,
			Maps:      reposService,
			Files:     testfiles.NewService(db),
			Gateway:   gateway,
			Validator: wiring.runtime.Validator,
			Settings:  settingsService,
		}), jobs.QueueDefault)

		jobs.Register(registry, coverage.NewMeasureHandler(coverage.Deps{
			Coverage: coverage.NewService(db),
			Repos:    reposService,
			Runs:     wiring.runtime.Runs,
			Files:    testfiles.NewService(db),
			Driver:   wiring.runtime.Driver,
		}), jobs.QueueDefault)
	} else {
		logger.WarnContext(ctx, "no container runtime, so unit test generation and "+
			"coverage measurement are unavailable on this worker")
	}

	return nil
}

// repoTargets adapts the target checker to the one method the sync stage declared: a
// repository URL goes through the same allowlist and address rules as a test target.
type repoTargets struct {
	targets *targets.Service
}

func (r repoTargets) CheckURL(
	ctx context.Context,
	projectID uuid.UUID,
	raw string,
) (string, error) {
	checked, err := r.targets.CheckURL(ctx, projectID, raw)
	if err != nil {
		return "", err
	}
	return checked.Host, nil
}
