package mocks

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Queue pushes the lifecycle jobs. A mock needs a container, and this process
// deliberately has none: the same split as running a suite (BE-4.2).
type Queue interface {
	SubmitMockStart(ctx context.Context, projectID uuid.UUID, payload StartPayload) (uuid.UUID, error)
	SubmitMockStop(ctx context.Context, projectID uuid.UUID, payload StopPayload) (uuid.UUID, error)
}

// Handler implements the mock-server slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	queue    Queue
	recorder Recorder
}

func NewHandler(
	service *Service,
	projectsService Projects,
	queue Queue,
	recorder Recorder,
) *Handler {
	return &Handler{
		service:  service,
		projects: projectsService,
		queue:    queue,
		recorder: recorder,
	}
}

// GetMockServer reports the mock's status.
func (h *Handler) GetMockServer(
	ctx context.Context,
	request api.GetMockServerRequestObject,
) (api.GetMockServerResponseObject, error) {
	mock, err := h.service.Get(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	return api.GetMockServer200JSONResponse(toAPI(mock)), nil
}

// StartMockServer brings a mock up, replacing whatever was running.
func (h *Handler) StartMockServer(
	ctx context.Context,
	request api.StartMockServerRequestObject,
) (api.StartMockServerResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	input := StartInput{ProjectID: request.ProjectID, Actor: actor}
	if request.Body != nil {
		if request.Body.Samples != nil {
			input.Samples = *request.Body.Samples
		}
		if request.Body.Seed != nil {
			input.Seed = uint64(*request.Body.Seed) //nolint:gosec // A reproducibility handle.
		}
		if request.Body.Faults != nil {
			input.Faults = faultsOf(*request.Body.Faults)
		}
	}

	// Validated here rather than in the worker, so a caller who meant 30 and typed 30
	// instead of 0.3 is told at once instead of by a failed job.
	if err := input.Faults.Validate(); err != nil {
		return nil, apierr.Validation(err.Error(), map[string]any{"field": "faults"})
	}

	jobID, err := h.queue.SubmitMockStart(ctx, request.ProjectID, StartPayload{
		ProjectID: request.ProjectID,
		Faults:    input.Faults,
		Samples:   input.Samples,
		Seed:      input.Seed,
		ActorID:   actor.UserID,
		RequestID: uuid.New(),
	})
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionMockStarted,
		Subject:   request.ProjectID.String(),
		ProjectID: &request.ProjectID,

		// The fault settings are audited with the request, because a mock injecting
		// failures is a thing somebody will later ask about when a client's test run
		// looked wrong.
		Detail: map[string]any{
			"failure_rate": input.Faults.FailureRate,
			"timeout_rate": input.Faults.TimeoutRate,
			"delay_ms":     input.Faults.DelayMs,
			"job_id":       jobID.String(),
		},
	})

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.StartMockServer202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// StopMockServer takes the mock down.
func (h *Handler) StopMockServer(
	ctx context.Context,
	request api.StopMockServerRequestObject,
) (api.StopMockServerResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	// Read first, so stopping something that never existed is a 404 rather than a job
	// that will find nothing.
	if _, err := h.service.Get(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	jobID, err := h.queue.SubmitMockStop(ctx, request.ProjectID, StopPayload{
		ProjectID: request.ProjectID,
		ActorID:   actor.UserID,
		RequestID: uuid.New(),
	})
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionMockStopped,
		Subject:   request.ProjectID.String(),
		ProjectID: &request.ProjectID,
		Detail:    map[string]any{"job_id": jobID.String()},
	})

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.StopMockServer202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// faultsOf reads the request's fault settings. Validated by the service, which owns the
// bounds: a delay of an hour and a failure rate of 30 are both callers meaning something
// else.
func faultsOf(body api.MockFaults) Faults {
	faults := Faults{}
	if body.DelayMs != nil {
		faults.DelayMs = *body.DelayMs
	}
	if body.FailureRate != nil {
		faults.FailureRate = *body.FailureRate
	}
	if body.TimeoutRate != nil {
		faults.TimeoutRate = *body.TimeoutRate
	}
	if body.StatusCodes != nil {
		faults.StatusCodes = *body.StatusCodes
	}
	if body.Seed != nil {
		faults.Seed = uint64(*body.Seed) //nolint:gosec // A reproducibility handle.
	}
	return faults
}

func toAPI(mock Mock) api.MockServer {
	seed := int64(mock.Faults.Seed) //nolint:gosec // Reported so a pattern can be reproduced.
	uptime := int(mock.Uptime().Seconds())

	body := api.MockServer{
		ProjectId:  mock.ProjectID,
		Status:     api.MockServerStatus(mock.Status),
		RouteCount: mock.RouteCount,

		Url:           optional(mock.URL),
		Image:         optional(mock.Image),
		Error:         optional(mock.Error),
		UptimeSeconds: &uptime,

		Faults: &api.MockFaults{
			DelayMs:     &mock.Faults.DelayMs,
			FailureRate: &mock.Faults.FailureRate,
			TimeoutRate: &mock.Faults.TimeoutRate,
			Seed:        &seed,
		},
	}
	if len(mock.Faults.StatusCodes) > 0 {
		body.Faults.StatusCodes = &mock.Faults.StatusCodes
	}
	body.StartedAt = mock.StartedAt
	body.StoppedAt = mock.StoppedAt
	return body
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
