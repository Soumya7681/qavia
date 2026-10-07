package generation

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	"github.com/hyscaler/qavia/api/internal/requirements"
	"github.com/hyscaler/qavia/api/internal/testcases"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs for the
// endpoints that are not under a project path parameter.
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Handler implements the generation slice of the generated server interface.
//
// The project-scoped routes are already checked by the membership middleware. The
// two that take a test case ID are not, so those resolve the owning project and
// check it here: authorisation stays in one layer either way
// (backend-standards.md 11).
type Handler struct {
	deps      Deps
	estimator *Estimator
	projects  Projects
	files     *testfiles.Service
	names     ProjectNames
}

func NewHandler(
	deps Deps,
	projectsService Projects,
	files *testfiles.Service,
	names ProjectNames,
) *Handler {
	return &Handler{
		deps:      deps,
		estimator: NewEstimator(deps),
		projects:  projectsService,
		files:     files,
		names:     names,
	}
}

func (h *Handler) ListRequirements(
	ctx context.Context,
	request api.ListRequirementsRequestObject,
) (api.ListRequirementsResponseObject, error) {
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)

	page, err := h.deps.Requirements.List(
		ctx, request.ProjectID, request.Params.ArtifactID, limit, cursor)
	if err != nil {
		return nil, err
	}

	body := api.RequirementPage{Items: make([]api.Requirement, 0, len(page.Items))}
	for _, item := range page.Items {
		body.Items = append(body.Items, toAPIRequirement(item))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListRequirements200JSONResponse(body), nil
}

func (h *Handler) ListEndpoints(
	ctx context.Context,
	request api.ListEndpointsRequestObject,
) (api.ListEndpointsResponseObject, error) {
	endpoints, err := h.deps.Ingest.Endpoints(ctx, request.ProjectID, request.Params.ArtifactID)
	if err != nil {
		return nil, err
	}

	body := api.EndpointList{Items: make([]api.EndpointSummary, 0, len(endpoints))}
	for _, endpoint := range endpoints {
		body.Items = append(body.Items, toAPIEndpoint(endpoint))
	}
	return api.ListEndpoints200JSONResponse(body), nil
}

func (h *Handler) GetRequirementCoverage(
	ctx context.Context,
	request api.GetRequirementCoverageRequestObject,
) (api.GetRequirementCoverageResponseObject, error) {
	coverage, err := h.deps.Requirements.Coverage(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	body := api.RequirementCoverage{
		Total:     coverage.Total,
		Covered:   coverage.Covered,
		Uncovered: make([]api.Requirement, 0, len(coverage.Uncovered)),
	}
	for _, item := range coverage.Uncovered {
		body.Uncovered = append(body.Uncovered, toAPIRequirement(item))
	}
	return api.GetRequirementCoverage200JSONResponse(body), nil
}

func (h *Handler) EstimateGeneration(
	ctx context.Context,
	request api.EstimateGenerationRequestObject,
) (api.EstimateGenerationResponseObject, error) {
	estimate, err := h.estimator.Estimate(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	prefix := estimate.PrefixTokens
	input := estimate.InputTokens
	cached := estimate.CachedTokens
	output := estimate.OutputTokens

	return api.EstimateGeneration200JSONResponse(api.GenerationEstimate{
		ProviderName:  estimate.ProviderName,
		ModelName:     estimate.ModelName,
		PromptCaching: api.GenerationEstimatePromptCaching(estimate.Caching),
		Endpoints:     estimate.Endpoints,
		Requirements:  estimate.Requirements,
		Calls:         estimate.Calls,
		PrefixTokens:  &prefix,
		InputTokens:   &input,
		CachedTokens:  &cached,
		OutputTokens:  &output,
		CostUsd:       estimate.Cost.StringFixed(4),
	}), nil
}

func (h *Handler) ListTestCases(
	ctx context.Context,
	request api.ListTestCasesRequestObject,
) (api.ListTestCasesResponseObject, error) {
	limit, cursor := pageParams(request.Params.Limit, request.Params.Cursor)

	filter := testcases.Filter{
		RequirementID: request.Params.RequirementID,
		Limit:         limit,
		Cursor:        cursor,
	}
	if request.Params.Category != nil {
		filter.Category = testcases.Category(*request.Params.Category)
	}
	if request.Params.Priority != nil {
		filter.Priority = testcases.Priority(*request.Params.Priority)
	}
	if request.Params.Status != nil {
		filter.Status = testcases.Status(*request.Params.Status)
	}
	if request.Params.Search != nil {
		filter.Search = *request.Params.Search
	}

	page, err := h.deps.TestCases.List(ctx, request.ProjectID, filter)
	if err != nil {
		return nil, err
	}

	body := api.TestCasePage{Items: make([]api.TestCase, 0, len(page.Items))}
	for _, item := range page.Items {
		body.Items = append(body.Items, toAPITestCase(item))
	}
	if page.NextCursor != "" {
		body.NextCursor.Set(page.NextCursor)
	}
	return api.ListTestCases200JSONResponse(body), nil
}

func (h *Handler) CreateTestCase(
	ctx context.Context,
	request api.CreateTestCaseRequestObject,
) (api.CreateTestCaseResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	input := testcases.CreateInput{
		ProjectID:     request.ProjectID,
		RequirementID: request.Body.RequirementId,
		CreatedBy:     &actor.UserID,
		Title:         request.Body.Title,
	}
	if request.Body.Preconditions != nil {
		input.Preconditions = *request.Body.Preconditions
	}
	if request.Body.Expected != nil {
		input.Expected = *request.Body.Expected
	}
	if request.Body.Priority != nil {
		input.Priority = testcases.Priority(*request.Body.Priority)
	}
	if request.Body.Category != nil {
		input.Category = testcases.Category(*request.Body.Category)
	}
	if request.Body.Endpoint != nil {
		input.Endpoint = *request.Body.Endpoint
	}
	if request.Body.Assertion != nil {
		input.Assertion = *request.Body.Assertion
	}
	if request.Body.Steps != nil {
		input.Steps = toSteps(*request.Body.Steps)
	}

	created, err := h.deps.TestCases.Create(ctx, input)
	if err != nil {
		return nil, err
	}
	return api.CreateTestCase201JSONResponse(toAPITestCase(created)), nil
}

func (h *Handler) SetTestCaseStatuses(
	ctx context.Context,
	request api.SetTestCaseStatusesRequestObject,
) (api.SetTestCaseStatusesResponseObject, error) {
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	updated, err := h.deps.TestCases.SetStatus(ctx,
		request.ProjectID, request.Body.Ids, testcases.Status(request.Body.Status))
	if err != nil {
		return nil, err
	}
	return api.SetTestCaseStatuses200JSONResponse{Updated: updated}, nil
}

func (h *Handler) GetTestCase(
	ctx context.Context,
	request api.GetTestCaseRequestObject,
) (api.GetTestCaseResponseObject, error) {
	testCase, err := h.authorizedCase(ctx, request.TestCaseID)
	if err != nil {
		return nil, err
	}
	return api.GetTestCase200JSONResponse(toAPITestCase(testCase)), nil
}

func (h *Handler) UpdateTestCase(
	ctx context.Context,
	request api.UpdateTestCaseRequestObject,
) (api.UpdateTestCaseResponseObject, error) {
	existing, err := h.authorizedCase(ctx, request.TestCaseID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureActive(ctx, existing.ProjectID); err != nil {
		return nil, err
	}

	input := testcases.EditInput{
		Title:         request.Body.Title,
		Preconditions: request.Body.Preconditions,
		Expected:      request.Body.Expected,
	}
	if request.Body.Priority != nil {
		priority := testcases.Priority(*request.Body.Priority)
		input.Priority = &priority
	}
	if request.Body.Category != nil {
		category := testcases.Category(*request.Body.Category)
		input.Category = &category
	}
	if request.Body.Status != nil {
		status := testcases.Status(*request.Body.Status)
		input.Status = &status
	}
	if request.Body.Steps != nil {
		steps := toSteps(*request.Body.Steps)
		input.Steps = &steps
	}

	updated, err := h.deps.TestCases.Update(ctx, request.TestCaseID, input)
	if err != nil {
		return nil, err
	}
	return api.UpdateTestCase200JSONResponse(toAPITestCase(updated)), nil
}

func (h *Handler) DeleteTestCase(
	ctx context.Context,
	request api.DeleteTestCaseRequestObject,
) (api.DeleteTestCaseResponseObject, error) {
	existing, err := h.authorizedCase(ctx, request.TestCaseID)
	if err != nil {
		return nil, err
	}
	if err := h.projects.EnsureActive(ctx, existing.ProjectID); err != nil {
		return nil, err
	}

	if err := h.deps.TestCases.Delete(ctx, request.TestCaseID); err != nil {
		return nil, err
	}
	return api.DeleteTestCase204Response{}, nil
}

// authorizedCase loads a case and checks the caller belongs to its project.
//
// The route carries no project ID, so the membership middleware cannot help: the
// owning project is resolved here and the same check applied, rather than trusting
// that a UUID is unguessable.
func (h *Handler) authorizedCase(ctx context.Context, id uuid.UUID) (testcases.TestCase, error) {
	actor := httpx.MustCurrentUser(ctx)

	testCase, err := h.deps.TestCases.Get(ctx, id)
	if err != nil {
		return testcases.TestCase{}, err
	}
	if err := h.projects.EnsureMember(ctx, actor, testCase.ProjectID); err != nil {
		return testcases.TestCase{}, err
	}
	return testCase, nil
}

func pageParams(limit *int, cursor *string) (int, string) {
	out := 0
	if limit != nil {
		out = *limit
	}
	value := ""
	if cursor != nil {
		value = *cursor
	}
	return out, value
}

func toSteps(steps []api.TestStep) []testcases.Step {
	out := make([]testcases.Step, 0, len(steps))
	for _, step := range steps {
		converted := testcases.Step{Action: step.Action}
		if step.Data != nil {
			converted.Data = *step.Data
		}
		if step.Expected != nil {
			converted.Expected = *step.Expected
		}
		out = append(out, converted)
	}
	return out
}

func toAPIRequirement(requirement requirements.Requirement) api.Requirement {
	generatedBy := requirement.GeneratedBy

	out := api.Requirement{
		Id:          requirement.ID,
		Kind:        api.RequirementKind(requirement.Kind),
		Title:       requirement.Title,
		Body:        requirement.Body,
		SourceRef:   requirement.SourceRef,
		GeneratedBy: &generatedBy,
		CreatedAt:   requirement.CreatedAt,
	}
	if requirement.EndpointID != nil {
		out.EndpointId.Set(*requirement.EndpointID)
	}
	if requirement.ArtifactID != nil {
		out.ArtifactId.Set(*requirement.ArtifactID)
	}
	return out
}

func toAPIEndpoint(endpoint ingest.Endpoint) api.EndpointSummary {
	operationID := endpoint.OperationID
	summary := endpoint.Summary
	description := endpoint.Description
	sourceRef := endpoint.SourceRef
	parameterCount := len(endpoint.Parameters)
	secured := len(endpoint.Security) > 0

	codes := make([]string, 0, len(endpoint.Responses))
	for _, response := range endpoint.Responses {
		codes = append(codes, response.Status)
	}

	return api.EndpointSummary{
		Method:         endpoint.Method,
		Path:           endpoint.Path,
		OperationId:    &operationID,
		Summary:        &summary,
		Description:    &description,
		ParameterCount: &parameterCount,
		ResponseCodes:  &codes,
		Secured:        &secured,
		SourceRef:      &sourceRef,
	}
}

func toAPITestCase(testCase testcases.TestCase) api.TestCase {
	preconditions := testCase.Preconditions
	expected := testCase.Expected
	generatedBy := testCase.GeneratedBy
	updatedAt := testCase.UpdatedAt

	out := api.TestCase{
		Id:            testCase.ID,
		ProjectId:     testCase.ProjectID,
		Title:         testCase.Title,
		Preconditions: &preconditions,
		Expected:      &expected,
		Priority:      api.TestPriority(testCase.Priority),
		Category:      api.TestCategory(testCase.Category),
		Status:        api.TestCaseStatus(testCase.Status),
		GeneratedBy:   &generatedBy,
		CreatedAt:     testCase.CreatedAt,
		UpdatedAt:     &updatedAt,
		Steps:         make([]api.TestStep, 0, len(testCase.Steps)),
	}

	for _, step := range testCase.Steps {
		data := step.Data
		stepExpected := step.Expected
		out.Steps = append(out.Steps, api.TestStep{
			Action: step.Action, Data: &data, Expected: &stepExpected,
		})
	}

	if testCase.RequirementID != nil {
		out.RequirementId.Set(*testCase.RequirementID)
	}
	if testCase.SupersededBy != nil {
		out.SupersededBy.Set(*testCase.SupersededBy)
	}
	return out
}
