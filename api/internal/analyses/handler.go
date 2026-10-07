package analyses

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/runtrigger"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service the routes outside a project path
// need, so authorisation stays in one layer (backend-standards.md 11).
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Chains starts the analyse pipeline for a run whose failures nobody asked about yet.
type Chains interface {
	SubmitTriggered(ctx context.Context, req runtrigger.Request) (uuid.UUID, error)
}

// Promoter turns an analysed failure into a defect. Declared as one method so this
// package does not depend on the defect tracker's shape.
type Promoter interface {
	PromoteFromResult(
		ctx context.Context,
		request api.PromoteResultRequestObject,
		actor httpx.Principal,
	) (api.PromoteResultResponseObject, error)
}

// Handler implements the analysis slice of the generated server interface.
type Handler struct {
	service  *Service
	runs     Runs
	projects Projects
	chains   Chains
	promoter Promoter
}

func NewHandler(
	service *Service,
	runs Runs,
	projectsService Projects,
	chains Chains,
	promoter Promoter,
) *Handler {
	return &Handler{
		service:  service,
		runs:     runs,
		projects: projectsService,
		chains:   chains,
		promoter: promoter,
	}
}

func (h *Handler) ListRunAnalyses(
	ctx context.Context,
	request api.ListRunAnalysesRequestObject,
) (api.ListRunAnalysesResponseObject, error) {
	if err := h.authorizeRun(ctx, request.RunID); err != nil {
		return nil, err
	}

	found, err := h.service.ForRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	body := api.AnalysisList{Items: make([]api.Analysis, 0, len(found))}
	for _, analysis := range found {
		body.Items = append(body.Items, toAPI(analysis))
	}
	return api.ListRunAnalyses200JSONResponse(body), nil
}

// AnalyseRun queues the analysis for a finished run.
//
// It exists for the two cases automatic analysis does not cover: an installation that
// turned it off to control spend, and a prompt change worth re-running against
// failures that were already explained.
func (h *Handler) AnalyseRun(
	ctx context.Context,
	request api.AnalyseRunRequestObject,
) (api.AnalyseRunResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	projectID, err := h.runs.ProjectFor(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureMember(ctx, actor, projectID); err != nil {
		return nil, err
	}

	failures, err := h.service.FailingResults(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if len(failures) == 0 {
		return nil, apierr.Conflict(
			"This run produced no failures, so there is nothing to explain.")
	}

	runID := request.RunID
	actorID := actor.UserID
	jobID, err := h.chains.SubmitTriggered(ctx, runtrigger.Request{
		ProjectID: projectID,
		Chain:     string(jobs.ChainAnalyse),
		Source:    runtrigger.KindManual,
		ActorID:   &actorID,
		RunID:     &runID,
	})
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.AnalyseRun202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

func (h *Handler) GetResultAnalysis(
	ctx context.Context,
	request api.GetResultAnalysisRequestObject,
) (api.GetResultAnalysisResponseObject, error) {
	result, err := h.service.Result(ctx, request.ResultID)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeRun(ctx, result.RunID); err != nil {
		return nil, err
	}

	analysis, found, err := h.service.Latest(ctx, request.ResultID)
	if err != nil {
		return nil, err
	}
	if !found {
		// Not an error in the platform: most failures have not been explained yet, and
		// a 404 is what tells the UI to offer the button rather than the panel.
		return nil, apierr.AnalysisNotFound()
	}

	return api.GetResultAnalysis200JSONResponse(toAPI(analysis)), nil
}

// PromoteResult is delegated, because filing a defect belongs to the defect tracker
// and the route belongs to the failure. The handler here is the seam.
func (h *Handler) PromoteResult(
	ctx context.Context,
	request api.PromoteResultRequestObject,
) (api.PromoteResultResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	result, err := h.service.Result(ctx, request.ResultID)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeRun(ctx, result.RunID); err != nil {
		return nil, err
	}

	return h.promoter.PromoteFromResult(ctx, request, actor)
}

func (h *Handler) SetAnalysisFeedback(
	ctx context.Context,
	request api.SetAnalysisFeedbackRequestObject,
) (api.SetAnalysisFeedbackResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.authorizeAnalysis(ctx, request.AnalysisID, actor); err != nil {
		return nil, err
	}

	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	if err := h.service.RecordFeedback(
		ctx, request.AnalysisID, actor.UserID, request.Body.Helpful, note); err != nil {
		return nil, err
	}
	return api.SetAnalysisFeedback204Response{}, nil
}

func (h *Handler) ClearAnalysisFeedback(
	ctx context.Context,
	request api.ClearAnalysisFeedbackRequestObject,
) (api.ClearAnalysisFeedbackResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.authorizeAnalysis(ctx, request.AnalysisID, actor); err != nil {
		return nil, err
	}
	if err := h.service.ClearFeedback(ctx, request.AnalysisID, actor.UserID); err != nil {
		return nil, err
	}
	return api.ClearAnalysisFeedback204Response{}, nil
}

func (h *Handler) GetFeedbackByPromptVersion(
	ctx context.Context,
	_ api.GetFeedbackByPromptVersionRequestObject,
) (api.GetFeedbackByPromptVersionResponseObject, error) {
	scores, err := h.service.ByPromptVersion(ctx)
	if err != nil {
		return nil, err
	}

	body := api.PromptFeedbackList{Items: make([]api.PromptFeedback, 0, len(scores))}
	for _, score := range scores {
		body.Items = append(body.Items, api.PromptFeedback{
			PromptVersion: score.PromptVersion,
			Helpful:       score.Helpful,
			Unhelpful:     score.Unhelpful,
		})
	}
	return api.GetFeedbackByPromptVersion200JSONResponse(body), nil
}

// authorizeRun checks the caller is a member of the run's project.
//
// The analysis routes hang off a run or a result rather than a project, so the
// membership middleware has not run for them: the check belongs here rather than
// being scattered through each handler body.
func (h *Handler) authorizeRun(ctx context.Context, runID uuid.UUID) error {
	actor := httpx.MustCurrentUser(ctx)

	projectID, err := h.runs.ProjectFor(ctx, runID)
	if err != nil {
		return err
	}
	if err := h.projects.EnsureMember(ctx, actor, projectID); err != nil {
		// Reported as not found, so a caller cannot enumerate other projects' runs.
		return apierr.RunNotFound()
	}
	return nil
}

func (h *Handler) authorizeAnalysis(
	ctx context.Context,
	analysisID uuid.UUID,
	actor httpx.Principal,
) error {
	analysis, err := h.service.Get(ctx, analysisID)
	if err != nil {
		return err
	}

	result, err := h.service.Result(ctx, analysis.RunResultID)
	if err != nil {
		return err
	}

	projectID, err := h.runs.ProjectFor(ctx, result.RunID)
	if err != nil {
		return err
	}
	if err := h.projects.EnsureMember(ctx, actor, projectID); err != nil {
		return apierr.AnalysisNotFound()
	}
	return nil
}

func toAPI(analysis Analysis) api.Analysis {
	body := api.Analysis{
		Id:             analysis.ID,
		RunResultId:    analysis.RunResultID,
		Reason:         analysis.Reason,
		RootCause:      analysis.RootCause,
		SuggestedFix:   &analysis.SuggestedFix,
		RelatedCommit:  &analysis.RelatedCommit,
		StabilityBasis: &analysis.StabilityBasis,
		PromptVersion:  &analysis.PromptVersion,
		ModelName:      &analysis.ModelName,
		CreatedAt:      analysis.CreatedAt,
		Evidence:       make([]api.EvidenceReference, 0, len(analysis.Evidence)),
	}

	for _, reference := range analysis.Evidence {
		body.Evidence = append(body.Evidence, toAPIReference(reference))
	}

	if analysis.Stability != nil {
		// Rendered as a number because a client charts it. The column is numeric, so the
		// value here is exact to three decimals rather than whatever a float parse
		// produced along the way.
		score, _ := analysis.Stability.Float64()
		body.StabilityScore = &score
	}

	return body
}

func toAPIReference(reference Reference) api.EvidenceReference {
	body := api.EvidenceReference{
		Kind:   api.EvidenceKind(reference.Kind),
		Detail: reference.Detail,
	}
	if reference.File != "" {
		body.File = &reference.File
	}
	if reference.FromLine != 0 {
		body.FromLine = &reference.FromLine
	}
	if reference.ToLine != 0 {
		body.ToLine = &reference.ToLine
	}
	if reference.Path != "" {
		body.Path = &reference.Path
	}
	if reference.Quote != "" {
		body.Quote = &reference.Quote
	}
	return body
}
