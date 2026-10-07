package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Settings is the slice of the settings service this package needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
}

// EnqueueRequest is one job to run.
type EnqueueRequest struct {
	Type      string
	ProjectID *uuid.UUID

	// Payload is marshalled into the row. The row is the durable record; Redis
	// carries only the job ID.
	Payload any

	// IdempotencyKey defaults to the handler's own key derived from the payload.
	// A caller overrides it when an outside system supplied one, such as the
	// Idempotency-Key header on a retried webhook.
	IdempotencyKey string

	ParentJobID *uuid.UUID
	EnqueuedBy  *uuid.UUID
}

// Client enqueues work and answers questions about it.
//
// Every enqueue writes the row first and pushes to Redis second. That order is the
// durability guarantee: a job that exists in the database but not in Redis is
// visible, diagnosable, and re-pushable, while the reverse would be work nobody
// can see.
type Client struct {
	db        *store.DB
	queue     *asynq.Client
	inspector *asynq.Inspector
	registry  *Registry
	settings  Settings
	hub       *Hub
}

func NewClient(
	db *store.DB,
	redis asynq.RedisConnOpt,
	registry *Registry,
	settingsService Settings,
	hub *Hub,
) *Client {
	return &Client{
		db:        db,
		queue:     asynq.NewClient(redis),
		inspector: asynq.NewInspector(redis),
		registry:  registry,
		settings:  settingsService,
		hub:       hub,
	}
}

// Close releases the Redis connections.
func (c *Client) Close() error {
	return errors.Join(c.queue.Close(), c.inspector.Close())
}

// Enqueue records a job and pushes it.
//
// Idempotency is the unique index on (type, idempotency_key), not a check followed
// by a write: a retried enqueue conflicts, the existing row comes back, and no
// second task is pushed. That is what makes a webhook retry or a double-clicked
// button harmless (backend-standards.md 8).
func (c *Client) Enqueue(ctx context.Context, request EnqueueRequest) (Job, error) {
	handler, known := c.registry.lookup(request.Type)
	if !known {
		// A type nothing can run is refused here rather than becoming a row that
		// fails on every worker that picks it up.
		return Job{}, fmt.Errorf("jobs: no handler registered for type %q", request.Type)
	}

	payload, err := json.Marshal(request.Payload)
	if err != nil {
		return Job{}, fmt.Errorf("jobs: encode payload for %q: %w", request.Type, err)
	}

	key := request.IdempotencyKey
	if key == "" {
		if key, err = handler.idempotencyKey(payload); err != nil {
			return Job{}, err
		}
	}

	maxAttempts, err := c.maxAttempts(ctx)
	if err != nil {
		return Job{}, err
	}

	row, err := c.db.Queries().EnqueueJob(ctx, dbgen.EnqueueJobParams{
		ProjectID:      request.ProjectID,
		Type:           request.Type,
		Payload:        payload,
		IdempotencyKey: key,
		CorrelationID:  logging.CorrelationID(ctx),
		ParentJobID:    request.ParentJobID,
		EnqueuedBy:     request.EnqueuedBy,
		MaxAttempts:    int16(maxAttempts),
	})
	if err != nil {
		return Job{}, fmt.Errorf("enqueue job %q: %w", request.Type, err)
	}

	job := Job{
		ID:             row.ID,
		ProjectID:      row.ProjectID,
		Type:           row.Type,
		Status:         Status(row.Status),
		Progress:       int(row.Progress),
		Attempts:       int(row.Attempts),
		MaxAttempts:    int(row.MaxAttempts),
		Error:          row.Error,
		ParentJobID:    row.ParentJobID,
		Payload:        row.Payload,
		IdempotencyKey: row.IdempotencyKey,
		CorrelationID:  row.CorrelationID,
		EnqueuedBy:     row.EnqueuedBy,
		QueuedAt:       row.QueuedAt,
		StartedAt:      row.StartedAt,
		FinishedAt:     row.FinishedAt,
	}

	if row.Existed {
		// The same work was already submitted. Pushing again would run it twice for
		// no benefit, so the original is returned as-is.
		slog.DebugContext(ctx, "job already enqueued",
			"job_id", job.ID, "type", job.Type, "status", string(job.Status))
		return job, nil
	}

	if err := c.push(ctx, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// push hands the job to Redis.
//
// The task ID is the job ID, so the queue refuses a duplicate too: two API
// processes racing on the same submission produce one task, not two.
// Adopt creates the row for a task that arrived without one (BE-0.20).
//
// The scheduler enqueues recurring work straight onto the queue: it is asynq's
// periodic scheduler, it runs before any row exists, and giving it a database handle
// so it could write one would mean a scheduler that fails when Postgres blinks and a
// second copy of the enqueue rules. So the worker creates the row when the task
// arrives, from the same idempotency key the handler declares — which for a daily
// sweep is the date, so a redelivery finds the row rather than sweeping twice.
//
// Row only, deliberately: pushing would enqueue a second copy of the task that is
// already being processed.
func (c *Client) Adopt(ctx context.Context, typeName string, payload json.RawMessage) (Job, error) {
	handler, known := c.registry.lookup(typeName)
	if !known {
		return Job{}, fmt.Errorf("jobs: no handler registered for type %q", typeName)
	}

	key, err := handler.idempotencyKey(payload)
	if err != nil {
		return Job{}, err
	}

	maxAttempts, err := c.maxAttempts(ctx)
	if err != nil {
		return Job{}, err
	}

	row, err := c.db.Queries().EnqueueJob(ctx, dbgen.EnqueueJobParams{
		Type:           typeName,
		Payload:        payload,
		IdempotencyKey: key,
		CorrelationID:  logging.CorrelationID(ctx),
		MaxAttempts:    int16(maxAttempts),
	})
	if err != nil {
		return Job{}, fmt.Errorf("adopt scheduled job %q: %w", typeName, err)
	}

	return Job{
		ID:             row.ID,
		ProjectID:      row.ProjectID,
		Type:           row.Type,
		Status:         Status(row.Status),
		Progress:       int(row.Progress),
		Attempts:       int(row.Attempts),
		MaxAttempts:    int(row.MaxAttempts),
		Error:          row.Error,
		ParentJobID:    row.ParentJobID,
		Payload:        row.Payload,
		IdempotencyKey: row.IdempotencyKey,
		CorrelationID:  row.CorrelationID,
		EnqueuedBy:     row.EnqueuedBy,
		QueuedAt:       row.QueuedAt,
		StartedAt:      row.StartedAt,
		FinishedAt:     row.FinishedAt,
	}, nil
}

func (c *Client) push(ctx context.Context, job Job) error {
	body, err := json.Marshal(envelope{
		JobID:         job.ID,
		CorrelationID: job.CorrelationID,
	})
	if err != nil {
		return fmt.Errorf("jobs: encode task envelope: %w", err)
	}

	_, err = c.queue.EnqueueContext(ctx,
		asynq.NewTask(job.Type, body),
		asynq.TaskID(job.ID.String()),
		asynq.Queue(string(c.registry.QueueFor(job.Type))),
		// asynq counts retries, not attempts: three attempts is the first run plus
		// two retries (NFR-5).
		asynq.MaxRetry(max(job.MaxAttempts-1, 0)),
	)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, asynq.ErrTaskIDConflict):
		// Another process pushed the same job first. That is the mechanism working.
		return nil
	default:
		return fmt.Errorf("jobs: push %s to the queue: %w", job.ID, err)
	}
}

// Get loads one job.
func (c *Client) Get(ctx context.Context, id uuid.UUID) (Job, error) {
	row, err := c.db.Queries().GetJob(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return Job{}, apierr.JobNotFound(id)
		}
		return Job{}, fmt.Errorf("load job %s: %w", id, err)
	}
	return toJob(row), nil
}

// GetWithChain loads a job, its stages, and its recent events.
//
// A parent carries its stages so one request describes a whole submission. A stage
// asked for directly carries its siblings for the same reason: a user who followed
// a link to stage two still wants to see the shape of what they submitted.
func (c *Client) GetWithChain(ctx context.Context, id uuid.UUID, eventLimit int) (Job, error) {
	job, err := c.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}

	parentID := job.ID
	if job.ParentJobID != nil {
		parentID = *job.ParentJobID
	}

	children, err := c.db.Queries().ListChildJobs(ctx, &parentID)
	if err != nil {
		return Job{}, fmt.Errorf("load chain stages for %s: %w", parentID, err)
	}
	for _, child := range children {
		job.Stages = append(job.Stages, toJob(child))
	}

	events, err := c.Events(ctx, id, 0, eventLimit)
	if err != nil {
		return Job{}, err
	}
	job.Events = events
	return job, nil
}

// List returns a page of a project's jobs, newest first.
func (c *Client) List(ctx context.Context, projectID uuid.UUID, limit int, cursor string) (Page, error) {
	limit = paging.ClampLimit(limit)

	params := dbgen.ListJobsForProjectParams{
		ProjectID: &projectID,
		PageSize:  int32(limit + 1),
	}
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &at
	}

	rows, err := c.db.Queries().ListJobsForProject(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list jobs: %w", err)
	}

	page := Page{Items: make([]Job, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = paging.EncodeTime(rows[i-1].QueuedAt)
			break
		}
		page.Items = append(page.Items, toJob(row))
	}
	return page, nil
}

// Events returns log lines after a given id, oldest first.
//
// A parent gets its whole chain's lines, because handlers narrate against the
// stage that is running and a viewer watching the submission would otherwise see
// an empty log while three stages talked. A stage gets only its own, since it has
// no children.
func (c *Client) Events(ctx context.Context, jobID uuid.UUID, afterID int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > paging.MaxLimit {
		limit = paging.MaxLimit
	}

	rows, err := c.db.Queries().ListChainEventsSince(ctx, dbgen.ListChainEventsSinceParams{
		ID: jobID, ID_2: afterID, PageSize: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("load job events for %s: %w", jobID, err)
	}

	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, toEvent(row))
	}
	return events, nil
}

// Cancel stops a job and every stage of its chain.
//
// Two moves, because a job is in one of two places. A row marked cancelled is what
// a worker checks before it starts, which covers anything still queued; telling
// the queue to cancel processing is what reaches a handler already running, whose
// context is then cancelled. Cancellation is cooperative, so a handler stops
// between units of work rather than being killed mid-write
// (backend-standards.md 8).
func (c *Client) Cancel(ctx context.Context, id uuid.UUID) (Job, error) {
	job, err := c.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if job.Status.Terminal() {
		return Job{}, apierr.JobNotCancelable(string(job.Status))
	}

	targets := []uuid.UUID{job.ID}
	if job.ParentJobID == nil {
		children, err := c.db.Queries().ListChildJobs(ctx, &job.ID)
		if err != nil {
			return Job{}, fmt.Errorf("load chain stages for %s: %w", job.ID, err)
		}
		for _, child := range children {
			targets = append(targets, child.ID)
		}
	}

	for _, target := range targets {
		if _, err := c.db.Queries().CancelJob(ctx, target); err != nil {
			return Job{}, fmt.Errorf("cancel job %s: %w", target, err)
		}
		if err := c.inspector.CancelProcessing(target.String()); err != nil {
			// Not found is the common case: the job was queued rather than running,
			// and the row is already marked, so the worker will skip it.
			slog.DebugContext(ctx, "cancel processing",
				"job_id", target, "error", err)
		}
		c.hub.Publish(ctx, target)
	}

	return c.Get(ctx, id)
}

// maxAttempts reads the retry ceiling from settings, so an operator raises it in
// the UI rather than in a constant (backend-standards.md 6).
func (c *Client) maxAttempts(ctx context.Context) (int, error) {
	attempts, err := c.settings.Int(ctx, "jobs.max_attempts", settings.Target{})
	if err != nil {
		return 0, err
	}
	return attempts, nil
}

// Ping reports whether the queue is reachable, for the readiness probe.
func (c *Client) Ping(ctx context.Context) error {
	// The inspector's queue listing is the cheapest call that proves a working
	// Redis connection rather than only a configured one.
	done := make(chan error, 1)
	go func() {
		_, err := c.inspector.Queues()
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("queue unreachable: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RedisClient opens a plain Redis connection for the things that are not the
// queue, such as webhook replay protection.
//
// It parses the same URL asynq does, so there is one connection string and no way
// for the two to disagree about which Redis they are talking to.
func RedisClient(redisURL string) (redis.UniversalClient, error) {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return redis.NewClient(options), nil
}

// RedisOptions parses REDIS_URL into what asynq takes.
//
// It lives here rather than in config because the shape belongs to the queue
// library: config's job is to read and validate the six bootstrap variables, not
// to know what a queue client wants.
func RedisOptions(redisURL string) (asynq.RedisConnOpt, error) {
	options, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return options, nil
}
