package perf

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/riskcontrol"
	"github.com/hyscaler/qavia/api/internal/targets"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Targets resolves and allowlist-checks the project's target, returning the host the
// confirmation is compared against.
type Targets interface {
	Check(ctx context.Context, projectID uuid.UUID) (targets.Target, error)
	CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (targets.Target, error)
}

// Queue pushes the performance run.
type Queue interface {
	SubmitPerf(ctx context.Context, projectID uuid.UUID, payload Payload) (uuid.UUID, error)
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// SettingsReader reads the concurrency ceiling.
type SettingsReader interface {
	MaxVirtualUsers(ctx context.Context, projectID uuid.UUID) (int, error)
}

// Handler implements the performance slice of the generated server interface.
type Handler struct {
	projects Projects
	targets  Targets
	guard    *riskcontrol.Guard
	queue    Queue
	settings SettingsReader
	recorder Recorder
}

func NewHandler(
	projectsService Projects,
	targetsService Targets,
	guard *riskcontrol.Guard,
	queue Queue,
	settingsReader SettingsReader,
	recorder Recorder,
) *Handler {
	return &Handler{
		projects: projectsService,
		targets:  targetsService,
		guard:    guard,
		queue:    queue,
		settings: settingsReader,
		recorder: recorder,
	}
}

// StartPerformanceTest gates the run, then queues it.
//
// The order is the control. The target is resolved and allowlist-checked first — which
// is the reject-before-traffic gate and also the host the confirmation is measured
// against — then the risk guard checks the kind is enabled and the host is confirmed.
// Only then is anything queued, so a refused run never became a job (BE-9.5).
func (h *Handler) StartPerformanceTest(
	ctx context.Context,
	request api.StartPerformanceTestRequestObject,
) (api.StartPerformanceTestResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	targetURL := ""
	if request.Body.TargetUrl != nil {
		targetURL = strings.TrimSpace(*request.Body.TargetUrl)
	}

	checked, err := h.resolveTarget(ctx, request.ProjectID, targetURL)
	if err != nil {
		return nil, err
	}

	profile := Profile{
		VirtualUsers:  request.Body.Profile.VirtualUsers,
		MaxErrorRate:  0,
		P95TargetMs:   0,
		RampUpSeconds: 0,
		HoldSeconds:   request.Body.Profile.HoldSeconds,
	}
	if request.Body.Profile.RampUpSeconds != nil {
		profile.RampUpSeconds = *request.Body.Profile.RampUpSeconds
	}
	if request.Body.Profile.P95TargetMs != nil {
		profile.P95TargetMs = *request.Body.Profile.P95TargetMs
	}
	if request.Body.Profile.MaxErrorRate != nil {
		profile.MaxErrorRate = *request.Body.Profile.MaxErrorRate
	}
	profile = profile.WithDefaults()

	maxUsers, err := h.settings.MaxVirtualUsers(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if err := profile.Validate(maxUsers); err != nil {
		return nil, apierr.Validation(err.Error(), map[string]any{"field": "profile"})
	}

	confirmation := ""
	if request.Body.ConfirmHost != nil {
		confirmation = *request.Body.ConfirmHost
	}
	if err := h.guard.Authorize(ctx, request.ProjectID, riskcontrol.KindPerformance,
		checked.Host, confirmation, actor); err != nil {
		return nil, err
	}

	jobID, err := h.queue.SubmitPerf(ctx, request.ProjectID, Payload{
		ProjectID: request.ProjectID,
		TargetURL: targetURL,
		Profile:   profile,
		ActorID:   actor.UserID,
		RequestID: uuid.New(),
	})
	if err != nil {
		return nil, err
	}

	project := request.ProjectID
	h.recorder.Record(ctx, audit.Entry{
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Action:     audit.ActionPerformanceRun,
		Subject:    checked.Host,
		ProjectID:  &project,
		Detail: map[string]any{
			"virtualUsers": profile.VirtualUsers,
			"holdSeconds":  profile.HoldSeconds,
			"jobId":        jobID.String(),
		},
	})

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.StartPerformanceTest202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

func (h *Handler) resolveTarget(
	ctx context.Context,
	projectID uuid.UUID,
	targetURL string,
) (targets.Target, error) {
	if targetURL != "" {
		return h.targets.CheckURL(ctx, projectID, targetURL)
	}
	return h.targets.Check(ctx, projectID)
}
