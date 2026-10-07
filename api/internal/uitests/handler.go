package uitests

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Queue pushes the discovery and generation jobs.
type Queue interface {
	SubmitDiscovery(ctx context.Context, projectID uuid.UUID, target string, actor uuid.UUID) (uuid.UUID, error)
	SubmitUITests(ctx context.Context, projectID uuid.UUID, flowID *uuid.UUID, flows []string, actor uuid.UUID) (uuid.UUID, error)
}

// Handler implements the UI-testing slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	queue    Queue
	objects  objectstore.Store
}

func NewHandler(
	service *Service,
	projectsService Projects,
	queue Queue,
	objects objectstore.Store,
) *Handler {
	return &Handler{service: service, projects: projectsService, queue: queue, objects: objects}
}

// DiscoverUIFlows queues a discovery.
//
// Queued rather than run inline, because a browser walking forty pages is minutes of
// work and a request nobody holds open. The target is checked by the job rather than
// here: the check needs DNS resolution and the answer has to be the one the worker
// will actually dial, not one this process resolved a minute earlier (BE-4.7).
func (h *Handler) DiscoverUIFlows(
	ctx context.Context,
	request api.DiscoverUIFlowsRequestObject,
) (api.DiscoverUIFlowsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	target := ""
	if request.Body != nil && request.Body.TargetUrl != nil {
		target = strings.TrimSpace(*request.Body.TargetUrl)
	}
	if target != "" {
		// Shape only. Whether it is allowed is the target service's decision, made on
		// the worker with the addresses it resolved: a URL that parses is not a URL a
		// run may reach.
		if parsed, err := url.Parse(target); err != nil || parsed.Host == "" {
			return nil, apierr.Validation(
				"That target is not a URL the platform can use. Expected something like https://staging.example.com.",
				map[string]any{"field": "targetUrl"})
		}
	}

	jobID, err := h.queue.SubmitDiscovery(ctx, request.ProjectID, target, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.DiscoverUIFlows202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// GenerateUITests queues spec generation from a graph.
//
// The graph is checked here rather than in the job, because "this project has never
// run a discovery" is an answer worth giving before a job exists to fail: a queued job
// that fails immediately is a notification for something the request could have said.
func (h *Handler) GenerateUITests(
	ctx context.Context,
	request api.GenerateUITestsRequestObject,
) (api.GenerateUITestsResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	var (
		flowID *uuid.UUID
		flows  []string
	)
	if request.Body != nil {
		flowID = request.Body.FlowId
		if request.Body.Flows != nil {
			flows = *request.Body.Flows
		}
	}

	if flowID != nil {
		stored, err := h.service.Get(ctx, *flowID)
		if err != nil {
			return nil, err
		}
		if stored.ProjectID != request.ProjectID {
			// A graph belonging to another project is indistinguishable from one that does
			// not exist, which is the same rule every other cross-project read follows.
			return nil, apierr.FlowGraphNotFound()
		}
	} else if _, found, err := h.service.Latest(ctx, request.ProjectID); err != nil {
		return nil, err
	} else if !found {
		return nil, apierr.NoFlowGraph()
	}

	jobID, err := h.queue.SubmitUITests(ctx, request.ProjectID, flowID, flows, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.GenerateUITests202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// GetLatestUIFlowGraph returns the newest graph, or says there is none.
func (h *Handler) GetLatestUIFlowGraph(
	ctx context.Context,
	request api.GetLatestUIFlowGraphRequestObject,
) (api.GetLatestUIFlowGraphResponseObject, error) {
	stored, found, err := h.service.Latest(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		// A normal state rather than a failure: it is what tells a UI to offer the
		// discovery button rather than an empty panel.
		return nil, apierr.NoFlowGraph()
	}

	body, err := h.toAPI(ctx, stored)
	if err != nil {
		return nil, err
	}
	return api.GetLatestUIFlowGraph200JSONResponse(body), nil
}

// GetUIFlowGraph returns one graph.
func (h *Handler) GetUIFlowGraph(
	ctx context.Context,
	request api.GetUIFlowGraphRequestObject,
) (api.GetUIFlowGraphResponseObject, error) {
	stored, err := h.service.Get(ctx, request.FlowID)
	if err != nil {
		return nil, err
	}

	body, err := h.toAPI(ctx, stored)
	if err != nil {
		return nil, err
	}
	return api.GetUIFlowGraph200JSONResponse(body), nil
}

// ListUIFlows returns the discovery history.
func (h *Handler) ListUIFlows(
	ctx context.Context,
	request api.ListUIFlowsRequestObject,
) (api.ListUIFlowsResponseObject, error) {
	limit := int32(defaultPageSize)
	if request.Params.Limit != nil {
		// Clamped before the conversion rather than after: the contract caps it at 200,
		// and a value that came from somewhere else must not become a negative page size.
		asked := *request.Params.Limit
		if asked > 0 && asked <= maxPageSize {
			limit = int32(asked)
		}
	}

	summaries, err := h.service.List(ctx, request.ProjectID, limit)
	if err != nil {
		return nil, err
	}

	items := make([]api.UIFlowGraphSummary, 0, len(summaries))
	for _, summary := range summaries {
		items = append(items, api.UIFlowGraphSummary{
			Id:         summary.ID,
			ProjectId:  summary.ProjectID,
			Target:     summary.Target,
			AuthMode:   summary.AuthMode,
			PageCount:  summary.Pages,
			FlowCount:  summary.Flows,
			Steps:      summary.Steps,
			CutShort:   summary.CutShort,
			ReviewedAt: reviewedAt(summary.ReviewedAt),
			ModelName:  &summary.ModelName,
			CreatedAt:  summary.CreatedAt,
		})
	}

	return api.ListUIFlows200JSONResponse(api.UIFlowGraphList{Items: items}), nil
}

// ReviewUIFlowGraph records that somebody has read the graph.
func (h *Handler) ReviewUIFlowGraph(
	ctx context.Context,
	request api.ReviewUIFlowGraphRequestObject,
) (api.ReviewUIFlowGraphResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	stored, err := h.service.MarkReviewed(ctx, request.FlowID, actor.UserID)
	if err != nil {
		return nil, err
	}

	body, err := h.toAPI(ctx, stored)
	if err != nil {
		return nil, err
	}
	return api.ReviewUIFlowGraph200JSONResponse(body), nil
}

// toAPI renders a stored graph against today's contract.
//
// The document is decoded rather than passed through, so a graph written by an older
// version of the platform is mapped explicitly instead of leaking its own shape to a
// client (backend-standards.md 6).
func (h *Handler) toAPI(ctx context.Context, stored Stored) (api.UIFlowGraph, error) {
	graph, err := stored.Decode()
	if err != nil {
		return api.UIFlowGraph{}, err
	}

	body := api.UIFlowGraph{
		Id:         stored.ID,
		ProjectId:  stored.ProjectID,
		Target:     stored.Target,
		AuthMode:   stored.AuthMode,
		Summary:    &graph.Summary,
		Steps:      stored.Steps,
		CutShort:   stored.CutShort,
		PageCount:  &stored.Pages,
		FlowCount:  &stored.Flows,
		ReviewedAt: reviewedAt(stored.ReviewedAt),
		ModelName:  &stored.ModelName,
		CreatedAt:  stored.CreatedAt,
	}

	pages := make([]api.UIPage, 0, len(graph.Pages))
	for _, page := range graph.Pages {
		actions := make([]api.UIPageAction, 0, len(page.Actions))
		for _, action := range page.Actions {
			actions = append(actions, api.UIPageAction{
				Description: action.Description,
				Testid:      optional(action.TestID),
				Role:        optional(action.Role),
				Name:        optional(action.Name),
				Label:       optional(action.Label),
				LeadsTo:     optional(action.LeadsTo),
			})
		}
		pages = append(pages, api.UIPage{
			Path:         page.Path,
			Title:        optional(page.Title),
			Purpose:      optional(page.Purpose),
			RequiresAuth: page.RequiresAuth,
			Actions:      &actions,
		})
	}
	body.Pages = &pages

	flows := make([]api.UIFlow, 0, len(graph.Flows))
	for _, flow := range graph.Flows {
		steps := make([]api.UIFlowStep, 0, len(flow.Steps))
		for _, step := range flow.Steps {
			steps = append(steps, api.UIFlowStep{
				Action: step.Action,
				Target: optional(step.Target),
				Testid: optional(step.TestID),
				Role:   optional(step.Role),
				Name:   optional(step.Name),
				Label:  optional(step.Label),
				Value:  optional(step.Value),
				Expect: optional(step.Expect),
			})
		}
		flows = append(flows, api.UIFlow{
			Name:         flow.Name,
			Purpose:      optional(flow.Purpose),
			RequiresAuth: flow.RequiresAuth,
			Steps:        &steps,
		})
	}
	body.Flows = &flows

	trail := make([]api.UIDiscoveryStep, 0, len(graph.Trail))
	for _, step := range graph.Trail {
		refused := step.Refused
		trail = append(trail, api.UIDiscoveryStep{
			Action:  step.Action,
			Detail:  optional(step.Detail),
			Reason:  optional(step.Reason),
			Url:     optional(step.URL),
			Refused: &refused,
		})
	}
	body.Trail = &trail

	body.Unreachable = &graph.Unreachable
	body.Unknowns = &graph.Unknowns
	body.Artifacts = h.artifacts(ctx, stored.Artifacts)

	return body, nil
}

// artifacts turns stored keys into something a client can fetch.
//
// A time-limited URL where the driver can sign one, and nothing where it cannot: a
// key is not useful to a browser, and handing one out would say more about the
// platform's storage layout than a client needs to know.
func (h *Handler) artifacts(ctx context.Context, stored []Artifact) *[]api.UIArtifact {
	items := make([]api.UIArtifact, 0, len(stored))

	for _, artifact := range stored {
		item := api.UIArtifact{
			Name:        artifact.Name,
			Bytes:       artifact.Bytes,
			ContentType: optional(artifact.ContentType),
		}

		if h.objects != nil {
			signed, err := h.objects.SignedURL(ctx, artifact.Key, signedURLTTL)
			switch {
			case err == nil:
				item.Url = &signed
			case errors.Is(err, objectstore.ErrSignedURLUnsupported):
				// Expected on a driver that cannot sign. Not logged: it is a property of the
				// configured storage, not an incident.
			default:
				slog.WarnContext(ctx, "sign a discovery artifact URL",
					"key", artifact.Key, "error", err)
			}
		}

		items = append(items, item)
	}

	return &items
}

// signedURLTTL bounds a link to a recording. Long enough to start a slow download of
// a trace, short enough that a URL left in a browser's history is useless.
const signedURLTTL = 10 * time.Minute

// optional renders an empty string as an absent field, so a client can tell "not
// recorded" from "recorded as empty".
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func reviewedAt(at *time.Time) *time.Time { return at }
