package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureActive(ctx context.Context, projectID uuid.UUID) error
}

// Secrets stores the access token. The settings service, narrowed to the one write
// this package performs: a token is a secret like any other, and this package does
// not get its own encryption.
type Secrets interface {
	Write(ctx context.Context, projectID uuid.UUID, key, value string, actor httpx.Principal) error
	Clear(ctx context.Context, projectID uuid.UUID, key string, actor httpx.Principal) error
}

// Queue pushes the sync job.
type Queue interface {
	SubmitSync(ctx context.Context, projectID uuid.UUID, ref string, actor uuid.UUID) (uuid.UUID, error)
	SubmitComprehend(ctx context.Context, projectID uuid.UUID, ref string, actor uuid.UUID) (uuid.UUID, error)
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Handler implements the repository slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
	secrets  Secrets
	queue    Queue
	recorder Recorder
}

func NewHandler(
	service *Service,
	projectsService Projects,
	secrets Secrets,
	queue Queue,
	recorder Recorder,
) *Handler {
	return &Handler{
		service:  service,
		projects: projectsService,
		secrets:  secrets,
		queue:    queue,
		recorder: recorder,
	}
}

func (h *Handler) GetRepository(
	ctx context.Context,
	request api.GetRepositoryRequestObject,
) (api.GetRepositoryResponseObject, error) {
	connection, found, err := h.service.Get(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.NoRepositoryConnected()
	}

	// The override is applied on read, so the API and every consumer agree about what
	// the stack is (BE-6.3.2).
	stack, _, err := h.service.DetectedStack(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	connection.Detected = stack

	return api.GetRepository200JSONResponse(toAPI(connection)), nil
}

// ConnectRepository attaches a repository, storing the token separately.
//
// The token is written to the settings store before the connection row, so a
// connection that exists always has the credential it claims to have. The other
// order would leave a row saying "authenticated" with nothing behind it.
func (h *Handler) ConnectRepository(
	ctx context.Context,
	request api.ConnectRepositoryRequestObject,
) (api.ConnectRepositoryResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	existing, hadConnection, err := h.service.Get(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	// Three states, and they have to stay distinguishable: a token supplied, a token
	// left alone, and a token cleared. An absent field keeps what is stored; an empty
	// string clears it.
	hasToken := hadConnection && existing.HasCredential()
	if request.Body.Token != nil {
		switch token := strings.TrimSpace(*request.Body.Token); token {
		case "":
			if err := h.secrets.Clear(ctx, request.ProjectID, CredentialKey, actor); err != nil {
				return nil, err
			}
			hasToken = false
		default:
			if err := h.secrets.Write(ctx, request.ProjectID, CredentialKey, token, actor); err != nil {
				return nil, err
			}
			hasToken = true
		}
	}

	provider := ProviderGit
	if request.Body.Provider != nil {
		provider = Provider(*request.Body.Provider)
	}
	branch := ""
	if request.Body.DefaultBranch != nil {
		branch = *request.Body.DefaultBranch
	}

	actorID := actor.UserID
	connection, err := h.service.Connect(ctx, ConnectInput{
		ProjectID: request.ProjectID,
		Provider:  provider,
		URL:       request.Body.Url,
		Branch:    branch,
		HasToken:  hasToken,
		CreatedBy: &actorID,
	})
	if err != nil {
		return nil, err
	}

	if h.recorder != nil {
		project := request.ProjectID
		h.recorder.Record(ctx, audit.Entry{
			Action:     audit.ActionRepositoryConnected,
			ActorID:    &actorID,
			ActorEmail: actor.Email,
			Subject:    connection.URL,
			ProjectID:  &project,
			Detail: map[string]any{
				"provider":      string(connection.Provider),
				"branch":        connection.DefaultBranch,
				"hasCredential": connection.HasCredential(),
			},
		})
	}

	return api.ConnectRepository200JSONResponse(toAPI(connection)), nil
}

// DisconnectRepository removes the connection and leaves the token.
//
// Deliberately: clearing a secret is its own action, with its own audit entry, and
// somebody disconnecting a repository to reconnect it with a different URL should not
// have to paste the token again.
func (h *Handler) DisconnectRepository(
	ctx context.Context,
	request api.DisconnectRepositoryRequestObject,
) (api.DisconnectRepositoryResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	if err := h.service.Disconnect(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	if h.recorder != nil {
		actorID := actor.UserID
		project := request.ProjectID
		h.recorder.Record(ctx, audit.Entry{
			Action:     audit.ActionRepositoryDisconnected,
			ActorID:    &actorID,
			ActorEmail: actor.Email,
			Subject:    project.String(),
			ProjectID:  &project,
		})
	}

	return api.DisconnectRepository204Response{}, nil
}

func (h *Handler) SyncRepository(
	ctx context.Context,
	request api.SyncRepositoryRequestObject,
) (api.SyncRepositoryResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	if _, found, err := h.service.Get(ctx, request.ProjectID); err != nil {
		return nil, err
	} else if !found {
		return nil, apierr.NoRepositoryConnected()
	}

	ref := ""
	if request.Body != nil && request.Body.Ref != nil {
		ref = *request.Body.Ref
	}

	jobID, err := h.queue.SubmitSync(ctx, request.ProjectID, ref, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.SyncRepository202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

func toAPI(connection Connection) api.RepositoryConnection {
	body := api.RepositoryConnection{
		Id:            connection.ID,
		ProjectId:     connection.ProjectID,
		Provider:      api.RepositoryProvider(connection.Provider),
		Url:           connection.URL,
		DefaultBranch: &connection.DefaultBranch,
		HasCredential: connection.HasCredential(),
		LastCommit:    &connection.LastCommit,
		LastError:     &connection.LastError,
		CreatedAt:     connection.CreatedAt,
	}
	body.LastFetchedAt = connection.LastFetchedAt
	body.DetectedAt = connection.DetectedAt
	body.Detected = toAPIStack(connection.Detected)
	return body
}

func toAPIStack(stack Stack) *api.DetectedStack {
	summary := stack.Summary()
	inspected := stack.Inspected
	if inspected == nil {
		// An empty list rather than null: a client rendering "files read" should not
		// have to distinguish the two.
		inspected = []string{}
	}

	return &api.DetectedStack{
		Language:       &stack.Language,
		Framework:      &stack.Framework,
		PackageManager: &stack.PackageManager,
		Version:        &stack.Version,
		TestCommand:    &stack.TestCommand,
		CoverageTool:   &stack.CoverageTool,
		Confident:      stack.Confident,
		Inspected:      inspected,
		Summary:        &summary,
	}
}

// GetRepositoryMap returns the newest comprehension map.
func (h *Handler) GetRepositoryMap(
	ctx context.Context,
	request api.GetRepositoryMapRequestObject,
) (api.GetRepositoryMapResponseObject, error) {
	stored, found, err := h.service.LatestMap(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, apierr.NoRepositoryMap()
	}

	body, err := toAPIMap(stored)
	if err != nil {
		return nil, err
	}
	return api.GetRepositoryMap200JSONResponse(body), nil
}

// MapRepository queues an exploration.
func (h *Handler) MapRepository(
	ctx context.Context,
	request api.MapRepositoryRequestObject,
) (api.MapRepositoryResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)
	if err := h.projects.EnsureActive(ctx, request.ProjectID); err != nil {
		return nil, err
	}

	if _, found, err := h.service.Get(ctx, request.ProjectID); err != nil {
		return nil, err
	} else if !found {
		return nil, apierr.NoRepositoryConnected()
	}

	ref := ""
	if request.Body != nil && request.Body.Ref != nil {
		ref = *request.Body.Ref
	}

	jobID, err := h.queue.SubmitComprehend(ctx, request.ProjectID, ref, actor.UserID)
	if err != nil {
		return nil, err
	}

	events := "/api/v1/jobs/" + jobID.String() + "/events"
	return api.MapRepository202JSONResponse(api.JobReference{
		JobId:     jobID,
		Status:    api.JobStatusQueued,
		EventsUrl: &events,
	}), nil
}

// toAPIMap decodes the stored document into the response shape.
//
// The document is decoded rather than passed through, so a map written by an older
// version of the platform is rendered against today's contract instead of leaking its
// own shape to a client.
func toAPIMap(stored StoredMap) (api.RepositoryMap, error) {
	var document struct {
		Summary     string `json:"summary"`
		Controllers []struct {
			File   string   `json:"file"`
			Name   string   `json:"name"`
			Routes []string `json:"routes"`
			Calls  []string `json:"calls"`
		} `json:"controllers"`
		Services []struct {
			File           string `json:"file"`
			Name           string `json:"name"`
			Responsibility string `json:"responsibility"`
		} `json:"services"`
		DataAccess []struct {
			File   string   `json:"file"`
			Name   string   `json:"name"`
			Tables []string `json:"tables"`
		} `json:"dataAccess"`
		Uncovered []struct {
			File   string `json:"file"`
			Symbol string `json:"symbol"`
			Why    string `json:"why"`
		} `json:"uncovered"`
		Unknowns []string `json:"unknowns"`
		Trail    []struct {
			Action  string `json:"action"`
			Detail  string `json:"detail"`
			Reason  string `json:"reason"`
			Refused bool   `json:"refused"`
		} `json:"trail"`
	}

	if err := json.Unmarshal(stored.Document, &document); err != nil {
		return api.RepositoryMap{}, apierr.Internal(
			fmt.Errorf("decode the repository map: %w", err))
	}

	body := api.RepositoryMap{
		Id:        stored.ID,
		ProjectId: stored.ProjectID,
		Commit:    &stored.Commit,
		Stack:     &stored.Stack,
		Summary:   &document.Summary,
		Steps:     stored.Steps,
		CutShort:  stored.CutShort,
		ModelName: &stored.ModelName,
		CreatedAt: stored.CreatedAt,
	}

	controllers := make([]api.MappedController, 0, len(document.Controllers))
	for _, controller := range document.Controllers {
		controllers = append(controllers, api.MappedController{
			File: controller.File, Name: controller.Name,
			Routes: &controller.Routes, Calls: &controller.Calls,
		})
	}
	body.Controllers = &controllers

	services := make([]api.MappedComponent, 0, len(document.Services))
	for _, service := range document.Services {
		services = append(services, api.MappedComponent{
			File: service.File, Name: service.Name, Responsibility: &service.Responsibility,
		})
	}
	body.Services = &services

	stores := make([]api.MappedDataStore, 0, len(document.DataAccess))
	for _, store := range document.DataAccess {
		stores = append(stores, api.MappedDataStore{
			File: store.File, Name: store.Name, Tables: &store.Tables,
		})
	}
	body.DataAccess = &stores

	gaps := make([]api.UncoveredPath, 0, len(document.Uncovered))
	for _, gap := range document.Uncovered {
		gaps = append(gaps, api.UncoveredPath{
			File: gap.File, Symbol: gap.Symbol, Why: &gap.Why,
		})
	}
	body.Uncovered = &gaps

	trail := make([]api.ExplorationStep, 0, len(document.Trail))
	for _, step := range document.Trail {
		trail = append(trail, api.ExplorationStep{
			Action: step.Action, Detail: &step.Detail,
			Reason: &step.Reason, Refused: &step.Refused,
		})
	}
	body.Trail = &trail

	unknowns := document.Unknowns
	if unknowns == nil {
		unknowns = []string{}
	}
	body.Unknowns = &unknowns

	return body, nil
}
