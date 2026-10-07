package ingest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// maxSchemaDepth stops the flattener on a circular reference.
//
// Specifications reference themselves constantly: a Comment has an Author who has
// Comments. Depth is the simplest correct answer, and four levels is more nesting
// than a test designer reads anyway.
const maxSchemaDepth = 4

// maxEnumValues bounds an enum in the normalized model. A 500-value enum in the
// prompt costs tokens and produces one boundary case either way.
const maxEnumValues = 40

// ParseOpenAPI normalizes an OpenAPI 3.0 or 3.1 document.
//
// libopenapi rather than a hand-rolled parser: $ref resolution, including circular
// references, is the hard part and getting it subtly wrong produces test cases for
// endpoints that do not exist.
func ParseOpenAPI(raw []byte) (Document, error) {
	document, err := libopenapi.NewDocument(raw)
	if err != nil {
		return Document{}, &ParseError{Message: "this file is not a readable OpenAPI document"}
	}

	model, buildErr := document.BuildV3Model()
	if model == nil {
		return Document{}, openAPIError(buildErr)
	}

	spec := model.Model
	out := Document{Format: FormatOpenAPI}

	if spec.Info != nil {
		out.Title = spec.Info.Title
		out.Version = spec.Info.Version
		out.Description = spec.Info.Description
	}
	for _, server := range spec.Servers {
		out.Servers = append(out.Servers, server.URL)
	}

	// An error that did not stop the build is a warning: a specification with one
	// unresolvable $ref still describes 39 usable endpoints, and refusing all of
	// them helps nobody.
	if buildErr != nil {
		out.Warnings = append(out.Warnings, buildErr.Error())
	}

	schemes := securitySchemes(&spec)
	globalSecurity := requirementsOf(spec.Security, schemes)

	if spec.Paths == nil {
		return out, &ParseError{Message: "this specification describes no paths"}
	}

	for pair := spec.Paths.PathItems.First(); pair != nil; pair = pair.Next() {
		path := pair.Key()
		item := pair.Value()

		for method, operation := range item.GetOperations().FromOldest() {
			endpoint := Endpoint{
				Method:      strings.ToUpper(method),
				Path:        path,
				OperationID: operation.OperationId,
				Summary:     operation.Summary,
				Description: operation.Description,
				SourceRef:   sourceRef(operation.GoLow().RootNode.Line, operation.GoLow().RootNode.Column),
			}

			// Path-level parameters apply to every operation under the path, and a
			// specification that declares an id there once expects it on all of them.
			endpoint.Parameters = append(
				parameters(item.Parameters), parameters(operation.Parameters)...)

			endpoint.Request = requestBody(operation.RequestBody)
			endpoint.Responses = responses(operation.Responses)

			endpoint.Security = requirementsOf(operation.Security, schemes)
			if len(endpoint.Security) == 0 {
				endpoint.Security = globalSecurity
			}

			out.Endpoints = append(out.Endpoints, endpoint)
		}
	}

	if len(out.Endpoints) == 0 {
		return out, &ParseError{Message: "this specification describes no operations"}
	}

	SortEndpoints(out.Endpoints)
	return out, nil
}

// openAPIError turns a build failure into one message naming a line.
func openAPIError(err error) error {
	if err == nil {
		return &ParseError{Message: "this specification could not be read"}
	}

	message := err.Error()
	line, column := positionIn(message)
	return &ParseError{
		Message: "this specification is not valid: " + message,
		Line:    line,
		Column:  column,
	}
}

// positionIn digs a line and column out of a validation message.
//
// libopenapi reports positions inside the message text rather than as fields, and
// a user staring at a 4,000-line specification needs the number more than the
// prose around it.
func positionIn(message string) (line, column int) {
	for _, marker := range []string{"line ", "Line "} {
		index := strings.Index(message, marker)
		if index < 0 {
			continue
		}

		rest := message[index+len(marker):]
		digits := strings.TrimLeftFunc(rest, func(r rune) bool { return false })
		end := 0
		for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
			end++
		}
		if end > 0 {
			parsed, err := strconv.Atoi(digits[:end])
			if err == nil {
				return parsed, 0
			}
		}
	}
	return 0, 0
}

func sourceRef(line, column int) string {
	if line <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", line, column)
}

func parameters(source []*v3.Parameter) []Parameter {
	out := make([]Parameter, 0, len(source))

	for _, parameter := range source {
		if parameter == nil {
			continue
		}

		converted := Parameter{
			Name:        parameter.Name,
			In:          parameter.In,
			Description: parameter.Description,
		}
		if parameter.Required != nil {
			converted.Required = *parameter.Required
		}
		if parameter.Schema != nil {
			converted.Schema = flatten(parameter.Schema, 0)
		}
		out = append(out, converted)
	}
	return out
}

func requestBody(body *v3.RequestBody) Body {
	if body == nil || body.Content == nil {
		return Body{}
	}

	out := Body{}
	if body.Required != nil {
		out.Required = *body.Required
	}

	// JSON first where it exists: it is what the generated tests will send, and a
	// specification that also documents form encoding does not change that.
	for _, contentType := range []string{"application/json", ""} {
		for pair := body.Content.First(); pair != nil; pair = pair.Next() {
			if contentType != "" && pair.Key() != contentType {
				continue
			}
			out.ContentType = pair.Key()
			if media := pair.Value(); media != nil && media.Schema != nil {
				out.Schema = flatten(media.Schema, 0)
			}
			return out
		}
	}
	return out
}

func responses(source *v3.Responses) []Response {
	if source == nil || source.Codes == nil {
		return nil
	}

	out := make([]Response, 0, source.Codes.Len())
	for pair := source.Codes.First(); pair != nil; pair = pair.Next() {
		response := Response{Status: pair.Key()}
		if value := pair.Value(); value != nil {
			response.Description = value.Description
			if value.Content != nil {
				for media := value.Content.First(); media != nil; media = media.Next() {
					response.ContentType = media.Key()
					if body := media.Value(); body != nil && body.Schema != nil {
						response.Schema = flatten(body.Schema, 0)
					}
					break
				}
			}
		}
		out = append(out, response)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Status < out[j].Status })
	return out
}

// securitySchemes indexes the declared schemes so an operation's requirement can
// name what kind of authentication it actually is.
func securitySchemes(spec *v3.Document) map[string]*v3.SecurityScheme {
	schemes := map[string]*v3.SecurityScheme{}
	if spec.Components == nil || spec.Components.SecuritySchemes == nil {
		return schemes
	}

	for pair := spec.Components.SecuritySchemes.First(); pair != nil; pair = pair.Next() {
		schemes[pair.Key()] = pair.Value()
	}
	return schemes
}

func requirementsOf(
	source []*base.SecurityRequirement,
	schemes map[string]*v3.SecurityScheme,
) []SecurityRequirement {
	out := make([]SecurityRequirement, 0, len(source))

	for _, requirement := range source {
		if requirement == nil || requirement.Requirements == nil {
			continue
		}
		for pair := requirement.Requirements.First(); pair != nil; pair = pair.Next() {
			entry := SecurityRequirement{Scheme: pair.Key(), Scopes: pair.Value()}
			if scheme, declared := schemes[pair.Key()]; declared && scheme != nil {
				entry.Type = scheme.Type
				entry.In = scheme.In
			}
			out = append(out, entry)
		}
	}
	return out
}

// flatten reduces a schema to what a test designer needs, stopping at
// maxSchemaDepth so a circular reference terminates.
func flatten(proxy *base.SchemaProxy, depth int) Schema {
	if proxy == nil {
		return Schema{}
	}
	if depth >= maxSchemaDepth {
		// Truncated rather than followed. The reference name is kept so a reader
		// can tell this was a cycle and not an omission.
		return Schema{Ref: proxy.GetReference()}
	}

	schema, err := proxy.BuildSchema()
	if err != nil || schema == nil {
		return Schema{Ref: proxy.GetReference()}
	}

	out := Schema{
		Description: schema.Description,
		Format:      schema.Format,
		Pattern:     schema.Pattern,
		Required:    schema.Required,
		Ref:         proxy.GetReference(),
	}
	if len(schema.Type) > 0 {
		out.Type = schema.Type[0]
	}
	if schema.Nullable != nil {
		out.Nullable = *schema.Nullable
	}

	for _, value := range schema.Enum {
		if len(out.Enum) >= maxEnumValues {
			break
		}
		if value != nil {
			out.Enum = append(out.Enum, value.Value)
		}
	}

	out.Minimum = floatFrom(schema.Minimum)
	out.Maximum = floatFrom(schema.Maximum)
	out.MinLength = intFromInt64(schema.MinLength)
	out.MaxLength = intFromInt64(schema.MaxLength)

	if schema.Items != nil && schema.Items.IsA() {
		items := flatten(schema.Items.A, depth+1)
		out.Items = &items
	}

	if schema.Properties != nil {
		out.Properties = make(map[string]Schema, schema.Properties.Len())
		for pair := schema.Properties.First(); pair != nil; pair = pair.Next() {
			out.Properties[pair.Key()] = flatten(pair.Value(), depth+1)
		}
	}

	// A composed schema contributes its first branch rather than nothing: an
	// allOf of a base object and an extension is one shape to a caller, and
	// dropping it would hide every field the endpoint actually takes.
	if out.Type == "" && len(schema.AllOf) > 0 {
		merged := flatten(schema.AllOf[0], depth+1)
		if out.Properties == nil {
			out.Properties = merged.Properties
		}
		if out.Type == "" {
			out.Type = merged.Type
		}
	}

	return out
}

func floatFrom(value *float64) *float64 {
	if value == nil {
		return nil
	}
	converted := *value
	return &converted
}

func intFromInt64(value *int64) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}
