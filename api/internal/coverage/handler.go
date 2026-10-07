package coverage

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Queue pushes the measurement and generation jobs.
type Queue interface {
	SubmitCoverage(ctx context.Context, projectID uuid.UUID, ref string, actor uuid.UUID) (uuid.UUID, error)
	SubmitUnitTests(
		ctx context.Context,
		projectID uuid.UUID,
		ref string,
		targets []string,
		actor uuid.UUID,
	) (uuid.UUID, error)
}

// Handler implements the coverage slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	queue    Queue
}

func NewHandler(service *Service, projectsService Projects, queue Queue) *Handler {
	return &Handler{service: service, projects: projectsService, queue: queue}
}

// GetCodeCoverage returns the newest measurement.
//
// A 404 with a reason when nothing has been measured, never a zero. Zero would be a
// claim about code this platform does not have, and it is the number somebody would
// screenshot (BE-6.7.3).
func (h *Handler) GetCodeCoverage(
	ctx context.Context,
	request api.GetCodeCoverageRequestObject,
) (api.GetCodeCoverageResponseObject, error) {
	measurement, found, err := h.service.Latest(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.NoCoverageMeasured(
			"No code coverage has been measured for this project. " +
				"Connect a repository and run a measurement.")
	}

	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	files, err := h.service.Files(ctx, measurement.ID, limit)
	if err != nil {
		return nil, err
	}

	return api.GetCodeCoverage200JSONResponse(toAPI(measurement, files)), nil
}

func (h *Handler) MeasureCoverage(
	ctx context.Context,
	request api.MeasureCoverageRequestObject,
) (api.MeasureCoverageResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	ref := ""
	if request.Body != nil && request.Body.Ref != nil {
		ref = *request.Body.Ref
	}

	jobID, err := h.queue.SubmitCoverage(ctx, request.ProjectID, ref, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.MeasureCoverage202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

func (h *Handler) GenerateUnitTests(
	ctx context.Context,
	request api.GenerateUnitTestsRequestObject,
) (api.GenerateUnitTestsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	ref := ""
	var targets []string
	if request.Body != nil {
		if request.Body.Ref != nil {
			ref = *request.Body.Ref
		}
		if request.Body.Targets != nil {
			targets = *request.Body.Targets
		}
	}

	jobID, err := h.queue.SubmitUnitTests(ctx, request.ProjectID, ref, targets, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.GenerateUnitTests202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

func toAPI(measurement Measurement, files []File) api.CodeCoverage {
	body := api.CodeCoverage{
		Id:              measurement.ID,
		ProjectId:       measurement.ProjectID,
		Commit:          &measurement.Commit,
		Tool:            measurement.Tool,
		Command:         measurement.Command,
		LinesTotal:      measurement.LinesTotal,
		LinesCovered:    measurement.LinesCovered,
		BranchesTotal:   &measurement.BranchesTotal,
		BranchesCovered: &measurement.BranchesCovered,
		MeasuredAt:      measurement.CreatedAt,
	}

	// Null rather than zero for an unmeasurable rate, all the way out to the client:
	// "the tool does not measure branches" and "no branch is covered" are different
	// facts and a chart drawn from the wrong one is wrong.
	if rate := measurement.LineRate(); rate >= 0 {
		body.LineRate = &rate
	}
	if rate := measurement.BranchRate(); rate >= 0 {
		body.BranchRate = &rate
	}

	rendered := make([]api.FileCoverage, 0, len(files))
	for _, file := range files {
		entry := api.FileCoverage{
			Path:            file.Path,
			LinesTotal:      file.LinesTotal,
			LinesCovered:    file.LinesCovered,
			BranchesTotal:   &file.BranchesTotal,
			BranchesCovered: &file.BranchesCovered,
		}
		if rate := file.LineRate(); rate >= 0 {
			entry.LineRate = &rate
		}
		rendered = append(rendered, entry)
	}
	body.Files = &rendered

	return body
}
