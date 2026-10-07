package testfiles

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/testcases"
)

// Postman export (F-6.2).
//
// Deterministic code, not an agent. A collection is a JSON document with a known
// shape, and the approved cases already say what to call and what to assert:
// paying a model to reformat that would be slower, cost money, and occasionally
// produce a collection Postman refuses to import.

const postmanSchema = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

// Variables rather than literals, so one exported collection runs against staging
// and production by switching an environment (F-6.2).
const (
	baseURLVariable = "baseUrl"
	authVariable    = "authToken"
)

type postmanExport struct {
	Info     postmanInfo       `json:"info"`
	Item     []postmanEntry    `json:"item"`
	Variable []postmanVariable `json:"variable"`
	Auth     *postmanAuthBlock `json:"auth,omitempty"`
}

type postmanInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      string `json:"schema"`
}

type postmanEntry struct {
	Name    string         `json:"name"`
	Item    []postmanEntry `json:"item,omitempty"`
	Request *postmanReq    `json:"request,omitempty"`
	Event   []postmanEvent `json:"event,omitempty"`
}

type postmanReq struct {
	Method      string           `json:"method"`
	Header      []postmanHeader  `json:"header"`
	URL         postmanURLExport `json:"url"`
	Body        *postmanBody     `json:"body,omitempty"`
	Description string           `json:"description,omitempty"`
}

type postmanHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type postmanURLExport struct {
	Raw  string   `json:"raw"`
	Host []string `json:"host"`
	Path []string `json:"path"`
}

type postmanBody struct {
	Mode string `json:"mode"`
	Raw  string `json:"raw"`
}

type postmanEvent struct {
	Listen string        `json:"listen"`
	Script postmanScript `json:"script"`
}

type postmanScript struct {
	Type string   `json:"type"`
	Exec []string `json:"exec"`
}

type postmanVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type postmanAuthBlock struct {
	Type   string            `json:"type"`
	Bearer []postmanVariable `json:"bearer,omitempty"`
}

// BuildPostman renders approved cases as a v2.1 collection.
//
// One request per case, grouped into a folder per endpoint, with the assertions as
// a test script. The script is what makes the collection a test suite rather than a
// list of requests somebody has to check by eye.
func BuildPostman(projectName string, cases []testcases.ApprovedCase) ([]byte, []uuid.UUID, error) {
	if len(cases) == 0 {
		return nil, nil, fmt.Errorf("no approved cases to export")
	}

	collection := postmanExport{
		Info: postmanInfo{
			Name: projectName + " (Qavia)",
			Description: "Generated from approved test cases. " +
				"Set " + baseURLVariable + " and " + authVariable + " in an environment; " +
				"nothing here is hardcoded.",
			Schema: postmanSchema,
		},
		Variable: []postmanVariable{
			{Key: baseURLVariable, Value: "https://staging.example.test", Type: "string"},
			{Key: authVariable, Value: "", Type: "string"},
		},
		Auth: &postmanAuthBlock{
			Type:   "bearer",
			Bearer: []postmanVariable{{Key: "token", Value: "{{" + authVariable + "}}", Type: "string"}},
		},
	}

	covered := make([]uuid.UUID, 0, len(cases))
	folders := map[string]int{}

	for _, item := range cases {
		endpoint := strings.TrimSpace(item.Method + " " + item.Path)
		if endpoint == "" {
			endpoint = "General"
		}

		index, exists := folders[endpoint]
		if !exists {
			collection.Item = append(collection.Item, postmanEntry{Name: endpoint})
			index = len(collection.Item) - 1
			folders[endpoint] = index
		}

		collection.Item[index].Item = append(collection.Item[index].Item, requestFor(item))
		covered = append(covered, item.ID)
	}

	encoded, err := json.MarshalIndent(collection, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode postman collection: %w", err)
	}
	return encoded, covered, nil
}

func requestFor(item testcases.ApprovedCase) postmanEntry {
	method := item.Method
	if method == "" {
		method = "GET"
	}

	route := item.Path
	if route == "" {
		route = "/"
	}

	segments := make([]string, 0, 4)
	for _, segment := range strings.Split(strings.Trim(route, "/"), "/") {
		if segment != "" {
			segments = append(segments, segment)
		}
	}

	entry := postmanEntry{
		Name: item.Title,
		Request: &postmanReq{
			Method:      strings.ToUpper(method),
			Header:      []postmanHeader{{Key: "Content-Type", Value: "application/json", Type: "text"}},
			Description: description(item),
			URL: postmanURLExport{
				Raw:  "{{" + baseURLVariable + "}}" + route,
				Host: []string{"{{" + baseURLVariable + "}}"},
				Path: segments,
			},
		},
		Event: []postmanEvent{{
			Listen: "test",
			Script: postmanScript{Type: "text/javascript", Exec: assertions(item)},
		}},
	}

	if body := requestBody(item); body != "" {
		entry.Request.Body = &postmanBody{Mode: "raw", Raw: body}
	}
	return entry
}

// description carries the case's provenance into the collection, so somebody
// reading it in Postman can trace a request back to a case and a requirement
// (F-6.11).
func description(item testcases.ApprovedCase) string {
	lines := []string{
		"Qavia test case " + item.ID.String(),
		"Category: " + string(item.Category) + ", priority: " + string(item.Priority),
	}
	if item.Preconditions != "" {
		lines = append(lines, "Preconditions: "+item.Preconditions)
	}
	if item.Expected != "" {
		lines = append(lines, "Expected: "+item.Expected)
	}
	return strings.Join(lines, "\n")
}

// assertions turn the expected result into a script.
//
// A status code is extracted where the case names one, because that is the
// assertion worth having automatically. Everything else stays a documented
// expectation rather than an invented check: a wrong assertion is worse than an
// absent one, since it fails a suite for the wrong reason.
func assertions(item testcases.ApprovedCase) []string {
	lines := []string{
		"// Generated from Qavia test case " + item.ID.String(),
	}

	if status := statusIn(item.Expected); status != "" {
		lines = append(lines,
			fmt.Sprintf("pm.test(%q, function () {", item.Title),
			fmt.Sprintf("    pm.response.to.have.status(%s);", status),
			"});")
	} else {
		lines = append(lines,
			fmt.Sprintf("pm.test(%q, function () {", item.Title),
			"    // Expected: "+strings.ReplaceAll(item.Expected, "\n", " "),
			"    pm.expect(pm.response.code).to.be.below(500);",
			"});")
	}
	return lines
}

// statusIn finds a three-digit HTTP status in the expected text.
func statusIn(expected string) string {
	for i := 0; i+3 <= len(expected); i++ {
		chunk := expected[i : i+3]
		if chunk[0] < '1' || chunk[0] > '5' {
			continue
		}
		if !isDigit(chunk[1]) || !isDigit(chunk[2]) {
			continue
		}
		// Bounded by a non-digit on both sides, so "1000" does not read as "100".
		if i > 0 && isDigit(expected[i-1]) {
			continue
		}
		if i+3 < len(expected) && isDigit(expected[i+3]) {
			continue
		}
		return chunk
	}
	return ""
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// requestBody uses the case's own data where a step supplied some, rather than
// inventing a payload the specification never described.
func requestBody(item testcases.ApprovedCase) string {
	for _, step := range item.Steps {
		data := strings.TrimSpace(step.Data)
		if strings.HasPrefix(data, "{") || strings.HasPrefix(data, "[") {
			return data
		}
	}
	return ""
}
