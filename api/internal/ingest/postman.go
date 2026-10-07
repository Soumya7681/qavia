package ingest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Postman collection import (F-3.2).
//
// Hand-decoded rather than taken from a library: the format is stable, only a
// subset matters, and the one thing that needs care is variable substitution,
// which every library leaves as raw {{placeholder}} strings anyway. That would
// produce test cases asserting against a literal "{{baseUrl}}".

// postmanCollection is the subset of the schema worth decoding.
type postmanCollection struct {
	Info struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schema      string `json:"schema"`
	} `json:"info"`

	Item     []postmanItem     `json:"item"`
	Variable []postmanVariable `json:"variable"`
	Auth     *postmanAuth      `json:"auth"`
}

type postmanVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// postmanItem is a request or a folder of them. Folders nest arbitrarily, which
// is why this is walked rather than iterated.
type postmanItem struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Item        []postmanItem   `json:"item"`
	Request     *postmanRequest `json:"request"`
	Response    []struct {
		Name string `json:"name"`
		Code int    `json:"code"`
	} `json:"response"`
}

type postmanRequest struct {
	Method      string          `json:"method"`
	Description string          `json:"description"`
	URL         json.RawMessage `json:"url"`
	Auth        *postmanAuth    `json:"auth"`
	Header      []struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Disabled bool   `json:"disabled"`
	} `json:"header"`
	Body *struct {
		Mode string `json:"mode"`
		Raw  string `json:"raw"`
	} `json:"body"`
}

type postmanAuth struct {
	Type string `json:"type"`
}

// postmanURL is the object form. The format also allows a plain string, which is
// why the field is decoded twice.
type postmanURL struct {
	Raw   string   `json:"raw"`
	Host  []string `json:"host"`
	Path  []string `json:"path"`
	Query []struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Description string `json:"description"`
		Disabled    bool   `json:"disabled"`
	} `json:"query"`
	Variable []struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Description string `json:"description"`
	} `json:"variable"`
}

// ParsePostman normalizes a collection into the same endpoint model OpenAPI
// produces, so everything downstream stays format-agnostic (BE-2.3).
func ParsePostman(raw []byte) (Document, error) {
	var collection postmanCollection
	if err := json.Unmarshal(raw, &collection); err != nil {
		return Document{}, &ParseError{Message: "this file is not a readable Postman collection"}
	}
	if collection.Info.Name == "" && len(collection.Item) == 0 {
		return Document{}, &ParseError{Message: "this file has no collection name and no requests"}
	}

	// Collection variables are substituted rather than left as placeholders. A
	// test asserting against "{{baseUrl}}/tickets" is a test nobody can run.
	variables := make(map[string]string, len(collection.Variable))
	for _, variable := range collection.Variable {
		variables[variable.Key] = variable.Value
	}

	out := Document{
		Format:      FormatPostman,
		Title:       collection.Info.Name,
		Description: collection.Info.Description,
	}
	if base, defined := variables["baseUrl"]; defined && base != "" {
		out.Servers = append(out.Servers, substitute(base, variables))
	}

	walkPostman(collection.Item, collection.Auth, variables, "", &out)

	if len(out.Endpoints) == 0 {
		return out, &ParseError{Message: "this collection contains no requests"}
	}
	SortEndpoints(out.Endpoints)
	return out, nil
}

// walkPostman descends folders, carrying the folder path so a request keeps the
// grouping its author gave it.
func walkPostman(
	items []postmanItem,
	inherited *postmanAuth,
	variables map[string]string,
	folder string,
	out *Document,
) {
	for _, item := range items {
		name := strings.TrimSpace(item.Name)

		if len(item.Item) > 0 {
			next := name
			if folder != "" && name != "" {
				next = folder + " / " + name
			}
			walkPostman(item.Item, inherited, variables, next, out)
			continue
		}

		if item.Request == nil {
			continue
		}

		endpoint, err := postmanEndpoint(item, inherited, variables, folder)
		if err != nil {
			out.Warnings = append(out.Warnings, err.Error())
			continue
		}
		out.Endpoints = append(out.Endpoints, endpoint)
	}
}

func postmanEndpoint(
	item postmanItem,
	inherited *postmanAuth,
	variables map[string]string,
	folder string,
) (Endpoint, error) {
	request := item.Request

	path, query, err := postmanPath(request.URL, variables)
	if err != nil {
		return Endpoint{}, fmt.Errorf("request %q: %w", item.Name, err)
	}

	endpoint := Endpoint{
		Method:      strings.ToUpper(request.Method),
		Path:        path,
		OperationID: item.Name,
		Summary:     item.Name,
		Description: strings.TrimSpace(request.Description + "\n" + item.Description),
		// A collection has no line numbers to cite, so the folder path is the
		// closest honest answer to "where did this come from".
		SourceRef: folder,
	}
	if endpoint.Method == "" {
		endpoint.Method = "GET"
	}

	endpoint.Parameters = query
	for _, header := range request.Header {
		if header.Disabled || isBoringHeader(header.Key) {
			continue
		}
		endpoint.Parameters = append(endpoint.Parameters, Parameter{
			Name:   header.Key,
			In:     "header",
			Schema: Schema{Type: "string"},
		})
	}

	if request.Body != nil && strings.TrimSpace(request.Body.Raw) != "" {
		endpoint.Request = Body{
			Required:    true,
			ContentType: "application/json",
			Example:     substitute(request.Body.Raw, variables),
			// A collection carries an example rather than a schema. Inferring the
			// shape from the example is better than claiming no shape at all: it is
			// what a test author would read anyway.
			Schema: inferSchema(substitute(request.Body.Raw, variables)),
		}
	}

	for _, response := range item.Response {
		endpoint.Responses = append(endpoint.Responses, Response{
			Status:      fmt.Sprintf("%d", response.Code),
			Description: response.Name,
		})
	}

	auth := request.Auth
	if auth == nil {
		auth = inherited
	}
	if auth != nil && auth.Type != "" && auth.Type != "noauth" {
		endpoint.Security = []SecurityRequirement{{Scheme: auth.Type, Type: auth.Type}}
	}

	return endpoint, nil
}

// postmanPath extracts the path and query parameters, substituting variables.
func postmanPath(raw json.RawMessage, variables map[string]string) (string, []Parameter, error) {
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("has no URL")
	}

	// The string form first: older collections use it, and it is unambiguous.
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return pathOf(substitute(asString, variables)), nil, nil
	}

	var structured postmanURL
	if err := json.Unmarshal(raw, &structured); err != nil {
		return "", nil, fmt.Errorf("has an unreadable URL")
	}

	path := ""
	if len(structured.Path) > 0 {
		segments := make([]string, 0, len(structured.Path))
		for _, segment := range structured.Path {
			segments = append(segments, postmanSegment(segment, variables))
		}
		path = "/" + strings.Join(segments, "/")
	} else if structured.Raw != "" {
		path = pathOf(substitute(structured.Raw, variables))
	}
	if path == "" {
		return "", nil, fmt.Errorf("has no path")
	}

	parameters := make([]Parameter, 0, len(structured.Query)+len(structured.Variable))
	for _, variable := range structured.Variable {
		parameters = append(parameters, Parameter{
			Name:        variable.Key,
			In:          "path",
			Required:    true,
			Description: variable.Description,
			Schema:      Schema{Type: "string"},
			Example:     substitute(variable.Value, variables),
		})
	}
	for _, query := range structured.Query {
		if query.Disabled {
			continue
		}
		parameters = append(parameters, Parameter{
			Name:        query.Key,
			In:          "query",
			Description: query.Description,
			Schema:      Schema{Type: "string"},
			Example:     substitute(query.Value, variables),
		})
	}

	return path, parameters, nil
}

// postmanSegment turns :id into {id}, so a collection and a specification produce
// the same path and therefore the same fingerprint.
func postmanSegment(segment string, variables map[string]string) string {
	segment = substitute(segment, variables)
	if strings.HasPrefix(segment, ":") {
		return "{" + strings.TrimPrefix(segment, ":") + "}"
	}
	return segment
}

func pathOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path == "" {
		// Not a URL, or a bare path with a placeholder host that failed to parse.
		trimmed := raw
		if index := strings.Index(trimmed, "://"); index >= 0 {
			trimmed = trimmed[index+3:]
			if slash := strings.Index(trimmed, "/"); slash >= 0 {
				trimmed = trimmed[slash:]
			}
		}
		if question := strings.Index(trimmed, "?"); question >= 0 {
			trimmed = trimmed[:question]
		}
		if !strings.HasPrefix(trimmed, "/") {
			trimmed = "/" + trimmed
		}
		return trimmed
	}
	return parsed.Path
}

// substitute replaces {{variable}} references with their values.
//
// An unknown variable is left as it is rather than blanked: "{{authToken}}" in the
// output tells a reader something is unresolved, while an empty string looks like
// a value that was deliberately empty.
func substitute(text string, variables map[string]string) string {
	if !strings.Contains(text, "{{") {
		return text
	}

	for name, value := range variables {
		text = strings.ReplaceAll(text, "{{"+name+"}}", value)
	}
	return text
}

// inferSchema reads the shape of an example body.
func inferSchema(example string) Schema {
	var decoded any
	if err := json.Unmarshal([]byte(example), &decoded); err != nil {
		return Schema{Type: "string"}
	}
	return schemaOf(decoded, 0)
}

func schemaOf(value any, depth int) Schema {
	if depth >= maxSchemaDepth {
		return Schema{}
	}

	switch typed := value.(type) {
	case map[string]any:
		schema := Schema{Type: "object", Properties: map[string]Schema{}}
		for key, field := range typed {
			schema.Properties[key] = schemaOf(field, depth+1)
		}
		return schema
	case []any:
		schema := Schema{Type: "array"}
		if len(typed) > 0 {
			items := schemaOf(typed[0], depth+1)
			schema.Items = &items
		}
		return schema
	case bool:
		return Schema{Type: "boolean"}
	case float64:
		return Schema{Type: "number"}
	case nil:
		return Schema{Nullable: true}
	default:
		return Schema{Type: "string"}
	}
}

// isBoringHeader skips the headers every request carries, which describe HTTP
// rather than the endpoint.
func isBoringHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "content-type", "accept", "user-agent", "content-length", "host", "connection":
		return true
	default:
		return false
	}
}
