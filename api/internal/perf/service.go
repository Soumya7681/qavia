package perf

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// Endpoints is the slice of the ingest service this package needs.
type Endpoints interface {
	Endpoints(ctx context.Context, projectID uuid.UUID, artifactID *uuid.UUID) ([]ingest.Endpoint, error)
}

// Gateway is the slice of the AI gateway this package needs.
type Gateway interface {
	Perf(ctx context.Context, call llm.AgentCall, input llm.PerfInput) (llm.AgentResult, error)
}

// Service generates load scripts.
type Service struct {
	endpoints Endpoints
	gateway   Gateway
}

func NewService(endpoints Endpoints, gateway Gateway) *Service {
	return &Service{endpoints: endpoints, gateway: gateway}
}

// Script is a generated load script.
type Script struct {
	Content   string
	Exercises []string
	Notes     string
	Model     string
}

// Generate writes a k6 script for a project under a profile.
//
// The endpoints are the cacheable prefix and the profile is the fixed authorised load.
// The agent is told to use the profile's numbers exactly: raising them would be the
// model deciding how hard to hit a client's server, and that decision was already made
// by whoever set the profile (BE-9.1.1).
func (s *Service) Generate(
	ctx context.Context,
	projectID, jobID uuid.UUID,
	profile Profile,
) (Script, error) {
	endpoints, err := s.endpoints.Endpoints(ctx, projectID, nil)
	if err != nil {
		return Script{}, apierr.Internal(fmt.Errorf("read the project's endpoints: %w", err))
	}
	if len(endpoints) == 0 {
		return Script{}, apierr.NoEndpointsToProbe()
	}

	result, err := s.gateway.Perf(ctx, llm.AgentCall{ProjectID: &projectID, JobID: &jobID},
		llm.PerfInput{
			Endpoints: endpointSummary(endpoints),
			Profile:   profile.Describe(),
			P95Ms:     profile.P95TargetMs,
			ErrorRate: profile.MaxErrorRate,
		})
	if err != nil {
		return Script{}, err
	}

	var generated struct {
		Content   string   `json:"content"`
		Exercises []string `json:"exercises"`
		Notes     string   `json:"notes"`
	}
	if err := json.Unmarshal(result.Raw, &generated); err != nil {
		return Script{}, apierr.Internal(fmt.Errorf("read the generated load script: %w", err))
	}
	if generated.Content == "" {
		return Script{}, apierr.Internal(fmt.Errorf("the load script was empty"))
	}

	return Script{
		Content:   generated.Content,
		Exercises: generated.Exercises,
		Notes:     generated.Notes,
		Model:     result.ModelName,
	}, nil
}

// endpointSummary is the shape the agent reads: the methods, paths, and the parameters
// worth loading, without the full request and response schemas a load test does not
// need. A load test cares which endpoints exist and what they take, not the shape of
// every field.
func endpointSummary(endpoints []ingest.Endpoint) map[string]any {
	summarised := make([]map[string]any, 0, len(endpoints))
	for _, endpoint := range endpoints {
		params := make([]string, 0, len(endpoint.Parameters))
		for _, parameter := range endpoint.Parameters {
			params = append(params, parameter.In+":"+parameter.Name)
		}
		summarised = append(summarised, map[string]any{
			"method":     endpoint.Method,
			"path":       endpoint.Path,
			"summary":    endpoint.Summary,
			"parameters": params,
			"hasBody":    endpoint.Request.Schema.Type != "" || len(endpoint.Request.Schema.Properties) > 0,
		})
	}
	return map[string]any{"endpoints": summarised}
}
