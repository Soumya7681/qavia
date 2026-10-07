package llm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hyscaler/qavia/api/internal/llm/aigen"
)

// The MCP connection test (BE-10.1).
//
// Go owns the server configuration and its credential; this call carries them to the AI
// service for one connection, because the protocol is spoken there — langchain-mcp-adapters
// bridges MCP into LangChain tools once, for every provider (ai-architecture.md 5.3). The
// tools come back and Go stores the allowlist and the audit. This service holds nothing.

// MCPServerConfig is what a connection needs.
type MCPServerConfig struct {
	Transport  string
	Command    string
	Args       []string
	URL        string
	Credential string
}

// MCPTool is one tool a server exposes.
type MCPTool struct {
	Name        string
	Description string
	Write       bool
}

// DiscoverMCPTools connects to a server and lists its tools. A connection failure is a
// domain error the caller records as unhealthy, not a 500.
func (g *Gateway) DiscoverMCPTools(ctx context.Context, config MCPServerConfig) ([]MCPTool, error) {
	request := aigen.MCPDiscoverRequest{
		Transport:  &config.Transport,
		Command:    &config.Command,
		Args:       &config.Args,
		Url:        &config.URL,
		Credential: &config.Credential,
	}

	response, err := g.client.DiscoverMCP(ctx, request)
	if err != nil {
		return nil, err
	}

	tools := make([]MCPTool, 0)
	if response.Tools != nil {
		for _, tool := range *response.Tools {
			mapped := MCPTool{Name: tool.Name}
			if tool.Description != nil {
				mapped.Description = *tool.Description
			}
			if tool.Write != nil {
				mapped.Write = *tool.Write
			}
			tools = append(tools, mapped)
		}
	}
	return tools, nil
}

// DiscoverMCP is the client method for the discover endpoint.
func (c *Client) DiscoverMCP(
	ctx context.Context,
	request aigen.MCPDiscoverRequest,
) (*aigen.MCPDiscoverResponse, error) {
	response, err := c.api.DiscoverMCPToolsWithResponse(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("discover mcp tools: %w", err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return nil, serviceError(response.StatusCode(), response.Body)
	}
	return response.JSON200, nil
}
