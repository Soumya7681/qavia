package analyses

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Evidence enforcement (BE-5.3).
//
// This is the part of the phase that decides whether an analysis is worth anything.
// A model asked to explain a failure will always produce an explanation; what it
// will not always do is point at the thing it read. So a reference has to name a
// place, and the place has to exist:
//
//   - a log line range has to be inside the log,
//   - a response field path has to resolve in the response body,
//   - a source location has to be inside the file, at a line the file has.
//
// A reference that fails validation is not dropped quietly. It fails the whole
// analysis, and the reason goes back into the retry, because an analysis half of
// whose citations are invented is not an analysis with a small error in it.

// EvidenceKind is what a reference points at.
type EvidenceKind string

const (
	// EvidenceLog cites a line range in the run's stored log.
	EvidenceLog EvidenceKind = "log"

	// EvidenceResponse cites a field path in a captured response body.
	EvidenceResponse EvidenceKind = "response"

	// EvidenceSource cites a file and line in the generated suite, or in a connected
	// repository once BE-6 exists.
	EvidenceSource EvidenceKind = "source"

	// EvidenceHistory cites the run history the platform itself computed, such as
	// "failed on three of the last five runs". It needs no external artifact: the
	// numbers are checked against the database instead.
	EvidenceHistory EvidenceKind = "history"
)

// Reference is one piece of evidence.
//
// A named type rather than a map, so the jsonb column has a shape the UI can render
// as links without parsing prose (BE-5.1.2, backend-standards.md 9).
type Reference struct {
	Kind EvidenceKind `json:"kind"`

	// Detail is what the reference says, in the words of the analysis. Short: it is a
	// label on a link, not the explanation.
	Detail string `json:"detail"`

	// Log and source references.
	File     string `json:"file,omitempty"`
	FromLine int    `json:"fromLine,omitempty"`
	ToLine   int    `json:"toLine,omitempty"`

	// Response references.
	Path string `json:"path,omitempty"`

	// Quote is the text the analysis claims is there. Checked against the artifact
	// when present, which is what catches a plausible citation of a line that says
	// something else.
	Quote string `json:"quote,omitempty"`
}

// Evidence is an analysis's full citation list.
type Evidence []Reference

// Encode renders evidence for the jsonb column.
func (e Evidence) Encode() (json.RawMessage, error) {
	if len(e) == 0 {
		return json.RawMessage(`[]`), nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("encode evidence: %w", err)
	}
	return raw, nil
}

// DecodeEvidence reads the column back.
func DecodeEvidence(raw json.RawMessage) (Evidence, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var evidence Evidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return nil, fmt.Errorf("decode evidence: %w", err)
	}
	return evidence, nil
}

// Artifacts is what the evidence is checked against.
//
// Supplied by the caller because it already has them: the analyse stage fetched the
// log to build the prompt, and re-fetching it here would double the object-store
// traffic for every analysis.
type Artifacts struct {
	// Log is the run's stored output, or the result's own log when there is one.
	Log string

	// Response is a captured response body, when the failure carried one.
	Response json.RawMessage

	// Sources are the suite files the analysis is allowed to cite, keyed by path.
	Sources map[string]string

	// HistoryLength is how many past results the platform actually has for this test,
	// so a history claim cannot cite more runs than exist.
	HistoryLength int
}

// ErrNoEvidence is the empty case, separated because it is the most common
// rejection and its retry message is different: the model cited nothing at all
// rather than citing something wrong.
var ErrNoEvidence = fmt.Errorf("the analysis cited no evidence")

// Validate checks every reference and returns the problems it found.
//
// It returns all of them rather than the first, because a retry that fixes one
// invented citation and keeps the other three has not been told enough.
func (e Evidence) Validate(artifacts Artifacts) []string {
	if len(e) == 0 {
		return []string{ErrNoEvidence.Error()}
	}

	logLines := 0
	if artifacts.Log != "" {
		logLines = strings.Count(artifacts.Log, "\n") + 1
	}

	var problems []string
	for index, reference := range e {
		label := fmt.Sprintf("evidence[%d]", index)

		switch reference.Kind {
		case EvidenceLog:
			problems = append(problems, validateLog(label, reference, artifacts.Log, logLines)...)
		case EvidenceResponse:
			problems = append(problems, validateResponse(label, reference, artifacts.Response)...)
		case EvidenceSource:
			problems = append(problems, validateSource(label, reference, artifacts.Sources)...)
		case EvidenceHistory:
			problems = append(problems, validateHistory(label, reference, artifacts.HistoryLength)...)
		default:
			problems = append(problems, fmt.Sprintf(
				"%s has kind %q, which is not one of log, response, source, or history",
				label, reference.Kind))
		}

		if strings.TrimSpace(reference.Detail) == "" {
			problems = append(problems, label+" has no detail, so it labels nothing")
		}
	}
	return problems
}

func validateLog(label string, reference Reference, log string, lines int) []string {
	var problems []string

	if lines == 0 {
		return []string{label + " cites the log, but this failure has no stored log"}
	}
	if reference.FromLine < 1 {
		problems = append(problems, label+" cites a log line below 1")
	}
	if reference.ToLine != 0 && reference.ToLine < reference.FromLine {
		problems = append(problems, label+" cites a log range that ends before it starts")
	}

	last := reference.ToLine
	if last == 0 {
		last = reference.FromLine
	}
	if last > lines {
		problems = append(problems, fmt.Sprintf(
			"%s cites log line %d, and the log has %d lines", label, last, lines))
		return problems
	}

	if reference.Quote != "" && reference.FromLine >= 1 {
		cited := strings.Join(strings.Split(log, "\n")[reference.FromLine-1:last], "\n")
		if !strings.Contains(cited, strings.TrimSpace(reference.Quote)) {
			problems = append(problems, fmt.Sprintf(
				"%s quotes text that is not on log lines %d to %d",
				label, reference.FromLine, last))
		}
	}

	return problems
}

func validateResponse(label string, reference Reference, body json.RawMessage) []string {
	if len(body) == 0 {
		return []string{label + " cites a response body, and this failure captured none"}
	}
	if strings.TrimSpace(reference.Path) == "" {
		return []string{label + " cites a response without naming a field path"}
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		// A non-JSON body can still be cited, but only as a whole: there is no path
		// to resolve inside it.
		if reference.Path != "$" {
			return []string{label + " cites a field path in a response that is not JSON"}
		}
		return nil
	}

	if _, found := resolvePath(decoded, reference.Path); !found {
		return []string{fmt.Sprintf(
			"%s cites response path %q, which the body does not contain", label, reference.Path)}
	}
	return nil
}

func validateSource(label string, reference Reference, sources map[string]string) []string {
	if reference.File == "" {
		return []string{label + " cites a source location with no file"}
	}

	content, known := sources[reference.File]
	if !known {
		return []string{fmt.Sprintf(
			"%s cites %q, which is not a file this analysis was given", label, reference.File)}
	}

	lines := strings.Count(content, "\n") + 1
	last := reference.ToLine
	if last == 0 {
		last = reference.FromLine
	}
	if reference.FromLine < 1 || last > lines {
		return []string{fmt.Sprintf(
			"%s cites %s line %d, and the file has %d lines",
			label, reference.File, last, lines)}
	}

	if reference.Quote != "" {
		cited := strings.Join(strings.Split(content, "\n")[reference.FromLine-1:last], "\n")
		if !strings.Contains(cited, strings.TrimSpace(reference.Quote)) {
			return []string{fmt.Sprintf(
				"%s quotes text that is not on %s lines %d to %d",
				label, reference.File, reference.FromLine, last)}
		}
	}
	return nil
}

func validateHistory(label string, reference Reference, available int) []string {
	if available == 0 {
		return []string{label + " cites run history, and this test has none"}
	}
	if reference.ToLine > available {
		// ToLine carries "how many runs" for a history reference, which is the one
		// place the field means something other than a line.
		return []string{fmt.Sprintf(
			"%s cites %d past runs, and the platform has %d", label, reference.ToLine, available)}
	}
	return nil
}

// resolvePath walks a dotted path with optional array indexes: `data.items[0].id`.
//
// Deliberately small. It exists to check that a cited field is really there, not to
// be a query language, and a path this cannot parse is reported as not found rather
// than accepted.
func resolvePath(document any, path string) (any, bool) {
	cleaned := strings.TrimPrefix(strings.TrimSpace(path), "$")
	cleaned = strings.TrimPrefix(cleaned, ".")
	if cleaned == "" {
		return document, true
	}

	current := document
	for _, segment := range strings.Split(cleaned, ".") {
		name, indexes := splitIndexes(segment)

		if name != "" {
			object, ok := current.(map[string]any)
			if !ok {
				return nil, false
			}
			value, found := object[name]
			if !found {
				return nil, false
			}
			current = value
		}

		for _, index := range indexes {
			list, ok := current.([]any)
			if !ok || index < 0 || index >= len(list) {
				return nil, false
			}
			current = list[index]
		}
	}
	return current, true
}

// splitIndexes pulls `items[0][2]` apart into a name and its indexes.
func splitIndexes(segment string) (string, []int) {
	name := segment
	var indexes []int

	for {
		open := strings.Index(name, "[")
		if open < 0 {
			break
		}
		close := strings.Index(name[open:], "]")
		if close < 0 {
			break
		}

		index := 0
		if _, err := fmt.Sscanf(name[open+1:open+close], "%d", &index); err != nil {
			// An unparsable index makes the whole path unresolvable, which is the
			// correct answer: it is not a place in this document.
			return name, []int{-1}
		}
		indexes = append(indexes, index)
		name = name[:open] + name[open+close+1:]
	}
	return name, indexes
}
