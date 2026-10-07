package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Status is where a job is. The set matches the job_status enum, and every
// transition goes through the store methods below so an illegal one is rejected
// in one place rather than wherever somebody wrote an UPDATE.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// Terminal reports whether a job will never change status again.
func (s Status) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Queue is the priority band a job runs in.
//
// Three bands, weighted in the worker: interactive work a person is waiting for,
// ordinary pipeline work, and background upkeep such as retention. Without them a
// nightly retention sweep would sit in front of a user's submission.
type Queue string

const (
	QueueCritical Queue = "critical"
	QueueDefault  Queue = "default"
	QueueLow      Queue = "low"
)

// EventLevel matches the job_event_level enum.
type EventLevel string

const (
	LevelDebug EventLevel = "debug"
	LevelInfo  EventLevel = "info"
	LevelWarn  EventLevel = "warn"
	LevelError EventLevel = "error"
)

// JobContext is what a handler may do to the job it is running.
//
// Deliberately small. A handler reports progress and narrates what it is doing;
// it does not change status, because status is the runner's business and a
// handler that could mark itself succeeded would make retries meaningless
// (backend-standards.md 8).
type JobContext interface {
	JobID() uuid.UUID

	// Progress records a percentage. A job over roughly ten seconds calls it: a
	// user watching a silent bar assumes the system is broken.
	Progress(percent int)

	// Event appends a line to the live log the SSE stream carries.
	Event(format string, args ...any)
}

// TypeHandler is one job type.
//
// The payload type parameter is what keeps a handler honest: it declares the
// shape it needs, and the runner decodes into it rather than handing round a
// map (backend-standards.md 9).
//
// backend-standards.md 8 calls this Handler. The name is TypeHandler here because
// every package names its HTTP handler Handler, and this package has both; the
// shape is unchanged.
type TypeHandler[T any] interface {
	// Type is the registered name, matching the type column on the row.
	Type() string

	// IdempotencyKey must be derived from the payload and stable across retries.
	// It is a unique index, so a second enqueue of the same work returns the
	// original row instead of creating a duplicate. NFR-4 is not optional.
	IdempotencyKey(payload T) string

	Handle(ctx context.Context, payload T, jc JobContext) error
}

// Job is one row as the rest of the platform sees it.
type Job struct {
	ID        uuid.UUID
	ProjectID *uuid.UUID

	Type   string
	Status Status

	Progress    int
	Attempts    int
	MaxAttempts int
	Error       *string

	ParentJobID *uuid.UUID
	Payload     json.RawMessage

	IdempotencyKey string
	CorrelationID  string

	EnqueuedBy *uuid.UUID
	QueuedAt   time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time

	// Stages are the children of a chain's parent job, oldest first. Loaded only
	// where a caller asked for the chain as a unit.
	Stages []Job

	// Events are the most recent log lines, for a first render before the SSE
	// stream takes over.
	Events []Event
}

// Chain returns the chain a job belongs to, read from its payload.
func (j Job) Chain() Chain {
	var payload StagePayload
	if err := json.Unmarshal(j.Payload, &payload); err != nil {
		return ""
	}
	return payload.Chain
}

// Event is one line of a job's log.
type Event struct {
	ID      int64
	Level   EventLevel
	Message string
	At      time.Time
}

// Page is one page of jobs plus the cursor for the next.
type Page struct {
	Items      []Job
	NextCursor string
}

// envelope is what travels through Redis.
//
// The job ID and nothing else that matters: the durable record is the row, and
// Redis holds only in-flight coordination (tech-stack.md 6). Losing Redis loses
// in-flight jobs, which retry, and loses no data.
//
// The correlation ID rides along so one user action stays traceable across five
// stages, which is the whole point of having one (backend-standards.md 12).
type envelope struct {
	JobID         uuid.UUID `json:"jobId"`
	CorrelationID string    `json:"correlationId,omitempty"`
	TraceParent   string    `json:"traceparent,omitempty"`
}

// StagePayload is the payload every pipeline stage receives.
//
// One shape for all of them, because a chain hands the same context from stage to
// stage: which chain, which stage, what to work on. A stage that needs more adds a
// field here rather than inventing its own envelope.
type StagePayload struct {
	Chain Chain `json:"chain"`
	Stage int   `json:"stage"`

	ProjectID  uuid.UUID  `json:"projectId"`
	ArtifactID *uuid.UUID `json:"artifactId,omitempty"`

	// ParentJobID is the chain's parent row, which is what makes a chain queryable
	// as a unit.
	ParentJobID uuid.UUID `json:"parentJobId"`

	// Reference is free text recorded by whatever triggered the chain, such as a
	// branch name from a webhook.
	Reference string `json:"reference,omitempty"`

	// RunID is the row the execute chain writes its results to. It exists before
	// the task is pushed, which is the rule for anything queued: a row with no task
	// is visible and retryable, and a task with no row is invisible (BE-4.9).
	RunID *uuid.UUID `json:"runId,omitempty"`
}

func toJob(row dbgen.Job) Job {
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
	}
}

func toEvent(row dbgen.JobEvent) Event {
	return Event{
		ID:      row.ID,
		Level:   EventLevel(row.Level),
		Message: row.Message,
		At:      row.At,
	}
}

// marshalPayload encodes a payload for the row.
func marshalPayload(payload any) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("jobs: encode payload: %w", err)
	}
	return encoded, nil
}

// decodePayload reads a stored payload into the type a handler declared.
//
// Unknown fields are refused. A payload written by an older release that no longer
// matches is an error a person can see, rather than a field that silently arrives
// as its zero value and changes what the job does (backend-standards.md 9).
func decodePayload[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, fmt.Errorf("jobs: empty payload")
	}

	decoder := json.NewDecoder(newBytesReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&out); err != nil {
		return out, fmt.Errorf("jobs: decode payload: %w", err)
	}
	return out, nil
}
