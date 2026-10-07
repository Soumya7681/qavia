package mocks

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
)

// The mock lifecycle stages (BE-8.6.4).
//
// Start and stop are jobs for the same reason execution is: a mock needs a container,
// and the API process deliberately has none. What that buys beyond consistency is a
// record — a mock that failed to start has a job with events explaining why, rather than
// a button that did nothing.

// Job types the pipeline declares.
const (
	TypeStart = jobs.TypeMockStart
	TypeStop  = jobs.TypeMockStop
)

// StartPayload names the mock to start.
type StartPayload struct {
	ProjectID uuid.UUID `json:"projectId"`

	Faults  Faults `json:"faults"`
	Samples int    `json:"samples,omitempty"`
	Seed    uint64 `json:"seed,omitempty"`

	// ActorID is who asked, carried so the row records it and the allowlist write is
	// attributed to a person rather than to the platform.
	ActorID uuid.UUID `json:"actorId"`

	// RequestID is the per-request nonce the idempotency key is built from: restarting
	// a mock is work somebody asks for, and keying on the project would make the second
	// request find the first one's row and push nothing.
	RequestID uuid.UUID `json:"requestId"`
}

// StopPayload names the mock to stop.
type StopPayload struct {
	ProjectID uuid.UUID `json:"projectId"`
	ActorID   uuid.UUID `json:"actorId"`
	RequestID uuid.UUID `json:"requestId"`
}

// StartIdempotencyKey is exported so the API can declare the type as enqueue-only
// without building a handler it cannot run.
func StartIdempotencyKey(payload StartPayload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("mock.start:%s", payload.ProjectID)
	}
	return fmt.Sprintf("mock.start:%s", payload.RequestID)
}

// StopIdempotencyKey is the same for a stop.
func StopIdempotencyKey(payload StopPayload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("mock.stop:%s", payload.ProjectID)
	}
	return fmt.Sprintf("mock.stop:%s", payload.RequestID)
}

// StartHandler brings a project's mock up.
type StartHandler struct {
	service *Service
}

func NewStartHandler(service *Service) *StartHandler { return &StartHandler{service: service} }

func (h *StartHandler) Type() string { return TypeStart }

func (h *StartHandler) IdempotencyKey(payload StartPayload) string {
	return StartIdempotencyKey(payload)
}

func (h *StartHandler) Handle(ctx context.Context, payload StartPayload, jc jobs.JobContext) error {
	jc.Event("Starting the mock server")

	mock, err := h.service.Start(ctx, StartInput{
		ProjectID: payload.ProjectID,
		Actor:     httpx.Principal{UserID: payload.ActorID},
		Faults:    payload.Faults,
		Samples:   payload.Samples,
		Seed:      payload.Seed,
	})
	if err != nil {
		// Returned, so the job records the reason: the row already carries it too, and a
		// failed start that looks like a success is the one outcome nobody can debug.
		return err
	}

	jc.Event("The mock is serving %d route(s) at %s", mock.RouteCount, mock.URL)
	if mock.Faults.FailureRate > 0 || mock.Faults.TimeoutRate > 0 || mock.Faults.DelayMs > 0 {
		// Said plainly. A mock quietly failing a third of requests is the most confusing
		// thing this platform could leave running.
		jc.Event("Fault injection is on: %.0f%% failures, %.0f%% timeouts, %dms delay",
			mock.Faults.FailureRate*100, mock.Faults.TimeoutRate*100, mock.Faults.DelayMs)
	}
	jc.Progress(100)
	return nil
}

// StopHandler takes a project's mock down.
type StopHandler struct {
	service *Service
}

func NewStopHandler(service *Service) *StopHandler { return &StopHandler{service: service} }

func (h *StopHandler) Type() string { return TypeStop }

func (h *StopHandler) IdempotencyKey(payload StopPayload) string {
	return StopIdempotencyKey(payload)
}

func (h *StopHandler) Handle(ctx context.Context, payload StopPayload, jc jobs.JobContext) error {
	if _, err := h.service.Stop(ctx, payload.ProjectID); err != nil {
		return err
	}

	jc.Event("The mock server is stopped")
	jc.Progress(100)
	return nil
}

// Reconcile corrects rows whose container is gone.
//
// A loop rather than a check on read, because the read happens in the API process and
// the containers are here: a status panel is only honest if something on this host is
// noticing when a mock dies. Runs alongside the run sweeper, on the same principle — a
// defer cannot run in a process that was killed (BE-4.12).
func (s *Service) Reconcile(ctx context.Context, every time.Duration) {
	if s.containers == nil {
		return
	}
	if every <= 0 {
		every = time.Minute
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOnce(ctx)
		}
	}
}

func (s *Service) reconcileOnce(ctx context.Context) {
	rows, err := s.db.Queries().ListRunningMocks(ctx)
	if err != nil {
		return
	}

	for _, row := range rows {
		if row.ContainerID == "" {
			continue
		}

		status, err := s.containers.ServiceStatus(ctx, row.ContainerID)
		if err != nil || status.Running {
			// A runtime that could not be asked is not a mock that stopped, so nothing is
			// rewritten on a failure to look.
			continue
		}

		// Get performs the correction, and going through it keeps that logic in one
		// place rather than duplicating the update here.
		if _, err := s.Get(ctx, row.ProjectID); err != nil {
			continue
		}
	}
}
