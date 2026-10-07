package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/platform/logging"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Projects is the slice of the projects service this package needs. Membership
// and archived-state rules live there (backend-standards.md 3).
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Guard is an enqueue-time check that a chain can actually succeed.
//
// It is how "fail early, not inside a worker" is enforced for things the jobs
// package must not know about (backend-standards.md 5): the AI layer refuses a
// chain with no provider configured, no model assigned to the tier it needs, a
// project not approved for the provider's residency, or a budget already spent.
// A user learns that when they press submit.
type Guard interface {
	EnsureReady(ctx context.Context, chain Chain, projectID uuid.UUID) error
}

// Service submits chains and answers questions about them.
type Service struct {
	client   *Client
	projects Projects
	recorder *audit.Recorder
	guards   []Guard
}

func NewService(
	client *Client,
	projectsService Projects,
	recorder *audit.Recorder,
	guards ...Guard,
) *Service {
	return &Service{
		client:   client,
		projects: projectsService,
		recorder: recorder,
		guards:   guards,
	}
}

// ensureReady runs every guard before anything is written.
//
// Before, not after: a chain refused halfway through has already created a parent
// row, an event log, and a notification for work that was never going to run.
func (s *Service) ensureReady(ctx context.Context, chain Chain, projectID uuid.UUID) error {
	for _, guard := range s.guards {
		if err := guard.EnsureReady(ctx, chain, projectID); err != nil {
			return err
		}
	}
	return nil
}

// SubmitInput is one chain submission.
type SubmitInput struct {
	ProjectID  uuid.UUID
	Chain      Chain
	ArtifactID *uuid.UUID

	// Reference is free text from whatever triggered the chain, such as a branch
	// name from a webhook.
	Reference string

	// IdempotencyKey comes from the header where a caller sent one. A CI system
	// that retries a webhook gets the original chain back rather than a second run.
	IdempotencyKey string
}

// Submit creates the chain and starts its first stage.
//
// Everything checkable is checked here rather than inside a worker twenty minutes
// later: the project exists, is not archived, and the chain has stages that can
// actually run. A user learns about a misconfiguration when they press submit
// (backend-standards.md 5).
//
// It returns as soon as the first stage is queued. Nothing waits for the work, so
// the caller can close the browser and be told when it finishes (FR-1.6, NFR-1).
// SubmitStandalone queues one job that is not part of a chain.
//
// Most work here is a chain, because most work has stages worth retrying
// independently. A report is not: it gathers, renders, and stores in one pass, and
// splitting that into three stages would buy nothing but three rows. This is the door
// for that case, and it enforces the same two checks a chain gets — the project is
// live, and something can actually run the type.
func (s *Service) SubmitStandalone(
	ctx context.Context,
	actor httpx.Principal,
	jobType string,
	projectID uuid.UUID,
	payload any,
) (Job, error) {
	if err := s.projects.EnsureActive(ctx, projectID); err != nil {
		return Job{}, err
	}

	job, err := s.client.Enqueue(ctx, EnqueueRequest{
		Type:       jobType,
		ProjectID:  &projectID,
		Payload:    payload,
		EnqueuedBy: &actor.UserID,
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s *Service) Submit(ctx context.Context, actor httpx.Principal, input SubmitInput) (Job, error) {
	if _, declared := StagesFor(input.Chain); !declared {
		return Job{}, apierr.Validation(
			fmt.Sprintf("%q is not a pipeline this platform runs.", input.Chain),
			map[string]any{"field": "chain"})
	}
	if !Runnable(input.Chain) {
		// Declared but not yet implemented. Refused with the name of the thing that
		// is missing rather than accepted and silently dropped (F-3.12).
		return Job{}, apierr.NotImplemented(Label(input.Chain))
	}
	if err := s.projects.EnsureActive(ctx, input.ProjectID); err != nil {
		return Job{}, err
	}
	if err := s.ensureReady(ctx, input.Chain, input.ProjectID); err != nil {
		return Job{}, err
	}

	stages, _ := StagesFor(input.Chain)

	parent, err := s.client.createChain(ctx, input, actor.UserID)
	if err != nil {
		return Job{}, err
	}
	if parent.existed {
		// The same Idempotency-Key was replayed. The original chain is the answer.
		return parent.job, nil
	}

	payload := StagePayload{
		Chain:       input.Chain,
		Stage:       0,
		ProjectID:   input.ProjectID,
		ArtifactID:  input.ArtifactID,
		ParentJobID: parent.job.ID,
		Reference:   input.Reference,
	}

	if _, err := s.client.Enqueue(ctx, EnqueueRequest{
		Type:        stages[0],
		ProjectID:   &input.ProjectID,
		Payload:     payload,
		ParentJobID: &parent.job.ID,
		EnqueuedBy:  &actor.UserID,
	}); err != nil {
		return Job{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionJobSubmitted,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    string(input.Chain),
		ProjectID:  &input.ProjectID,
		Detail:     map[string]any{"jobId": parent.job.ID.String(), "reference": input.Reference},
	})

	return s.client.GetWithChain(ctx, parent.job.ID, eventPreviewLimit)
}

// Get returns one job with its chain, for a caller who may see it.
func (s *Service) Get(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Job, error) {
	job, err := s.client.GetWithChain(ctx, id, eventPreviewLimit)
	if err != nil {
		return Job{}, err
	}
	if err := s.authorize(ctx, actor, job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// List returns a page of a project's jobs.
func (s *Service) List(
	ctx context.Context,
	actor httpx.Principal,
	projectID uuid.UUID,
	limit int,
	cursor string,
) (Page, error) {
	if err := s.projects.EnsureMember(ctx, actor, projectID); err != nil {
		return Page{}, err
	}
	return s.client.List(ctx, projectID, limit, cursor)
}

// Cancel stops a job and its chain.
func (s *Service) Cancel(ctx context.Context, actor httpx.Principal, id uuid.UUID) (Job, error) {
	job, err := s.client.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if err := s.authorize(ctx, actor, job); err != nil {
		return Job{}, err
	}

	cancelled, err := s.client.Cancel(ctx, id)
	if err != nil {
		return Job{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action:     audit.ActionJobCancelled,
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Subject:    job.Type,
		ProjectID:  job.ProjectID,
		Detail:     map[string]any{"jobId": job.ID.String()},
	})
	return s.client.GetWithChain(ctx, cancelled.ID, eventPreviewLimit)
}

// Events returns log lines after a given id, for the first render and for a
// stream that is catching up.
func (s *Service) Events(ctx context.Context, actor httpx.Principal, id uuid.UUID, afterID int64, limit int) ([]Event, error) {
	job, err := s.client.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, actor, job); err != nil {
		return nil, err
	}
	return s.client.Events(ctx, id, afterID, limit)
}

// authorize applies project membership to a job.
//
// A job with no project is platform work, such as retention, and only an admin has
// any business reading it.
func (s *Service) authorize(ctx context.Context, actor httpx.Principal, job Job) error {
	if job.ProjectID == nil {
		if actor.Role != role.Admin {
			return apierr.JobNotFound(job.ID)
		}
		return nil
	}
	return s.projects.EnsureMember(ctx, actor, *job.ProjectID)
}

// chainCreation is a parent row plus whether it already existed.
type chainCreation struct {
	job     Job
	existed bool
}

// createChain writes the parent row for a submission.
//
// The parent is a container, not a task: it is never pushed to Redis, because
// nothing runs it. It exists so a chain is queryable as a unit and so its status
// can say what the submission as a whole is doing (BE-0.23).
func (c *Client) createChain(ctx context.Context, input SubmitInput, actorID uuid.UUID) (chainCreation, error) {
	payload := StagePayload{
		Chain:      input.Chain,
		ProjectID:  input.ProjectID,
		ArtifactID: input.ArtifactID,
		Reference:  input.Reference,
	}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return chainCreation{}, err
	}

	key := input.IdempotencyKey
	if key == "" {
		// No caller-supplied key, so two deliberate submissions of the same chain
		// are two chains. Idempotency here protects against retries, not against
		// somebody choosing to run the same thing twice.
		key = uuid.NewString()
	}

	maxAttempts, err := c.maxAttempts(ctx)
	if err != nil {
		return chainCreation{}, err
	}

	// A schedule or a webhook has no user behind it, and the column is a foreign
	// key: attributing the work to the zero UUID would fail the insert rather than
	// record "nobody".
	var enqueuedBy *uuid.UUID
	if actorID != uuid.Nil {
		enqueuedBy = &actorID
	}

	row, err := c.db.Queries().EnqueueJob(ctx, dbgen.EnqueueJobParams{
		ProjectID:      &input.ProjectID,
		Type:           chainType(input.Chain),
		Payload:        encoded,
		IdempotencyKey: key,
		CorrelationID:  logging.CorrelationID(ctx),
		EnqueuedBy:     enqueuedBy,
		MaxAttempts:    int16(maxAttempts),
	})
	if err != nil {
		return chainCreation{}, fmt.Errorf("create chain %s: %w", input.Chain, err)
	}

	return chainCreation{
		job: Job{
			ID:             row.ID,
			ProjectID:      row.ProjectID,
			Type:           row.Type,
			Status:         Status(row.Status),
			Progress:       int(row.Progress),
			Attempts:       int(row.Attempts),
			MaxAttempts:    int(row.MaxAttempts),
			ParentJobID:    row.ParentJobID,
			Payload:        row.Payload,
			IdempotencyKey: row.IdempotencyKey,
			CorrelationID:  row.CorrelationID,
			EnqueuedBy:     row.EnqueuedBy,
			QueuedAt:       row.QueuedAt,
		},
		existed: row.Existed,
	}, nil
}

// chainType is the type recorded on a parent row. It names the chain so a list of
// jobs reads as a list of submissions rather than of stages.
func chainType(chain Chain) string { return TypeChainParent + "." + string(chain) }
