package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/ingest"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Projects is the slice of the projects service this package needs.
type Projects interface {
	EnsureMember(ctx context.Context, actor httpx.Principal, projectID uuid.UUID) error
}

// Handler implements the test-data slice of the generated server interface.
type Handler struct {
	service  *Service
	projects Projects
}

func NewHandler(service *Service, projectsService Projects) *Handler {
	return &Handler{service: service, projects: projectsService}
}

// ListTestDataShapes says what a project can generate from.
func (h *Handler) ListTestDataShapes(
	ctx context.Context,
	request api.ListTestDataShapesRequestObject,
) (api.ListTestDataShapesResponseObject, error) {
	shapes, err := h.service.Shapes(ctx, request.ProjectID)
	if err != nil {
		return nil, err
	}

	items := make([]api.TestDataShape, 0, len(shapes))
	for _, shape := range shapes {
		item := api.TestDataShape{
			Endpoint:   shape.Endpoint,
			HasRequest: shape.HasRequest,
			Summary:    optional(shape.Summary),
		}
		if len(shape.Responses) > 0 {
			item.Responses = &shape.Responses
		}
		if len(shape.Fields) > 0 {
			item.Fields = &shape.Fields
		}
		items = append(items, item)
	}

	return api.ListTestDataShapes200JSONResponse(api.TestDataShapeList{Items: items}), nil
}

// ListDumpTables says what an uploaded schema dump declares.
func (h *Handler) ListDumpTables(
	ctx context.Context,
	request api.ListDumpTablesRequestObject,
) (api.ListDumpTablesResponseObject, error) {
	tables, warnings, err := h.service.Tables(ctx, request.ProjectID, request.Params.ArtifactID)
	if err != nil {
		return nil, err
	}

	body := api.DumpTableList{Items: make([]api.DumpTable, 0, len(tables))}
	for _, table := range tables {
		columns := make([]api.DumpColumn, 0, len(table.Columns))
		for _, column := range table.Columns {
			entry := api.DumpColumn{
				Name:       column.Name,
				Type:       column.Type,
				NotNull:    column.NotNull,
				PrimaryKey: &column.PrimaryKey,
				Length:     column.Length,
			}
			if len(column.Enum) > 0 {
				entry.Enum = &column.Enum
			}
			columns = append(columns, entry)
		}
		body.Items = append(body.Items, api.DumpTable{Name: table.Name, Columns: columns})
	}
	if len(warnings) > 0 {
		body.Warnings = &warnings
	}

	return api.ListDumpTables200JSONResponse(body), nil
}

// GenerateTestData generates a valid set, or streams it as an export.
func (h *Handler) GenerateTestData(
	ctx context.Context,
	request api.GenerateTestDataRequestObject,
) (api.GenerateTestDataResponseObject, error) {
	input := GenerateInput{
		ProjectID: request.ProjectID,
		Source:    sourceOf(request.Body.Source),
		Count:     10,
	}
	if request.Body.Count != nil {
		input.Count = *request.Body.Count
	}
	if request.Body.Seed != nil {
		input.Seed = uint64(*request.Body.Seed) //nolint:gosec // A reproducibility handle, not a bound.
	}
	if request.Body.Locale != nil {
		input.Locale = *request.Body.Locale
	}
	if request.Body.Fields != nil {
		input.Fields = *request.Body.Fields
	}

	result, err := h.service.Generate(ctx, input)
	if err != nil {
		return nil, err
	}

	if request.Body.Format != nil && *request.Body.Format != api.Json {
		table := ""
		if request.Body.Table != nil {
			table = *request.Body.Table
		}

		return &export{
			ctx:     ctx,
			records: result.Records,
			options: ExportOptions{
				Format: Format(*request.Body.Format),
				Table:  table,
				Seed:   result.Seed,
			},
		}, nil
	}

	body := api.TestDataSet{
		Seed:    int64(result.Seed), //nolint:gosec // Reported so the caller can ask for the same set again.
		Records: make([]map[string]any, 0, len(result.Records)),
	}
	for _, record := range result.Records {
		body.Records = append(body.Records, record.Map())
	}
	if len(result.Warnings) > 0 {
		body.Warnings = &result.Warnings
	}

	return api.GenerateTestData200JSONResponse(body), nil
}

// GenerateInvalidTestData generates the boundary and invalid set.
func (h *Handler) GenerateInvalidTestData(
	ctx context.Context,
	request api.GenerateInvalidTestDataRequestObject,
) (api.GenerateInvalidTestDataResponseObject, error) {
	input := GenerateInput{
		ProjectID: request.ProjectID,
		Source:    sourceOf(request.Body.Source),
	}
	if request.Body.Seed != nil {
		input.Seed = uint64(*request.Body.Seed) //nolint:gosec // A reproducibility handle.
	}
	if request.Body.Locale != nil {
		input.Locale = *request.Body.Locale
	}

	limit := 50
	if request.Body.Limit != nil {
		limit = *request.Body.Limit
	}

	var kinds []InvalidKind
	if request.Body.Kinds != nil {
		for _, kind := range *request.Body.Kinds {
			kinds = append(kinds, InvalidKind(kind))
		}
	}

	result, err := h.service.GenerateInvalid(ctx, input, kinds, limit)
	if err != nil {
		return nil, err
	}

	body := api.InvalidTestDataSet{
		Seed:    int64(result.Seed), //nolint:gosec // Reported so the caller can ask for the same set again.
		Records: make([]api.InvalidTestDataRecord, 0, len(result.Records)),
	}
	for _, record := range result.Records {
		body.Records = append(body.Records, api.InvalidTestDataRecord{
			Field:       record.Field,
			Kind:        api.InvalidDataKind(record.Kind),
			Violates:    optional(record.Violates),
			Expectation: optional(record.Expectation),
			Record:      record.Record.Map(),
		})
	}
	if len(result.Skipped) > 0 {
		body.Skipped = &result.Skipped
	}

	return api.GenerateInvalidTestData200JSONResponse(body), nil
}

// sourceOf reads the request's source.
func sourceOf(source api.TestDataSource) Source {
	out := Source{DumpArtifactID: source.DumpArtifactId}
	if source.Endpoint != nil {
		out.Endpoint = strings.TrimSpace(*source.Endpoint)
	}
	if source.Response != nil {
		out.Response = strings.TrimSpace(*source.Response)
	}
	if source.Table != nil {
		out.Table = strings.TrimSpace(*source.Table)
	}
	return out
}

// FromSchema builds a source for a caller that already has a schema, such as the SQL
// dump path (BE-8.7).
func FromSchema(schema ingest.Schema) Source { return Source{Schema: &schema} }

// export streams a generated set in the requested format.
//
// Written straight to the response rather than buffered, because the whole argument for
// generated data is that asking for a lot of it is cheap: a hundred thousand rows must
// not be assembled in memory before the first byte leaves.
type export struct {
	ctx     context.Context
	records []Record
	options ExportOptions
}

func (e *export) VisitGenerateTestDataResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", e.options.Format.ContentType())
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q",
			"qavia-test-data."+e.options.Format.Extension()))
	w.WriteHeader(http.StatusOK)

	if err := Write(w, e.records, e.options); err != nil {
		// The response is already committed, so there is nothing to tell the client: the
		// truncated file is the signal, and the reason belongs in the log.
		slog.WarnContext(e.ctx, "stream a test data export",
			"format", e.options.Format, "error", err)
	}
	return nil
}

// optional renders an empty string as an absent field.
func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
