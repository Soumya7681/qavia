package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Discoverer runs a connection test, delegating the protocol to the AI service.
type Discoverer interface {
	DiscoverMCPTools(ctx context.Context, config llm.MCPServerConfig) ([]llm.MCPTool, error)
}

// Recorder writes audit entries.
type Recorder interface {
	Record(ctx context.Context, entry audit.Entry)
}

// Handler implements the MCP slice of the generated server interface.
type Handler struct {
	service    *Service
	discoverer Discoverer
	recorder   Recorder
}

func NewHandler(service *Service, discoverer Discoverer, recorder Recorder) *Handler {
	return &Handler{service: service, discoverer: discoverer, recorder: recorder}
}

// ListMCPServers returns every configured server.
func (h *Handler) ListMCPServers(
	ctx context.Context,
	_ api.ListMCPServersRequestObject,
) (api.ListMCPServersResponseObject, error) {
	servers, err := h.service.List(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]api.MCPServer, 0, len(servers))
	for _, server := range servers {
		items = append(items, toAPI(server))
	}
	return api.ListMCPServers200JSONResponse(api.MCPServerList{Items: items}), nil
}

// CreateMCPServer adds a server, deny-all.
func (h *Handler) CreateMCPServer(
	ctx context.Context,
	request api.CreateMCPServerRequestObject,
) (api.CreateMCPServerResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := CreateInput{
		Name:      request.Body.Name,
		Transport: Transport(request.Body.Transport),
		Scope:     Scope(request.Body.Scope),
		CreatedBy: actor.UserID,
	}
	fillShape(&input.Command, &input.Args, &input.URL, request.Body.Command, request.Body.Args, request.Body.Url)
	input.ScopeID = request.Body.ProjectId
	if request.Body.Credential != nil {
		input.Credential = *request.Body.Credential
	}
	if request.Body.AutoWrite != nil {
		input.AutoWrite = *request.Body.AutoWrite
	}

	server, err := h.service.Create(ctx, input)
	if err != nil {
		return nil, err
	}

	project := server.ScopeID
	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionIntegrationSaved,
		Subject:   server.Name,
		ProjectID: project,
		Detail:    map[string]any{"integration": "mcp", "transport": string(server.Transport)},
	})

	return api.CreateMCPServer201JSONResponse(toAPI(server)), nil
}

// GetMCPServer returns one server.
func (h *Handler) GetMCPServer(
	ctx context.Context,
	request api.GetMCPServerRequestObject,
) (api.GetMCPServerResponseObject, error) {
	server, err := h.service.Get(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	return api.GetMCPServer200JSONResponse(toAPI(server)), nil
}

// UpdateMCPServer changes a server and its allowlist.
func (h *Handler) UpdateMCPServer(
	ctx context.Context,
	request api.UpdateMCPServerRequestObject,
) (api.UpdateMCPServerResponseObject, error) {
	actor := httpx.MustCurrentUser(ctx)

	input := UpdateInput{
		Name:      request.Body.Name,
		Transport: Transport(request.Body.Transport),
		Scope:     Scope(request.Body.Scope),
		ScopeID:   request.Body.ProjectId,
		IsEnabled: true,
	}
	fillShape(&input.Command, &input.Args, &input.URL, request.Body.Command, request.Body.Args, request.Body.Url)
	if request.Body.EnabledTools != nil {
		input.EnabledTools = *request.Body.EnabledTools
	}
	if request.Body.AutoWrite != nil {
		input.AutoWrite = *request.Body.AutoWrite
	}
	if request.Body.IsEnabled != nil {
		input.IsEnabled = *request.Body.IsEnabled
	}
	input.Credential = request.Body.Credential // nil keeps, "" clears

	server, err := h.service.Update(ctx, request.ServerID, input)
	if err != nil {
		return nil, err
	}

	h.recorder.Record(ctx, audit.Entry{
		ActorID:   &actor.UserID,
		Action:    audit.ActionIntegrationSaved,
		Subject:   server.Name,
		ProjectID: server.ScopeID,
		Detail:    map[string]any{"integration": "mcp", "enabledTools": len(server.EnabledTools)},
	})

	return api.UpdateMCPServer200JSONResponse(toAPI(server)), nil
}

// DeleteMCPServer removes a server.
func (h *Handler) DeleteMCPServer(
	ctx context.Context,
	request api.DeleteMCPServerRequestObject,
) (api.DeleteMCPServerResponseObject, error) {
	if err := h.service.Delete(ctx, request.ServerID); err != nil {
		return nil, err
	}
	return api.DeleteMCPServer204Response{}, nil
}

// TestMCPServer connects and lists the tools.
//
// A connection failure is not a 500: it is the answer the test exists to give, recorded
// as unhealthy on the server so the admin sees why. The server is returned either way,
// with the tools it found or the reason it could not (BE-10.1).
func (h *Handler) TestMCPServer(
	ctx context.Context,
	request api.TestMCPServerRequestObject,
) (api.TestMCPServerResponseObject, error) {
	config, err := h.service.ConfigFor(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}

	tools, discErr := h.discoverer.DiscoverMCPTools(ctx, llm.MCPServerConfig{
		Transport:  string(config.Transport),
		Command:    config.Command,
		Args:       config.Args,
		URL:        config.URL,
		Credential: config.Credential,
	})
	if discErr != nil {
		if err := h.service.RecordHealth(ctx, request.ServerID, HealthUnreachable, discErr.Error(), nil); err != nil {
			return nil, err
		}
		server, err := h.service.Get(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		return api.TestMCPServer502JSONResponse(toAPI(server)), nil
	}

	discovered := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		discovered = append(discovered, Tool{Name: tool.Name, Description: tool.Description, Write: tool.Write})
	}
	if err := h.service.RecordHealth(ctx, request.ServerID, HealthHealthy, "", discovered); err != nil {
		return nil, err
	}

	server, err := h.service.Get(ctx, request.ServerID)
	if err != nil {
		return nil, err
	}
	return api.TestMCPServer200JSONResponse(toAPI(server)), nil
}

// ListMCPCalls returns the call audit.
func (h *Handler) ListMCPCalls(
	ctx context.Context,
	request api.ListMCPCallsRequestObject,
) (api.ListMCPCallsResponseObject, error) {
	limit := 0
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}

	calls, err := h.service.Calls(ctx, request.Params.ServerID, request.Params.ProjectID, limit)
	if err != nil {
		return nil, err
	}

	items := make([]api.MCPCall, 0, len(calls))
	for _, call := range calls {
		items = append(items, toAPICall(call))
	}
	return api.ListMCPCalls200JSONResponse(api.MCPCallList{Items: items}), nil
}

// fillShape copies the transport-specific fields, tolerating the nil pointers the
// generated request uses for optional fields.
func fillShape(command *string, args *[]string, url *string, bodyCommand *string, bodyArgs *[]string, bodyURL *string) {
	if bodyCommand != nil {
		*command = strings.TrimSpace(*bodyCommand)
	}
	if bodyArgs != nil {
		*args = *bodyArgs
	}
	if bodyURL != nil {
		*url = strings.TrimSpace(*bodyURL)
	}
}

func toAPI(server Server) api.MCPServer {
	tools := make([]api.MCPServerTool, 0, len(server.DiscoveredTools))
	for _, tool := range server.DiscoveredTools {
		write := tool.Write
		description := tool.Description
		tools = append(tools, api.MCPServerTool{Name: tool.Name, Description: &description, Write: &write})
	}

	enabled := server.IsEnabled
	autoWrite := server.AutoWrite
	hasCredential := server.HasCredentials
	command := server.Command
	url := server.URL
	detail := server.HealthDetail

	body := api.MCPServer{
		Id:              server.ID,
		Name:            server.Name,
		Transport:       api.MCPTransport(server.Transport),
		Command:         &command,
		Args:            &server.Args,
		Url:             &url,
		Scope:           api.MCPScope(server.Scope),
		ProjectId:       server.ScopeID,
		EnabledTools:    server.EnabledTools,
		DiscoveredTools: &tools,
		AutoWrite:       &autoWrite,
		IsEnabled:       enabled,
		HasCredential:   &hasCredential,
		HealthStatus:    api.MCPServerHealthStatus(server.HealthStatus),
		HealthDetail:    &detail,
		HealthCheckedAt: server.HealthCheckedAt,
		CreatedAt:       server.CreatedAt,
	}
	return body
}

func toAPICall(call Call) api.MCPCall {
	server := call.ServerName
	agent := call.Agent
	errText := call.Error

	body := api.MCPCall{
		Id:         call.ID,
		ServerId:   call.ServerID,
		ServerName: &server,
		Tool:       call.Tool,
		Status:     api.MCPCallStatus(call.Status),
		Error:      &errText,
		Agent:      &agent,
		ProjectId:  call.ProjectID,
		JobId:      call.JobID,
		At:         call.At,
	}
	if len(call.Arguments) > 0 {
		var arguments map[string]any
		if err := json.Unmarshal(call.Arguments, &arguments); err == nil {
			body.Arguments = &arguments
		}
	}
	return body
}
