package security

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
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
}

// Targets resolves and allowlist-checks the project's target.
type Targets interface {
	Check(ctx context.Context, projectID uuid.UUID) (targets.Target, error)
	CheckURL(ctx context.Context, projectID uuid.UUID, raw string) (targets.Target, error)
}

// RunLookup authorises a findings read by the run's project.
type RunLookup interface {
	ProjectFor(ctx context.Context, runID uuid.UUID) (uuid.UUID, error)
}

// Queue pushes the scan.
type Queue interface {
	SubmitScan(ctx context.Context, projectID uuid.UUID, payload ScanPayload) (uuid.UUID, error)
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Handler implements the security slice of the generated server interface.
type Handler struct {
	projects Projects
	targets  Targets
	guard    *riskcontrol.Guard
	queue    Queue
	findings *Service
	runs     RunLookup
	recorder Recorder
}

func NewHandler(
	projectsService Projects,
	targetsService Targets,
	guard *riskcontrol.Guard,
	queue Queue,
	findings *Service,
	runs RunLookup,
	recorder Recorder,
) *Handler {
	return &Handler{
		projects: projectsService,
		targets:  targetsService,
		guard:    guard,
		queue:    queue,
		findings: findings,
		runs:     runs,
		recorder: recorder,
	}
}

// StartSecurityScan gates the scan, then queues it.
//
// Same order as a performance run: resolve and allowlist-check the target first, then
// the risk guard checks the kind is enabled and the host is confirmed, then queue. A
// refused scan never became a job (BE-9.5).
func (h *Handler) StartSecurityScan(
	ctx context.Context,
	request api.StartSecurityScanRequestObject,
) (api.StartSecurityScanResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	targetURL := ""
	var categories []string
	confirmation := ""
	if request.Body != nil {
		if request.Body.TargetUrl != nil {
			targetURL = strings.TrimSpace(*request.Body.TargetUrl)
		}
		if request.Body.ConfirmHost != nil {
			confirmation = *request.Body.ConfirmHost
		}
		if request.Body.Categories != nil {
			for _, category := range *request.Body.Categories {
				if ValidCategory(string(category)) {
					categories = append(categories, string(category))
				}
			}
		}
	}

	checked, err := h.resolveTarget(ctx, request.ProjectID, targetURL)
	if err != nil {
		return nil, err
	}

	if err := h.guard.Authorize(ctx, request.ProjectID, riskcontrol.KindSecurity,
		checked.Host, confirmation, actor); err != nil {
		return nil, err
	}

	jobID, err := h.queue.SubmitScan(ctx, request.ProjectID, ScanPayload{
		ProjectID:  request.ProjectID,
		TargetURL:  targetURL,
		Categories: categories,
		ActorID:    actor.UserID,
		RequestID:  uuid.New(),
	})
	if err != nil {
		return nil, err
	}

	project := request.ProjectID
	h.recorder.Record(ctx, audit.Entry{
		ActorID:    &actor.UserID,
		ActorEmail: actor.Email,
		Action:     audit.ActionSecurityScan,
		Subject:    checked.Host,
		ProjectID:  &project,
		Detail:     map[string]any{"categories": categories, "jobId": jobID.String()},
	})

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.StartSecurityScan202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// ListRunFindings returns a scan's findings.
func (h *Handler) ListRunFindings(
	ctx context.Context,
	request api.ListRunFindingsRequestObject,
) (api.ListRunFindingsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	projectID, err := h.runs.ProjectFor(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureMember(ctx, actor, projectID); err != nil {
		return nil, apierr.SecurityScanNotFound()
	}

	findings, err := h.findings.ForRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	items := make([]api.SecurityFinding, 0, len(findings))
	for _, finding := range findings {
		items = append(items, api.SecurityFinding{
			Id:           finding.ID,
			RunResultId:  finding.RunResultID,
			PayloadId:    finding.PayloadID,
			Category:     finding.Category,
			Endpoint:     finding.Endpoint,
			Parameter:    optional(finding.Parameter),
			Severity:     api.SecurityFindingSeverity(finding.Severity),
			Evidence:     optional(finding.Evidence),
			Reproduction: optional(finding.Reproduction),
		})
	}

	return api.ListRunFindings200JSONResponse(api.SecurityFindingList{Items: items}), nil
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

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
