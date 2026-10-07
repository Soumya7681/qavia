package datagen

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// The service (BE-8.1, BE-8.5).
//
// It holds no data of its own, and that is the design rather than an omission: a
// generated set is a pure function of a schema, a seed, and a count, so storing it
// would be storing something reproducible in one line. What a caller keeps is the
// seed. Nothing here writes a row, which also means nothing here can leak a fixture
// into a backup.

// Endpoints is the slice of the ingest service this package needs.
type Endpoints interface {
	Endpoints(ctx context.Context, projectID uuid.UUID, artifactID *uuid.UUID) ([]ingest.Endpoint, error)
}

// Dumps reads an uploaded SQL dump back out of object storage, so data can be shaped
// by a client's own tables without a database credential (BE-8.7.1).
type Dumps interface {
	// ReadDump returns the dump's text, or a stated reason: the artifact is not this
	// project's, or it is not a dump.
	ReadDump(ctx context.Context, projectID, artifactID uuid.UUID) (string, error)
}

// Service answers generation requests for a project.
type Service struct {
	endpoints Endpoints
	generator *Generator

	// enrich is the optional AI path for the handful of fields where semantics matter.
	// Nil is the normal state and the fallback is always the faker (BE-8.2).
	enrich Enricher

	// dumps is set on a process that can read an uploaded schema dump.
	dumps Dumps
}

func NewService(endpoints Endpoints, options ...Option) *Service {
	service := &Service{endpoints: endpoints, generator: NewGenerator()}
	for _, option := range options {
		option(service)
	}
	return service
}

// Option configures the service.
type Option func(*Service)

// WithEnricher turns on the AI path for opt-in fields.
func WithEnricher(enricher Enricher) Option {
	return func(s *Service) { s.enrich = enricher }
}

// WithDumps turns on generation from an uploaded SQL dump.
func WithDumps(dumps Dumps) Option {
	return func(s *Service) { s.dumps = dumps }
}

// Source names what to generate from.
type Source struct {
	// Endpoint is "METHOD /path", matched against the project's parsed specification.
	// The request body's schema is what gets generated.
	Endpoint string

	// Response generates from a response's schema instead of the request's, for
	// seeding a mock or a fixture the client app reads.
	Response string

	// Schema is an explicit schema, for a caller that assembled a shape itself.
	Schema *ingest.Schema

	// DumpArtifactID and Table generate from an uploaded SQL dump's table, parsed
	// locally with no database connection and no MCP server (BE-8.7.1).
	DumpArtifactID *uuid.UUID
	Table          string
}

// GenerateInput is one request.
type GenerateInput struct {
	ProjectID uuid.UUID
	Source    Source

	Count  int
	Seed   uint64
	Locale string

	// Fields names the fields the AI path may write, and it is opt-in per field for a
	// reason: the bulk is free and instant, and a model asked for a hundred records of
	// everything costs money to be occasionally wrong (BE-8.2.2).
	Fields []string
}

// Generate produces a valid set.
func (s *Service) Generate(ctx context.Context, input GenerateInput) (Result, error) {
	schema, err := s.schemaFor(ctx, input.ProjectID, input.Source)
	if err != nil {
		return Result{}, err
	}

	result, err := s.generator.Generate(Request{
		Schema: schema,
		Count:  input.Count,
		Seed:   input.Seed,
		Locale: input.Locale,
	})
	if err != nil {
		return Result{}, apierr.Validation(err.Error(), nil)
	}

	// The AI path runs after the faker, over the records it produced, so a provider
	// outage costs realism on a few fields rather than the whole request (BE-8.2.2).
	if len(input.Fields) > 0 && s.enrich != nil {
		notes := s.enrich.Enrich(ctx, EnrichInput{
			ProjectID: input.ProjectID,
			Records:   result.Records,
			Fields:    input.Fields,
			Schema:    schema,
			Locale:    input.Locale,
		})
		result.Warnings = append(result.Warnings, notes...)
	} else if len(input.Fields) > 0 {
		result.Warnings = append(result.Warnings,
			"no AI provider is configured for field enrichment, so every field came from the faker")
	}

	return result, nil
}

// GenerateInvalid produces the boundary and invalid set.
func (s *Service) GenerateInvalid(
	ctx context.Context,
	input GenerateInput,
	kinds []InvalidKind,
	limit int,
) (InvalidResult, error) {
	schema, err := s.schemaFor(ctx, input.ProjectID, input.Source)
	if err != nil {
		return InvalidResult{}, err
	}

	result, err := s.generator.GenerateInvalid(InvalidRequest{
		Schema: schema,
		Kinds:  kinds,
		Limit:  limit,
		Seed:   input.Seed,
		Locale: input.Locale,
	})
	if err != nil {
		return InvalidResult{}, apierr.Validation(err.Error(), nil)
	}
	return result, nil
}

// Shapes lists what a project can generate from, so a UI can offer the choice rather
// than asking somebody to type an endpoint path exactly.
type Shape struct {
	Endpoint string
	Summary  string

	// HasRequest and Responses say which schemas are available: an endpoint with no
	// request body cannot seed a POST fixture, and saying so beats generating one
	// empty record.
	HasRequest bool
	Responses  []string

	Fields []string
}

// Shapes is every endpoint of a project with a schema worth generating from.
func (s *Service) Shapes(ctx context.Context, projectID uuid.UUID) ([]Shape, error) {
	endpoints, err := s.endpoints.Endpoints(ctx, projectID, nil)
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the project's endpoints: %w", err))
	}

	shapes := make([]Shape, 0, len(endpoints))
	for _, endpoint := range endpoints {
		shape := Shape{
			Endpoint:   endpoint.Key(),
			Summary:    endpoint.Summary,
			HasRequest: hasSchema(endpoint.Request.Schema),
		}

		for _, response := range endpoint.Responses {
			if hasSchema(response.Schema) {
				shape.Responses = append(shape.Responses, response.Status)
			}
		}

		switch {
		case shape.HasRequest:
			shape.Fields = fieldNames(endpoint.Request.Schema)
		case len(shape.Responses) > 0:
			shape.Fields = fieldNames(firstResponseSchema(endpoint))
		}

		if !shape.HasRequest && len(shape.Responses) == 0 {
			// Nothing to generate from. Omitted rather than listed as an option that
			// produces one empty record.
			continue
		}
		shapes = append(shapes, shape)
	}
	return shapes, nil
}

// Tables lists what a dump declares, so a UI can offer the tables rather than asking
// somebody to remember their names.
func (s *Service) Tables(
	ctx context.Context,
	projectID, artifactID uuid.UUID,
) ([]Table, []string, error) {
	if s.dumps == nil {
		return nil, nil, apierr.Validation(
			"This process cannot read uploaded dumps.", nil)
	}

	dump, err := s.dumps.ReadDump(ctx, projectID, artifactID)
	if err != nil {
		return nil, nil, err
	}

	tables, warnings, err := ParseDump(dump)
	if err != nil {
		return nil, nil, apierr.Validation(err.Error(), map[string]any{"field": "dumpArtifactId"})
	}
	return tables, warnings, nil
}

// schemaFor resolves what to generate from.
func (s *Service) schemaFor(
	ctx context.Context,
	projectID uuid.UUID,
	source Source,
) (ingest.Schema, error) {
	if source.Schema != nil {
		return *source.Schema, nil
	}
	if source.DumpArtifactID != nil {
		return s.dumpSchema(ctx, projectID, *source.DumpArtifactID, source.Table)
	}
	if strings.TrimSpace(source.Endpoint) == "" {
		return ingest.Schema{}, apierr.Validation(
			"Name an endpoint to generate data for, or supply a schema.",
			map[string]any{"field": "endpoint"})
	}

	endpoints, err := s.endpoints.Endpoints(ctx, projectID, nil)
	if err != nil {
		return ingest.Schema{}, apierr.Internal(fmt.Errorf("read the project's endpoints: %w", err))
	}

	wanted := normaliseKey(source.Endpoint)
	for _, endpoint := range endpoints {
		if normaliseKey(endpoint.Key()) != wanted {
			continue
		}

		if source.Response != "" {
			for _, response := range endpoint.Responses {
				if response.Status == source.Response && hasSchema(response.Schema) {
					return response.Schema, nil
				}
			}
			return ingest.Schema{}, apierr.Validation(
				fmt.Sprintf("%s has no %s response with a schema.", endpoint.Key(), source.Response),
				map[string]any{"field": "response"})
		}

		if hasSchema(endpoint.Request.Schema) {
			return endpoint.Request.Schema, nil
		}

		// A GET has no request body, and generating from its success response is what
		// the caller almost certainly meant: it is the shape of the data the endpoint
		// returns, which is what a fixture or a mock needs.
		if schema := firstResponseSchema(endpoint); hasSchema(schema) {
			return schema, nil
		}

		return ingest.Schema{}, apierr.Validation(
			fmt.Sprintf("%s declares no request or response schema to generate from.", endpoint.Key()),
			map[string]any{"field": "endpoint"})
	}

	return ingest.Schema{}, apierr.Validation(
		fmt.Sprintf("%q is not an endpoint in this project's specification.", source.Endpoint),
		map[string]any{"field": "endpoint"})
}

// dumpSchema resolves one table of an uploaded dump.
func (s *Service) dumpSchema(
	ctx context.Context,
	projectID, artifactID uuid.UUID,
	table string,
) (ingest.Schema, error) {
	tables, _, err := s.Tables(ctx, projectID, artifactID)
	if err != nil {
		return ingest.Schema{}, err
	}

	wanted := strings.ToLower(strings.TrimSpace(table))
	if wanted == "" {
		if len(tables) == 1 {
			// One table in the dump is unambiguous, and making somebody name it would be
			// asking a question with one possible answer.
			return tables[0].Schema(), nil
		}

		names := make([]string, 0, len(tables))
		for _, candidate := range tables {
			names = append(names, candidate.Name)
		}
		return ingest.Schema{}, apierr.Validation(
			fmt.Sprintf("This dump declares %d tables, so name the one to generate from.", len(tables)),
			map[string]any{"field": "table", "tables": names})
	}

	for _, candidate := range tables {
		if strings.EqualFold(candidate.Name, wanted) {
			return candidate.Schema(), nil
		}
	}

	return ingest.Schema{}, apierr.Validation(
		fmt.Sprintf("%q is not a table in this dump.", table),
		map[string]any{"field": "table"})
}

// firstResponseSchema is the schema of the lowest-numbered success response, which is
// the one that describes the normal case.
func firstResponseSchema(endpoint ingest.Endpoint) ingest.Schema {
	statuses := make([]string, 0, len(endpoint.Responses))
	byStatus := map[string]ingest.Schema{}

	for _, response := range endpoint.Responses {
		if hasSchema(response.Schema) {
			statuses = append(statuses, response.Status)
			byStatus[response.Status] = response.Schema
		}
	}
	if len(statuses) == 0 {
		return ingest.Schema{}
	}

	sort.Strings(statuses)
	for _, status := range statuses {
		if strings.HasPrefix(status, "2") {
			return byStatus[status]
		}
	}
	return byStatus[statuses[0]]
}

// fieldNames lists a schema's top-level fields, which is what a UI offers for
// per-field AI opt-in.
func fieldNames(schema ingest.Schema) []string {
	if schema.Type == "array" && schema.Items != nil {
		schema = *schema.Items
	}
	return sortedKeys(schema.Properties)
}

func hasSchema(schema ingest.Schema) bool {
	return schema.Type != "" || len(schema.Properties) > 0 || schema.Items != nil
}

// normaliseKey makes endpoint matching forgiving about case and spacing, and nothing
// else: a path is a path, and matching it loosely would generate data for the wrong
// operation.
func normaliseKey(key string) string {
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) < 2 {
		return strings.ToUpper(strings.TrimSpace(key))
	}
	return strings.ToUpper(fields[0]) + " " + fields[1]
}
