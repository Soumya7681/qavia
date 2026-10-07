package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Notifier is the slice of the notifications service this package needs.
type Notifier interface {
	Notify(ctx context.Context, to notifier.Recipient, msg notifier.Message) error
}

// Directory resolves a user ID into somebody a notification can be addressed to.
type Directory interface {
	Recipient(ctx context.Context, userID uuid.UUID) (notifier.Recipient, error)
}

// eventPreviewLimit is how many recent log lines a job carries in a JSON response
// before the SSE stream takes over.
const eventPreviewLimit = 50

// Worker runs jobs.
//
// It owns every status transition. A handler reports progress and narrates; the
// runner decides what queued, running, succeeded, failed, and cancelled mean, so
// the rules live in one place instead of in each handler
// (backend-standards.md 8).
type Worker struct {
	db        *store.DB
	client    *Client
	registry  *Registry
	hub       *Hub
	settings  Settings
	notifier  Notifier
	directory Directory

	server *asynq.Server
}

// WorkerOptions is what the worker needs beyond its dependencies.
type WorkerOptions struct {
	Concurrency int

	// ShutdownTimeout bounds draining on SIGTERM. A handler that has not finished
	// by then has its context cancelled and the job is retried, which is safe
	// precisely because every handler is idempotent.
	ShutdownTimeout time.Duration
}

func NewWorker(
	db *store.DB,
	client *Client,
	registry *Registry,
	hub *Hub,
	settingsService Settings,
	notifierService Notifier,
	directory Directory,
	redis asynq.RedisConnOpt,
	opts WorkerOptions,
) *Worker {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}

	worker := &Worker{
		db:        db,
		client:    client,
		registry:  registry,
		hub:       hub,
		settings:  settingsService,
		notifier:  notifierService,
		directory: directory,
	}

	worker.server = asynq.NewServer(redis, asynq.Config{
		Concurrency: opts.Concurrency,
		Queues:      Queues(),

		// Exponential with a floor, so a provider that is briefly rate limiting is
		// not hammered, and a transient database blip does not cost minutes.
		RetryDelayFunc: asynq.RetryDelayFunc(func(n int, _ error, _ *asynq.Task) time.Duration {
			return retryDelay(n)
		}),

		ShutdownTimeout: opts.ShutdownTimeout,
		Logger:          asynqLogger{},

		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			slog.ErrorContext(ctx, "job failed",
				"type", task.Type(),
				"retry", retryCount(ctx),
				"error", err)
		}),
	})

	return worker
}

// Run processes jobs until the context is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	mux := asynq.NewServeMux()
	for _, typeName := range w.registry.Types() {
		mux.HandleFunc(typeName, w.process)
	}

	done := make(chan error, 1)
	go func() { done <- w.server.Run(mux) }()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("run worker: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Stop accepting first, then drain: a job interrupted mid-handler is
		// retried, and retrying is only safe because handlers are idempotent.
		w.server.Shutdown()
		return nil
	}
}

// process runs one task.
//
// Every path through this function leaves the row in a status that matches
// reality, because a job whose row says running forever is indistinguishable from
// a job that is genuinely stuck.
func (w *Worker) process(ctx context.Context, task *asynq.Task) error {
	var message envelope
	if err := json.Unmarshal(task.Payload(), &message); err != nil {
		// Nothing to retry: the task is unreadable, so retrying re-reads the same
		// bytes. SkipRetry moves it to the archive where it can be inspected.
		return fmt.Errorf("%w: decode task envelope: %w", asynq.SkipRetry, err)
	}

	ctx = logging.WithCorrelationID(ctx, message.CorrelationID)
	ctx = logging.WithJobID(ctx, message.JobID.String())

	// A task with no job ID came from the scheduler, which enqueues recurring work
	// before any row exists. The row is created here, keyed by what the handler
	// declares, so a scheduled sweep is an ordinary job with ordinary events and an
	// ordinary history rather than something that runs invisibly (BE-0.20).
	var job Job
	if message.JobID == uuid.Nil {
		adopted, err := w.client.Adopt(ctx, task.Type(), task.Payload())
		if err != nil {
			return fmt.Errorf("%w: adopt scheduled task %q: %w", asynq.SkipRetry, task.Type(), err)
		}
		job = adopted
		ctx = logging.WithJobID(ctx, job.ID.String())
	} else {
		loaded, err := w.client.Get(ctx, message.JobID)
		if err != nil {
			// The row is the durable record. Without it there is nothing to run and
			// nothing to record, so this is not retried.
			return fmt.Errorf("%w: load job %s: %w", asynq.SkipRetry, message.JobID, err)
		}
		job = loaded
	}

	if job.Status.Terminal() {
		// Cancelled while queued, or already finished by an earlier delivery.
		slog.InfoContext(ctx, "skipping job in a terminal state",
			"job_id", job.ID, "status", string(job.Status))
		return nil
	}

	handler, known := w.registry.lookup(job.Type)
	if !known {
		w.fail(ctx, job, fmt.Errorf("no handler registered for %q", job.Type))
		return fmt.Errorf("%w: no handler for %q", asynq.SkipRetry, job.Type)
	}

	if _, err := w.db.Queries().MarkJobRunning(ctx, job.ID); err != nil {
		return fmt.Errorf("mark job %s running: %w", job.ID, err)
	}
	w.markChainRunning(ctx, job)
	w.hub.Publish(ctx, job.ID)

	jc := &jobContext{worker: w, jobID: job.ID, parentID: job.ParentJobID}
	start := time.Now()

	err := handler.handle(ctx, job.Payload, jc)
	switch {
	case err == nil:
		w.succeed(ctx, job, time.Since(start))
		return nil

	case errors.Is(err, context.Canceled) && ctx.Err() != nil:
		// Either the job was cancelled or the worker is shutting down. Neither is a
		// failure of the work: the row already says cancelled in the first case, and
		// the task is redelivered in the second.
		slog.InfoContext(ctx, "job interrupted", "job_id", job.ID)
		return nil //nolint:nilerr // cancellation is not a failure: the row already says so, or the task is redelivered

	default:
		if lastAttempt(ctx) {
			w.fail(ctx, job, err)
			return err
		}

		if _, markErr := w.db.Queries().MarkJobQueuedForRetry(ctx, dbgen.MarkJobQueuedForRetryParams{
			ID: job.ID, Error: errorText(err),
		}); markErr != nil {
			slog.ErrorContext(ctx, "mark job for retry", "job_id", job.ID, "error", markErr)
		}
		// JobContext takes no context by design (backend-standards.md 8): its writes
		// have to survive the job's context being cancelled, which is exactly when
		// the last line matters most.
		jc.Event("Attempt failed, retrying: %s", err) //nolint:contextcheck // see above
		w.hub.Publish(ctx, job.ID)
		return err
	}
}

// succeed records the stage, then advances the chain.
func (w *Worker) succeed(ctx context.Context, job Job, elapsed time.Duration) {
	if _, err := w.db.Queries().MarkJobSucceeded(ctx, job.ID); err != nil {
		slog.ErrorContext(ctx, "mark job succeeded", "job_id", job.ID, "error", err)
	}
	slog.InfoContext(ctx, "job succeeded",
		"job_id", job.ID, "type", job.Type, "duration_ms", elapsed.Milliseconds())
	w.publishChain(ctx, job)

	w.advance(ctx, job)
}

// fail records a terminal failure and tells whoever submitted it.
//
// A user who has closed the browser learns from the notification centre rather
// than by coming back to look, which is the whole promise of the job pipeline
// (FR-1.6, FR-8.5).
func (w *Worker) fail(ctx context.Context, job Job, cause error) {
	if _, err := w.db.Queries().MarkJobFailed(ctx, dbgen.MarkJobFailedParams{
		ID: job.ID, Error: errorText(cause),
	}); err != nil {
		slog.ErrorContext(ctx, "mark job failed", "job_id", job.ID, "error", err)
	}
	w.appendEvent(ctx, job.ID, LevelError, "Failed: %s", cause)
	w.publishChain(ctx, job)

	parentID := job.ID
	if job.ParentJobID != nil {
		parentID = *job.ParentJobID
		if _, err := w.db.Queries().MarkJobFailed(ctx, dbgen.MarkJobFailedParams{
			ID: parentID, Error: errorText(cause),
		}); err != nil {
			slog.ErrorContext(ctx, "mark chain failed", "job_id", parentID, "error", err)
		}
		w.hub.Publish(ctx, parentID)
	}

	w.notify(ctx, job, notifier.Message{
		Kind:  notifier.KindJobFailed,
		Title: fmt.Sprintf("%s failed", chainLabel(job)),
		Body:  cause.Error(),
		Link:  jobLink(job, parentID),
	})
}

// advance enqueues the next stage, or completes the chain.
//
// A handler never calls the next handler directly. Each stage is enqueued, so it
// is independently retryable and independently observable, and the chain is
// declared in pipeline.go rather than implied by what each handler happens to do
// (backend-standards.md 8).
func (w *Worker) advance(ctx context.Context, job Job) {
	var payload StagePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.Chain == "" {
		// Not a chained job. Standalone jobs such as retention end here.
		return
	}

	stages, known := StagesFor(payload.Chain)
	if !known {
		slog.ErrorContext(ctx, "job belongs to an unknown chain",
			"job_id", job.ID, "chain", string(payload.Chain))
		return
	}

	next := payload.Stage + 1
	if next < len(stages) {
		nextPayload := payload
		nextPayload.Stage = next

		if _, err := w.client.Enqueue(ctx, EnqueueRequest{
			Type:        stages[next],
			ProjectID:   job.ProjectID,
			Payload:     nextPayload,
			ParentJobID: &payload.ParentJobID,
			EnqueuedBy:  job.EnqueuedBy,
		}); err != nil {
			slog.ErrorContext(ctx, "enqueue next stage",
				"job_id", job.ID, "chain", string(payload.Chain), "stage", next, "error", err)
			w.fail(ctx, job, fmt.Errorf("could not start the next stage: %w", err))
		}
		return
	}

	// Last stage. The chain is complete, which is what the parent row records and
	// what the person who submitted it is told about.
	if _, err := w.db.Queries().MarkJobSucceeded(ctx, payload.ParentJobID); err != nil {
		slog.ErrorContext(ctx, "mark chain succeeded",
			"job_id", payload.ParentJobID, "error", err)
	}
	w.hub.Publish(ctx, payload.ParentJobID)

	w.notify(ctx, job, notifier.Message{
		Kind:  notifier.KindJobCompleted,
		Title: fmt.Sprintf("%s finished", chainLabel(job)),
		Body:  "Every stage completed.",
		Link:  jobLink(job, payload.ParentJobID),
	})
}

// markChainRunning moves the parent row out of queued when its first stage starts,
// so a chain does not look untouched while it is halfway through.
//
// It uses its own query rather than MarkJobRunning because that one counts an
// attempt, and a parent is not attempted: a three-stage chain would report three
// attempts on a submission that never failed once.
func (w *Worker) markChainRunning(ctx context.Context, job Job) {
	if job.ParentJobID == nil {
		return
	}
	if _, err := w.db.Queries().MarkChainRunning(ctx, *job.ParentJobID); err != nil {
		slog.ErrorContext(ctx, "mark chain running", "job_id", *job.ParentJobID, "error", err)
	}
	w.hub.Publish(ctx, *job.ParentJobID)
}

// notify tells the person who submitted the work how it went.
func (w *Worker) notify(ctx context.Context, job Job, msg notifier.Message) {
	if w.notifier == nil || w.directory == nil || job.EnqueuedBy == nil {
		// A system-triggered job, such as a scheduled drift check, has nobody
		// waiting on it.
		return
	}

	recipient, err := w.directory.Recipient(ctx, *job.EnqueuedBy)
	if err != nil {
		slog.ErrorContext(ctx, "resolve job notification recipient",
			"job_id", job.ID, "user_id", *job.EnqueuedBy, "error", err)
		return
	}
	if err := w.notifier.Notify(ctx, recipient, msg); err != nil {
		slog.ErrorContext(ctx, "notify job outcome", "job_id", job.ID, "error", err)
	}
}

// publishChain wakes the watchers of a stage and of its parent.
func (w *Worker) publishChain(ctx context.Context, job Job) {
	w.hub.Publish(ctx, job.ID)
	if job.ParentJobID != nil {
		w.hub.Publish(ctx, *job.ParentJobID)
	}
}

func (w *Worker) appendEvent(ctx context.Context, jobID uuid.UUID, level EventLevel, format string, args ...any) {
	if _, err := w.db.Queries().AppendJobEvent(ctx, dbgen.AppendJobEventParams{
		JobID:   jobID,
		Level:   dbgen.JobEventLevel(level),
		Message: truncate(fmt.Sprintf(format, args...), 2000),
	}); err != nil {
		slog.ErrorContext(ctx, "append job event", "job_id", jobID, "error", err)
	}
}

// jobContext is what a handler sees. It writes through to the row and the event
// log, which is what the SSE stream reads (BE-0.24).
type jobContext struct {
	worker   *Worker
	jobID    uuid.UUID
	parentID *uuid.UUID
}

func (j *jobContext) JobID() uuid.UUID { return j.jobID }

// Progress and Event write on a background context on purpose. A cancelled job
// still needs its last progress and its final line recorded: those writes are what
// tell a watching user why it stopped, and inheriting the cancelled context would
// discard exactly the information that matters.
func (j *jobContext) Progress(percent int) {
	percent = min(max(percent, 0), 100)

	ctx := context.Background()
	if _, err := j.worker.db.Queries().SetJobProgress(ctx, dbgen.SetJobProgressParams{
		ID: j.jobID, Progress: int16(percent),
	}); err != nil {
		slog.ErrorContext(ctx, "set job progress", "job_id", j.jobID, "error", err)
	}
	j.publish(ctx)
}

func (j *jobContext) Event(format string, args ...any) {
	ctx := context.Background()
	j.worker.appendEvent(ctx, j.jobID, LevelInfo, format, args...)
	j.publish(ctx)
}

// publish wakes the watchers of this stage and of the chain it belongs to.
//
// Both, because a browser watches the submission rather than whichever stage
// happens to be running: without the parent notification a chain would look frozen
// between stages, and NFR-3 asks for an update within five seconds.
func (j *jobContext) publish(ctx context.Context) {
	j.worker.hub.Publish(ctx, j.jobID)
	if j.parentID != nil {
		j.worker.hub.Publish(ctx, *j.parentID)
	}
}

// retryDelay is exponential with a floor and a ceiling.
func retryDelay(attempt int) time.Duration {
	const (
		base     = 5 * time.Second
		maxDelay = 5 * time.Minute
	)

	delay := base << min(attempt, 6)
	return min(delay, maxDelay)
}

// lastAttempt reports whether asynq has any retries left for this task.
func lastAttempt(ctx context.Context) bool {
	retried, retriedKnown := asynq.GetRetryCount(ctx)
	maxRetry, maxKnown := asynq.GetMaxRetry(ctx)
	if !retriedKnown || !maxKnown {
		// Outside a task context, which happens only in a direct call from a test.
		return true
	}
	return retried >= maxRetry
}

func retryCount(ctx context.Context) int {
	count, _ := asynq.GetRetryCount(ctx)
	return count
}

// errorText prepares a failure for the row.
//
// Truncated, because an error chain can be long and the column is read in a UI.
// Nothing is added to it: an error message that reached here has already passed
// through the domain-error rules, which forbid a credential in one.
func errorText(err error) *string {
	if err == nil {
		return nil
	}
	text := truncate(err.Error(), 2000)
	return &text
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit-1] + "…"
}

func chainLabel(job Job) string {
	if chain := job.Chain(); chain != "" {
		if label, known := chainLabels[chain]; known {
			return label
		}
		return string(chain)
	}
	return job.Type
}

// jobLink is the deep link a notification carries (FR-8.5). It is a path, so it
// stays correct if the platform's hostname changes.
func jobLink(job Job, parentID uuid.UUID) string {
	if job.ProjectID == nil {
		return fmt.Sprintf("/jobs/%s", parentID)
	}
	return fmt.Sprintf("/projects/%s/jobs/%s", *job.ProjectID, parentID)
}

// asynqLogger routes the queue library's own logging into slog, so a worker
// produces one stream of structured lines rather than two formats.
type asynqLogger struct{}

func (asynqLogger) Debug(args ...any) { slog.Debug(fmt.Sprint(args...), "source", "asynq") }
func (asynqLogger) Info(args ...any)  { slog.Info(fmt.Sprint(args...), "source", "asynq") }
func (asynqLogger) Warn(args ...any)  { slog.Warn(fmt.Sprint(args...), "source", "asynq") }
func (asynqLogger) Error(args ...any) { slog.Error(fmt.Sprint(args...), "source", "asynq") }
func (asynqLogger) Fatal(args ...any) { slog.Error(fmt.Sprint(args...), "source", "asynq") }
