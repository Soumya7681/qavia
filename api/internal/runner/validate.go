package runner

import (
	"encoding/json"
	"fmt"
)

// The static-validation contract (BE-3.4).
//
// A runner image has two entry points: `qavia-run test` executes a suite, and
// `qavia-run validate` parses and type-checks it without running anything. The
// second is here because Go cannot parse TypeScript or Python, and the toolchain
// that can is already baked into the image that will run the suite
// (backend-standards.md 10).
//
// The shape is fixed so the Go side never branches on framework:
//
//	{
//	  "schema": "qavia.validate/1",
//	  "files": ["tests/tickets.get.test.ts"],
//	  "problems": [{"file": "...", "line": 12, "code": "QAVIA_PLACEHOLDER",
//	                "message": "asserts expect(true).toBe(true), which cannot fail"}]
//	}

// ValidationPath is where every image writes its validation report, relative to the
// workspace. The same file the run report uses: a container has one output channel
// and the mode is what decides the shape.
const ValidationPath = ".qavia/report.json"

// ValidationReport is the static-validation shape.
type ValidationReport struct {
	Schema   string              `json:"schema"`
	Files    []string            `json:"files"`
	Problems []ValidationProblem `json:"problems"`
}

// ValidationProblem is one compile, parse, or placeholder finding, with the position
// when the tool gave one.
type ValidationProblem struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}

// String renders a finding for a prompt or a log line.
func (p ValidationProblem) String() string {
	switch {
	case p.Line > 0:
		return fmt.Sprintf("%s:%d %s", p.File, p.Line, p.Message)
	case p.File != "":
		return fmt.Sprintf("%s %s", p.File, p.Message)
	default:
		return p.Message
	}
}

// ParseValidationReport decodes the static-validation shape.
func ParseValidationReport(raw []byte) (ValidationReport, error) {
	var report ValidationReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return ValidationReport{}, fmt.Errorf("the validation report could not be read: %w", err)
	}
	if report.Schema != "qavia.validate/1" {
		return ValidationReport{}, fmt.Errorf(
			"the runner reported schema %q, and this platform reads qavia.validate/1", report.Schema)
	}
	return report, nil
}
