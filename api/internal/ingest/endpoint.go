// Package ingest turns an uploaded specification into the normalized endpoint
// model everything downstream works from.
//
// All of it is deterministic code, never a model (ai-architecture.md 2). Parsing
// JSON is not a judgement call, and a model asked to do it would cost money, take
// seconds, and be wrong occasionally rather than never. The model's job starts
// afterwards, on the normalized result.
package ingest

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// Format is what an artifact turned out to be.
type Format string

const (
	FormatOpenAPI Format = "openapi"
	FormatPostman Format = "postman"
	FormatText    Format = "requirement_text"
)

// Endpoint is one operation, in a shape that does not remember which format it
// came from. OpenAPI and Postman both normalize into this, so everything
// downstream is format-agnostic (BE-2.3).
type Endpoint struct {
	Method string
	Path   string

	OperationID string
	Summary     string
	Description string

	Parameters []Parameter
	Request    Body
	Responses  []Response
	Security   []SecurityRequirement

	// SourceRef locates this in the uploaded file, as "line:column". It is what
	// makes a generated test case a citation rather than a claim (BE-2.2).
	SourceRef string
}

// Key is the identity of an endpoint within a specification.
func (e Endpoint) Key() string { return e.Method + " " + e.Path }

// Parameter is a path, query, header, or cookie parameter.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`

	// Schema is the JSON Schema fragment, flattened to what a test author needs:
	// type, format, enum, and the bounds that produce boundary cases.
	Schema Schema `json:"schema"`

	Example string `json:"example,omitempty"`
}

// Body is a request body.
type Body struct {
	Required    bool   `json:"required"`
	ContentType string `json:"contentType,omitempty"`
	Schema      Schema `json:"schema"`
	Example     string `json:"example,omitempty"`
}

// Response is one documented response.
type Response struct {
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Schema      Schema `json:"schema"`
}

// SecurityRequirement names a scheme the operation requires.
type SecurityRequirement struct {
	Scheme string   `json:"scheme"`
	Type   string   `json:"type,omitempty"`
	In     string   `json:"in,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
}

// Schema is the flattened shape of a value.
//
// Deliberately not the whole of JSON Schema. What a test designer needs is the
// type, the constraints that produce boundary cases, and the required fields; a
// faithful reproduction of every keyword would be a larger prompt for no extra
// tests. Circular references stop at Depth, which is why this can be a tree at
// all.
type Schema struct {
	Type   string `json:"type,omitempty"`
	Format string `json:"format,omitempty"`

	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`

	// Bounds. Pointers, because zero is a meaningful minimum and "absent" has to
	// be distinguishable from it: a boundary case at 0 is different from no bound.
	Minimum   *float64 `json:"minimum,omitempty"`
	Maximum   *float64 `json:"maximum,omitempty"`
	MinLength *int     `json:"minLength,omitempty"`
	MaxLength *int     `json:"maxLength,omitempty"`
	Pattern   string   `json:"pattern,omitempty"`

	Nullable bool `json:"nullable,omitempty"`

	Required   []string          `json:"required,omitempty"`
	Properties map[string]Schema `json:"properties,omitempty"`
	Items      *Schema           `json:"items,omitempty"`

	// Ref names what was referenced, kept when the tree was truncated so a reader
	// can tell a cycle from a missing definition.
	Ref string `json:"ref,omitempty"`
}

// Document is a parsed specification.
type Document struct {
	Format Format

	Title       string
	Version     string
	Description string

	Servers   []string
	Endpoints []Endpoint

	// Text is the raw content for a pasted requirement, which has no structure to
	// normalize and feeds the extract agent directly (BE-2.4).
	Text string

	// Warnings are things worth telling a user that did not stop the parse: an
	// operation with no responses, a scheme that was referenced but not defined.
	Warnings []string
}

// Fingerprint identifies an endpoint for deduplication.
//
// Normalized before hashing: method uppercased, path with its parameter names
// removed, so /users/{id} and /users/{userId} are one endpoint rather than two.
// Two specifications that describe the same API produce the same fingerprints,
// which is what makes a re-upload free.
func Fingerprint(method, path, assertion string) []byte {
	sum := sha256.Sum256([]byte(strings.ToUpper(method) + " " +
		NormalizePath(path) + " " + strings.ToLower(strings.TrimSpace(assertion))))
	return sum[:]
}

// NormalizePath replaces every path parameter with a placeholder.
func NormalizePath(path string) string {
	var (
		out    strings.Builder
		inside bool
	)

	for _, r := range path {
		switch {
		case r == '{':
			inside = true
			out.WriteString("{}")
		case r == '}':
			inside = false
		case !inside:
			out.WriteRune(r)
		}
	}

	normalized := out.String()
	if normalized == "" {
		return "/"
	}
	if !strings.HasPrefix(normalized, "/") {
		normalized = "/" + normalized
	}
	return strings.TrimSuffix(normalized, "/")
}

// SortEndpoints puts a document's endpoints in a stable order.
//
// Stability is not cosmetic here: the endpoint list is part of the cached prompt
// prefix, and a map-ordered list would change between runs and silently destroy
// prompt caching (ai-architecture.md 3.6).
func SortEndpoints(endpoints []Endpoint) {
	sort.Slice(endpoints, func(i, j int) bool {
		if endpoints[i].Path != endpoints[j].Path {
			return endpoints[i].Path < endpoints[j].Path
		}
		return endpoints[i].Method < endpoints[j].Method
	})
}

// ParseError names where a specification failed, with the line if there is one.
//
// A generic "invalid JSON" tells a user nothing they can act on; a line number
// tells them where to look (BE-2.2).
type ParseError struct {
	Message string
	Line    int
	Column  int
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s (line %d, column %d)", e.Message, e.Line, e.Column)
	}
	return e.Message
}
