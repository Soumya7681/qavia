// Command api serves the Qavia HTTP API.
//
// This file is the entire object graph. Every dependency is a constructor
// argument assembled here: there is no DI container and no runtime resolution
// order (tech-stack.md 3). Reading this file tells you how the process is wired.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/analyses"
	"github.com/hyscaler/qavia/api/internal/artifacts"
	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/capability/defecttracker"
	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/capability/sourceprovider"
	"github.com/hyscaler/qavia/api/internal/comprehension"
	"github.com/hyscaler/qavia/api/internal/coverage"
	"github.com/hyscaler/qavia/api/internal/datagen"
	"github.com/hyscaler/qavia/api/internal/defects"
	"github.com/hyscaler/qavia/api/internal/generation"
	"github.com/hyscaler/qavia/api/internal/health"
	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/integrations"
	"github.com/hyscaler/qavia/api/internal/integrations/jira"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/mcp"
	"github.com/hyscaler/qavia/api/internal/mocks"
	"github.com/hyscaler/qavia/api/internal/notifications"
	"github.com/hyscaler/qavia/api/internal/perf"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/config"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/platform/observability"
	"github.com/hyscaler/qavia/api/internal/projects"
	"github.com/hyscaler/qavia/api/internal/reports"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/requirements"
	"github.com/hyscaler/qavia/api/internal/riskcontrol"
	"github.com/hyscaler/qavia/api/internal/runs"
	"github.com/hyscaler/qavia/api/internal/security"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/setup"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/targets"
	"github.com/hyscaler/qavia/api/internal/testcases"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	"github.com/hyscaler/qavia/api/internal/uitests"
	"github.com/hyscaler/qavia/api/internal/users"
)

// registerJobHandlers declares every job type both processes know about.
//
// The API needs the same registry as the worker even though it never runs a
// handler: an enqueue of a type nothing can run is refused at submit rather than
// becoming a row that fails on every worker that picks it up.
func registerJobHandlers(
	registry *jobs.Registry,
	artifactsService *artifacts.Service,
	gateway *llm.Gateway,
	pipeline generation.Deps,
	files *testfiles.Service,
) {
	for _, handler := range jobs.NoopHandlers() {
		jobs.Register(registry, handler, jobs.QueueDefault)
	}
	jobs.Register(registry, artifacts.NewRetentionHandler(artifactsService), jobs.QueueLow)

	// The smoke test is critical rather than default: somebody is watching it, and
	// it exists to answer a question quickly.
	jobs.Register(registry, llm.NewSmokeHandler(gateway), jobs.QueueCritical)

	// Ingest is deterministic parsing and finishes in seconds, so it sits on the
	// default queue. The two agent stages sit there too: they are what a user is
	// waiting for, and starving them behind a retention sweep would be backwards.
	jobs.Register(registry, generation.NewIngestHandler(pipeline), jobs.QueueDefault)
	jobs.Register(registry, generation.NewExtractHandler(pipeline), jobs.QueueDefault)
	jobs.Register(registry, generation.NewDesignHandler(pipeline), jobs.QueueDefault)
	jobs.Register(registry, generation.NewCodegenHandler(pipeline, files), jobs.QueueDefault)

	// Execution is enqueue-only here. This process checks the target and writes the
	// run row; a worker with a container runtime is what actually runs the suite, so
	// the API knows the type without pretending it can handle it (BE-4.9).
	jobs.RegisterEnqueueOnly(registry, runs.TypeExecute, jobs.QueueDefault, runs.ExecuteIdempotencyKey)

	// Analysis is enqueue-only here for the same reason as execution: it reads a run's
	// log out of object storage and calls a provider from a worker, and this process
	// only ever asks for it (BE-5.2).
	jobs.RegisterEnqueueOnly(registry, analyses.TypeAnalyse, jobs.QueueDefault,
		analyses.AnalyseIdempotencyKey)

	// Report generation is a standalone job rather than a chain, and it renders where
	// the browser is, so this process only enqueues it (BE-5.9).
	jobs.RegisterEnqueueOnlyFor(registry, reports.TypeReport, jobs.QueueLow,
		func(payload reports.Payload) string {
			return fmt.Sprintf("report:%s", payload.ReportID)
		})

	// A clone needs git, a disk, and a workspace, none of which this process has
	// (BE-6.1.3).
	jobs.RegisterEnqueueOnlyFor(registry, repos.TypeSync, jobs.QueueDefault,
		repos.SyncIdempotencyKey)
	jobs.RegisterEnqueueOnlyFor(registry, comprehension.TypeComprehend, jobs.QueueDefault,
		comprehension.ComprehendIdempotencyKey)
	jobs.RegisterEnqueueOnlyFor(registry, comprehension.TypeUnitTests, jobs.QueueDefault,
		comprehension.UnitTestIdempotencyKey)

	// Coverage runs a client's own test suite in a container, so it belongs where the
	// runtime is (BE-6.6.1).
	jobs.RegisterEnqueueOnlyFor(registry, coverage.TypeMeasure, jobs.QueueDefault,
		coverage.MeasureIdempotencyKey)

	// A discovery holds a browser open for minutes, which needs a container runtime
	// this process does not have (BE-7.2).
	jobs.RegisterEnqueueOnlyFor(registry, uitests.TypeDiscover, jobs.QueueDefault,
		uitests.DiscoverIdempotencyKey)

	// Generation validates every spec in the runner image, so it belongs where the
	// runtime is (BE-3.4, BE-7.4).
	jobs.RegisterEnqueueOnlyFor(registry, uitests.TypeGenerate, jobs.QueueDefault,
		uitests.GenerateIdempotencyKey)

	// A mock server is a container that outlives the request, so its lifecycle runs
	// where the runtime is (BE-8.6.3).
	jobs.RegisterEnqueueOnlyFor(registry, mocks.TypeStart, jobs.QueueDefault,
		mocks.StartIdempotencyKey)
	jobs.RegisterEnqueueOnlyFor(registry, mocks.TypeStop, jobs.QueueDefault,
		mocks.StopIdempotencyKey)

	// A performance run and a security scan each generate traffic in a container, so
	// they run where the runtime is (BE-9.1, BE-9.4).
	jobs.RegisterEnqueueOnlyFor(registry, perf.TypeRun, jobs.QueueDefault,
		perf.IdempotencyKey)
	jobs.RegisterEnqueueOnlyFor(registry, security.TypeScan, jobs.QueueDefault,
		security.IdempotencyKey)
}

// version is set by the build via -ldflags.
var version = "dev"

// shutdownGrace bounds draining. Long enough for an in-flight request to finish,
// short enough that a deploy is not held up by an SSE stream that would otherwise
// stay open for hours.
const shutdownGrace = 20 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		// Before the logger exists this is the only channel available, which is
		// why cmd/ is exempt from the no-fmt.Print lint rule.
		fmt.Fprintf(os.Stderr, "api: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// A LevelVar rather than a fixed level, because the log level is a setting and
	// the settings service is not reachable until the database is. It starts at info
	// and is raised or lowered below, once settings can be read.
	logLevel := new(slog.LevelVar)
	logLevel.Set(slog.LevelInfo)

	logger := logging.New(logging.Options{
		Writer:    os.Stdout,
		Level:     logLevel,
		AddSource: !cfg.Env.IsProduction(),
	})
	slog.SetDefault(logger)

	logger.InfoContext(ctx, "starting api", "version", version, "config", cfg)

	built, err := build(ctx, cfg, logger, logLevel)
	if err != nil {
		return err
	}
	defer built.close()

	return serve(ctx, logger, cfg, built.handler)
}

// app is the assembled API process: the handler plus what has to be released when
// it stops.
//
// It exists so the integration tests build exactly what main builds. A test
// harness that wires its own subset of handlers drifts from production one
// constructor at a time, and the standing tests (BE-0.29) are only worth having if
// they run against the real thing.
type app struct {
	handler  http.Handler
	db       *store.DB
	settings *settings.Service

	closers []func()
}

func (a *app) onClose(fn func()) { a.closers = append(a.closers, fn) }

// close releases everything in reverse order of acquisition, the same order the
// defers it replaced ran in.
func (a *app) close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// build wires every dependency and returns the handler. On error, whatever was
// already acquired is released before returning.
func build(ctx context.Context, cfg config.Config, logger *slog.Logger, logLevel *slog.LevelVar) (_ *app, err error) {
	a := &app{}
	defer func() {
		if err != nil {
			a.close()
		}
	}()

	db, err := store.Open(ctx, store.Options{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return nil, err
	}
	a.onClose(db.Close)
	a.db = db

	// Migrations are embedded, so a deploy carries the schema it needs and there
	// is no separate migration step to forget.
	if err := store.Migrate(ctx, db); err != nil {
		return nil, err
	}

	recorder := audit.NewRecorder(db)

	// The settings service is what makes everything after phase 0 configurable from
	// the UI, so it is built early and passed to whatever needs a configurable value.
	cipher, err := settings.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return nil, err
	}
	settingsService := settings.NewService(
		db, settings.Default(), cipher, recorder, settings.DefaultCacheTTL)
	a.settings = settingsService

	// The API and the worker are separate processes, so cache invalidation crosses
	// process boundaries over Postgres LISTEN/NOTIFY. Without this listener a
	// setting changed in one process would stay stale in the other until its TTL
	// expired.
	listenerCtx, stopListener := context.WithCancel(ctx)
	a.onClose(stopListener)
	go func() {
		if err := settingsService.Listen(listenerCtx); err != nil {
			logger.ErrorContext(ctx, "settings invalidation listener stopped", "error", err)
		}
	}()

	// Everything below reads its configuration from settings rather than from
	// constants or the environment. This is the payoff for BE-0.13: an operator
	// changes any of it from the UI (backend-standards.md 6).
	global := settings.Target{}

	if level, err := settingsService.String(ctx, "observability.log_level", global); err == nil {
		logLevel.Set(logging.ParseLevel(level))
	} else {
		logger.WarnContext(ctx, "read log level setting", "error", err)
	}

	traceExporter, err := settingsService.String(ctx, "observability.trace_exporter", global)
	if err != nil {
		return nil, err
	}
	otlpEndpoint, err := settingsService.String(ctx, "observability.otlp_endpoint", global)
	if err != nil {
		return nil, err
	}

	shutdownTracing, err := observability.Setup(ctx, observability.Options{
		ServiceName:    "qavia-api",
		ServiceVersion: version,
		Environment:    string(cfg.Env),
		Exporter:       observability.Exporter(traceExporter),
		OTLPEndpoint:   otlpEndpoint,
		SampleRatio:    1,
	})
	if err != nil {
		return nil, err
	}
	a.onClose(func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			logger.WarnContext(ctx, "flush traces", "error", err)
		}
	})

	// Sessions are server-side, in the same Postgres as everything else, so
	// revoking access is a DELETE rather than waiting out a token.
	sessionLifetime, err := settingsService.Duration(ctx, "security.session_lifetime", global)
	if err != nil {
		return nil, err
	}
	sessionIdleTimeout, err := settingsService.Duration(ctx, "security.session_idle_timeout", global)
	if err != nil {
		return nil, err
	}

	sessionStore := auth.NewSessionStore(db)
	sessionManager := auth.NewSessionManager(
		sessionStore,
		cfg.Env.IsProduction(), // Secure cookie only where there is TLS
		sessionLifetime,
		sessionIdleTimeout,
	)

	authService := auth.NewService(db, sessionManager, sessionStore, recorder)
	usersService := users.NewService(db, sessionStore, recorder)

	// The capability registries are what make every external platform optional
	// (F-1.11). Built-ins register unconditionally; an external adapter registers
	// only when its settings are present, and this file is the only one that knows
	// a vendor name (backend-standards.md 7).
	objects, err := objectstore.FromSettings(ctx, settingsService)
	if err != nil {
		return nil, err
	}

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

	sourceProviders := sourceprovider.NewRegistry()
	sourceProviders.Register(sourceprovider.NewArchive(objects, sourceprovider.Limits{}))

	projectsService := projects.NewService(db, recorder)
	artifactsService := artifacts.NewService(db, objects, projectsService, settingsService, recorder)

	// The queue. The API process enqueues and reads; it never runs a handler,
	// which is why there is a second binary.
	redisOptions, err := jobs.RedisOptions(cfg.RedisURL)
	if err != nil {
		return nil, err
	}

	// The AI layer. The gateway is the only path from Go into a model: it resolves
	// a tier, enforces data residency, checks the budget before the call, and
	// writes the one llm_calls row afterwards.
	aiServiceURL, err := settingsService.String(ctx, "ai.service_url", global)
	if err != nil {
		return nil, err
	}
	aiTimeout, err := settingsService.Duration(ctx, "ai.request_timeout", global)
	if err != nil {
		return nil, err
	}

	aiClient, err := llm.NewClient(aiServiceURL, aiTimeout)
	if err != nil {
		return nil, err
	}
	aiGateway := llm.NewGateway(db, aiClient, cipher, settingsService, projectsService)
	aiService := llm.NewService(db, cipher, aiGateway, aiClient, recorder)

	// Ingest and generation. Parsing is deterministic; the two agent stages go
	// through the gateway like everything else.
	ingestService := ingest.NewService(db, objects)
	requirementsService := requirements.NewService(db)
	testCasesService := testcases.NewService(db)
	testFilesService := testfiles.NewService(db)

	pipeline := generation.Deps{
		Artifacts:    artifactRefs{artifacts: artifactsService},
		Ingest:       ingestService,
		Requirements: requirementsService,
		TestCases:    testCasesService,
		Gateway:      aiGateway,
		Settings:     settingsService,
	}

	jobRegistry := jobs.NewRegistry()

	// One database listener per process fans out to every watching browser, rather
	// than one connection per viewer (BE-0.24).
	hub := jobs.NewHub(db)
	go func() {
		if err := hub.Listen(listenerCtx); err != nil {
			logger.ErrorContext(ctx, "job event listener stopped", "error", err)
		}
	}()

	jobClient := jobs.NewClient(db, redisOptions, jobRegistry, settingsService, hub)
	a.onClose(func() {
		if err := jobClient.Close(); err != nil {
			logger.WarnContext(ctx, "close queue client", "error", err)
		}
	})

	jobsService := jobs.NewService(jobClient, projectsService, recorder, llm.NewGuard(aiGateway))

	// Execution. The API process checks the target and writes the run row; the
	// worker is what actually starts a container, so nothing here talks to Docker.
	targetsService := targets.NewService(settingsService, nil)
	riskGuard := riskcontrol.NewGuard(db, settingsService)
	runsService := runs.NewService(db, settingsService, targetsService,
		runs.WithLogNotifier(hub), runs.WithObjectStore(objects))

	// Analysis and the built-in defect tracker. The tracker registers as the
	// DefectTracker capability first, so every external adapter added later degrades
	// to it rather than replacing it (BE-5.5.5).
	analysesService := analyses.NewService(db)
	defectsService := defects.NewService(db)

	trackers := defecttracker.NewRegistry()
	trackers.Register(defects.NewBuiltIn(defectsService))

	// The Jira adapter registers behind the built-in, which stays the source of truth.
	// It is active only when Jira is configured; unconfigured, its Available() is false
	// and the mirror pushes to nothing (BE-10.2).
	trackers.Register(jira.NewTracker(settingsService, nil))

	// The mirror pushes a filed defect to whatever external tracker is active and stores
	// where it landed. It alerts through the notification service on a failure, so a
	// broken Jira degrades to an admin alert rather than a lost bug (BE-10.6).
	defectMirror := defects.NewMirror(trackers, defectsService, notificationsService, cfg.AppURL.String())

	defectsHandler := defects.NewHandler(
		defectsService, projectsService, promotionSource{analyses: analysesService}, recorder,
		defects.WithMirror(defectMirror))

	// Reports are generated in a job and downloaded as a file, so this process writes
	// the row and queues the work; the worker renders it (BE-5.9).
	reportsService := reports.NewService(db, objects)

	// Repositories. This process stores the connection and queues a sync; the clone
	// happens in the worker, and the token never leaves it (BE-6.1).
	reposService := repos.NewService(db, settingsService)
	coverageService := coverage.NewService(db)

	// Extraction continues into generation by itself unless a project asked to
	// review it first, so the pipeline needs a way to start the next chain. Deps is
	// a value, so this has to happen before the handlers are built from it: that
	// ordering is the reason registration is here rather than beside the registry.
	pipeline.Chains = jobsService
	registerJobHandlers(jobRegistry, artifactsService, aiGateway, pipeline, testFilesService)

	redisClient, err := jobs.RedisClient(cfg.RedisURL)
	if err != nil {
		return nil, err
	}
	a.onClose(func() {
		if err := redisClient.Close(); err != nil {
			logger.WarnContext(ctx, "close redis client", "error", err)
		}
	})

	triggers := runtrigger.NewRegistry()
	triggers.Register(runtrigger.NewManual())
	webhookTrigger := runtrigger.NewWebhook(
		settingsService, jobsService, runtrigger.NewRedisReplayGuard(redisClient))
	triggers.Register(webhookTrigger)

	// A missing AI provider is reported by setup rather than blocking it: only an
	// admin and reachable storage stop setup completing, and an AI job is refused
	// at enqueue with a message naming what to configure.
	setupService := setup.NewService(usersService, authService, settingsService, objects, aiService)

	// Readiness covers exactly what this process needs to serve traffic. Probes
	// are listed here rather than discovered, so reading main tells you what
	// /readyz actually checks.
	healthService := health.New(version,
		health.Probe{Name: "database", Check: db.Ping},
		health.Probe{Name: "queue", Check: jobClient.Ping},
		health.Probe{Name: "object_store", Check: func(probeCtx context.Context) error {
			if !objects.Available(probeCtx) {
				return fmt.Errorf("object store %q is not reachable", objects.ID())
			}
			return nil
		}},
	)

	handler, err := newRouter(
		&server{
			healthAPI:    health.NewHandler(healthService),
			authAPI:      auth.NewHandler(authService, cfg.AppURL),
			usersAPI:     users.NewHandler(usersService),
			settingsAPI:  settings.NewHandler(settingsService),
			setupAPI:     setup.NewHandler(setupService),
			projectsAPI:  projects.NewHandler(projectsService),
			artifactsAPI: artifacts.NewHandler(artifactsService),
			jobsAPI:      jobs.NewHandler(jobsService, hub),
			aiAPI:        llm.NewHandler(aiService, aiGateway, settingsService),
			generationAPI: generation.NewHandler(
				pipeline, projectsService, testFilesService, projectsService),
			executionAPI: runs.NewHandler(
				runsService, projectsService, jobsService, recorder, hub),
			analysisAPI: analyses.NewHandler(
				analysesService, runsService, projectsService, jobsService, defectsHandler),
			defectsAPI: defectsHandler,
			reportsAPI: reports.NewHandler(
				reportsService, projectsService, reportQueue{jobs: jobsService}),
			repositoryAPI: repos.NewHandler(reposService, projectsService,
				repoSecrets{settings: settingsService}, repoQueue{jobs: jobsService}, recorder),
			coverageAPI: coverage.NewHandler(
				coverageService, projectsService, repoQueue{jobs: jobsService}),
			uiTestsAPI: uitests.NewHandler(uitests.NewService(db), projectsService,
				uiQueue{jobs: jobsService}, objects),

			// Generation is deterministic and free: a seeded faker over the project's own
			// schemas. The enricher is the narrow AI path for fields where semantics
			// matter, and it is opt-in per field (BE-8.1, BE-8.2).
			// The mock's own container runs on a worker, so this process reads the row and
			// enqueues the lifecycle. Its service is built without a container driver,
			// which is what makes that split explicit rather than incidental.
			mocksAPI: mocks.NewHandler(
				mocks.NewService(db, ingestService, nil, settingsService, nil),
				projectsService, mockQueue{jobs: jobsService}, recorder),

			testDataAPI: datagen.NewHandler(
				datagen.NewService(ingestService,
					datagen.WithEnricher(datagen.NewAIEnricher(aiGateway)),
					datagen.WithDumps(dumpReader{artifacts: artifactsService, objects: objects})),
				projectsService),

			// Performance and security. The container work runs on a worker; this process
			// resolves the target, gates the run, and enqueues it (BE-9).
			// MCP servers: the registry, connection test, and call audit. Admin-scoped,
			// credentialed, and per-project (BE-10.1).
			mcpAPI: mcp.NewHandler(mcp.NewService(db, cipher), aiGateway, recorder),

			// The integration health report reads across every capability registry, so
			// it is wired after they are all constructed (BE-10.6).
			integrationsAPI: integrations.NewHandler(integrations.Registries{
				Notifiers:      notifiers,
				Trackers:       trackers,
				SourceProvider: sourceProviders,
				Triggers:       triggers,
				ObjectStore:    objects,
			}),

			perfAPI: perf.NewHandler(
				projectsService, targetsService, riskGuard,
				perfQueue{jobs: jobsService}, perfSettings{settings: settingsService}, recorder),
			securityAPI: security.NewHandler(
				projectsService, targetsService, riskGuard,
				securityQueue{jobs: jobsService}, security.NewService(db), runsService, recorder),
			webhooksAPI:      jobs.NewWebhookHandler(webhookTrigger, recorder),
			notificationsAPI: notifications.NewHandler(notificationsService),
			auditAPI:         audit.NewHandler(audit.NewService(db)),
		},
		policies(),
		auth.SessionMiddleware(sessionManager, authService),
		projects.RequireMembership(projectsService),
	)
	if err != nil {
		return nil, err
	}

	a.handler = handler
	return a, nil
}

func serve(ctx context.Context, logger *slog.Logger, cfg config.Config, handler http.Handler) error {
	srv := &http.Server{
		Addr:    net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler: handler,

		ReadHeaderTimeout: 10 * time.Second,

		// No WriteTimeout on purpose: the SSE endpoints stream for as long as a
		// job runs, and a write deadline would cut them off mid-stage. Individual
		// handlers bound their own work through the request context.
		IdleTimeout: 2 * time.Minute,

		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errs := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("serve: %w", err)
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	// Stop accepting, let in-flight requests finish, then close the pool via the
	// deferred Close in run.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// artifactRefs adapts the artifacts service to what the generation pipeline
// declared it needs.
//
// The pipeline asks for the three fields it uses rather than the artifacts domain
// type, so a column added to an artifact does not ripple into a package that only
// wanted to know where the bytes are (backend-standards.md 3).
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

// promotionSource adapts the analysis service to what the defect tracker declared it
// needs, the same way artifactRefs adapts artifacts for the generation pipeline.
//
// It exists so the tracker does not import the analysis package: a defect filed by
// hand needs nothing from it, and a dependency that only one code path uses is still
// a dependency.
type promotionSource struct {
	analyses *analyses.Service
}

func (p promotionSource) LatestFor(
	ctx context.Context,
	resultID uuid.UUID,
) (defects.PromotionSource, bool, error) {
	analysis, found, err := p.analyses.Latest(ctx, resultID)
	if err != nil || !found {
		return defects.PromotionSource{}, found, err
	}

	result, err := p.analyses.Result(ctx, resultID)
	if err != nil {
		return defects.PromotionSource{}, false, err
	}

	projectID, err := p.analyses.ProjectForResult(ctx, resultID)
	if err != nil {
		return defects.PromotionSource{}, false, err
	}

	return defects.PromotionSource{
		AnalysisID: analysis.ID,
		Reason:     analysis.Reason,
		RootCause:  analysis.RootCause,
		Fix:        analysis.SuggestedFix,
		RunID:      result.RunID,
		ProjectID:  projectID,
		TestName:   result.Name,
		TestCaseID: result.TestCaseID,
	}, true, nil
}

// reportQueue adapts the jobs service to the one method the report handler declared
// it needs: a report is a standalone job, not a chain, and the handler should not
// learn the difference.
type reportQueue struct {
	jobs *jobs.Service
}

func (q reportQueue) SubmitReport(
	ctx context.Context,
	projectID, reportID uuid.UUID,
	actor *uuid.UUID,
) (uuid.UUID, error) {
	principal := httpx.Principal{}
	if actor != nil {
		principal.UserID = *actor
	}

	job, err := q.jobs.SubmitStandalone(ctx, principal, reports.TypeReport, projectID,
		reports.Payload{ReportID: reportID, ProjectID: projectID})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// repoSecrets adapts the settings service to the two writes the repository handler
// performs. Narrowed on purpose: a package that can write one secret should not be
// handed the whole settings service.
type repoSecrets struct {
	settings *settings.Service
}

func (r repoSecrets) Write(
	ctx context.Context,
	projectID uuid.UUID,
	key, value string,
	actor httpx.Principal,
) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}

	_, err = r.settings.Write(ctx,
		settings.Actor{UserID: actor.UserID, Email: actor.Email, Role: actor.Role},
		settings.WriteRequest{
			Key:     key,
			Scope:   settings.ScopeProject,
			ScopeID: &projectID,
			Raw:     raw,
		})
	return err
}

func (r repoSecrets) Clear(
	ctx context.Context,
	projectID uuid.UUID,
	key string,
	actor httpx.Principal,
) error {
	return r.settings.Clear(ctx,
		settings.Actor{UserID: actor.UserID, Email: actor.Email, Role: actor.Role},
		key, settings.ScopeProject, &projectID)
}

// repoQueue adapts the jobs service to the one method the repository handler needs.
type repoQueue struct {
	jobs *jobs.Service
}

func (q repoQueue) SubmitCoverage(
	ctx context.Context,
	projectID uuid.UUID,
	ref string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		coverage.TypeMeasure, projectID,
		coverage.Payload{ProjectID: projectID, Ref: ref, RequestID: uuid.New()})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

func (q repoQueue) SubmitUnitTests(
	ctx context.Context,
	projectID uuid.UUID,
	ref string,
	targets []string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		comprehension.TypeUnitTests, projectID,
		comprehension.UnitTestPayload{
			ProjectID: projectID, Ref: ref, Targets: targets, RequestID: uuid.New(),
		})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

func (q repoQueue) SubmitComprehend(
	ctx context.Context,
	projectID uuid.UUID,
	ref string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		comprehension.TypeComprehend, projectID,
		comprehension.Payload{ProjectID: projectID, Ref: ref, RequestID: uuid.New()})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// mockQueue adapts the jobs service to the two methods the mock handler needs.
type mockQueue struct {
	jobs *jobs.Service
}

func (q mockQueue) SubmitMockStart(
	ctx context.Context,
	projectID uuid.UUID,
	payload mocks.StartPayload,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: payload.ActorID},
		mocks.TypeStart, projectID, payload)
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

func (q mockQueue) SubmitMockStop(
	ctx context.Context,
	projectID uuid.UUID,
	payload mocks.StopPayload,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: payload.ActorID},
		mocks.TypeStop, projectID, payload)
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// perfQueue adapts the jobs service to the one method the performance handler needs.
type perfQueue struct {
	jobs *jobs.Service
}

func (q perfQueue) SubmitPerf(
	ctx context.Context,
	projectID uuid.UUID,
	payload perf.Payload,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: payload.ActorID},
		perf.TypeRun, projectID, payload)
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// securityQueue adapts the jobs service to the one method the security handler needs.
type securityQueue struct {
	jobs *jobs.Service
}

func (q securityQueue) SubmitScan(
	ctx context.Context,
	projectID uuid.UUID,
	payload security.ScanPayload,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: payload.ActorID},
		security.TypeScan, projectID, payload)
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

// perfSettings reads the concurrency ceiling for the performance handler.
type perfSettings struct {
	settings *settings.Service
}

func (p perfSettings) MaxVirtualUsers(ctx context.Context, projectID uuid.UUID) (int, error) {
	return p.settings.Int(ctx, "performance.max_virtual_users",
		settings.Target{ProjectID: &projectID})
}

// dumpReader adapts the artifact store to the one method the test-data service
// declared: read this project's uploaded schema dump.
//
// The project check is here rather than in the service, because ownership of an
// artifact is the artifact store's fact: a dump belonging to another project is
// reported as not found, which is the same rule every other cross-project read follows.
type dumpReader struct {
	artifacts *artifacts.Service
	objects   objectstore.Store
}

func (d dumpReader) ReadDump(
	ctx context.Context,
	projectID, artifactID uuid.UUID,
) (string, error) {
	artifact, err := d.artifacts.StorageKeyFor(ctx, artifactID)
	if err != nil {
		return "", err
	}
	if artifact.ProjectID != projectID {
		return "", apierr.ArtifactNotFound(artifactID)
	}
	if artifact.Kind != artifacts.KindSQLDump {
		return "", apierr.Validation(
			fmt.Sprintf("That artifact is a %s, not a SQL dump.", artifact.Kind),
			map[string]any{"field": "dumpArtifactId"})
	}
	if artifact.SizeBytes > maxDumpBytes {
		// A dump this large is a data dump rather than a schema-only one, and reading it
		// into memory to find CREATE TABLE statements would be a request that costs the
		// process its heap.
		return "", apierr.Validation(
			fmt.Sprintf("That dump is %d bytes. Upload a schema-only dump (pg_dump --schema-only), "+
				"which is normally well under %d.", artifact.SizeBytes, maxDumpBytes),
			map[string]any{"field": "dumpArtifactId"})
	}

	body, err := d.objects.Get(ctx, artifact.StorageKey)
	if err != nil {
		return "", apierr.StorageUnreachable(err)
	}
	defer func() {
		if err := body.Close(); err != nil {
			slog.WarnContext(ctx, "close a dump stream", "artifact_id", artifactID, "error", err)
		}
	}()

	content, err := io.ReadAll(io.LimitReader(body, maxDumpBytes))
	if err != nil {
		return "", apierr.StorageUnreachable(err)
	}
	return string(content), nil
}

// maxDumpBytes bounds what the dump parser reads. A schema-only dump of a large
// application is a few hundred kilobytes; anything past this is data.
const maxDumpBytes = 8 << 20

// uiQueue adapts the jobs service to the one method the UI-testing handler needs.
type uiQueue struct {
	jobs *jobs.Service
}

func (q uiQueue) SubmitDiscovery(
	ctx context.Context,
	projectID uuid.UUID,
	target string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		uitests.TypeDiscover, projectID,
		uitests.Payload{ProjectID: projectID, TargetURL: target, RequestID: uuid.New()})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

func (q uiQueue) SubmitUITests(
	ctx context.Context,
	projectID uuid.UUID,
	flowID *uuid.UUID,
	flows []string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		uitests.TypeGenerate, projectID,
		uitests.GeneratePayload{
			ProjectID: projectID, FlowID: flowID, Flows: flows, RequestID: uuid.New(),
		})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}

func (q repoQueue) SubmitSync(
	ctx context.Context,
	projectID uuid.UUID,
	ref string,
	actor uuid.UUID,
) (uuid.UUID, error) {
	job, err := q.jobs.SubmitStandalone(ctx, httpx.Principal{UserID: actor},
		repos.TypeSync, projectID,
		repos.Payload{ProjectID: projectID, Ref: ref, RequestID: uuid.New()})
	if err != nil {
		return uuid.Nil, err
	}
	return job.ID, nil
}
