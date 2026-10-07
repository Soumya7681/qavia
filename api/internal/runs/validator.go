package runs

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// Static validation of generated code (BE-3.4).
//
// It lives here rather than in the generation package for one reason: this is where
// the runner is. Go cannot parse TypeScript or Python, the toolchain that can is
// baked into the image that will execute the suite, and the decisions about which
// image, which limits, and which runtime are already made in this package.
//
// The container gets no network at all for a validation pass. Nothing is executed —
// the entry point parses, type-checks, and scans for placeholder assertions — so
// there is nothing a target could be needed for, and a validation that could reach
// the internet would be a validation that could exfiltrate the code it was checking.

// Validator checks generated files inside a runner image.
type Validator struct {
	driver runner.Driver
	runs   *Service
}

func NewValidator(driver runner.Driver, service *Service) *Validator {
	return &Validator{driver: driver, runs: service}
}

// Validate runs one validation pass over a set of files, keyed by path.
//
// Every file goes into one container, because the toolchain start-up dominates: one
// pass over forty files costs what one pass over one file costs, and forty
// containers cost forty times as much. The result is keyed by file, so a caller can
// still act on them individually.
func (v *Validator) Validate(
	ctx context.Context,
	projectID uuid.UUID,
	framework testfiles.Framework,
	files map[string]string,
) (map[string][]runner.ValidationProblem, error) {
	if len(files) == 0 {
		return nil, nil
	}

	image, err := v.runs.ImageFor(ctx, projectID, framework)
	if err != nil {
		return nil, err
	}

	limits, err := v.runs.Limits(ctx, projectID)
	if err != nil {
		return nil, err
	}
	runtime, err := v.runs.Runtime(ctx)
	if err != nil {
		return nil, err
	}

	result, err := v.driver.Run(ctx, runner.Spec{
		// Not a run ID: this is not a run, and naming it like one would put it in the
		// sweeper's path alongside real executions with no run row behind it.
		RunID:     "validate-" + uuid.New().String(),
		Image:     image,
		Command:   []string{"qavia-run", "validate"},
		Workspace: files,
		Limits:    limits,
		Runtime:   runtime,

		// No allowlisted host, so the container gets no network interface at all.
		Egress:  runner.Egress{},
		Reports: []string{runner.ValidationPath},
	}, io.Discard)
	if err != nil {
		return nil, err
	}

	raw, found := result.Reports[runner.ValidationPath]
	if !found {
		// Distinguished from "the files are fine": a validation that did not report is
		// not a validation that passed.
		return nil, apierr.Internal(fmt.Errorf(
			"the validator produced no report (exit %d): %s", result.ExitCode, tail(result.Logs, 2<<10)))
	}

	report, err := runner.ParseValidationReport(raw)
	if err != nil {
		return nil, apierr.Internal(err)
	}

	byFile := make(map[string][]runner.ValidationProblem, len(report.Problems))
	for _, problem := range report.Problems {
		file := strings.TrimPrefix(problem.File, "./")
		if file == "" {
			// A finding the tool could not attribute to a file, such as an unresolved
			// dependency. It belongs to every file in the pass rather than to none.
			for path := range files {
				byFile[path] = append(byFile[path],
					runner.ValidationProblem{Message: problem.Message, Code: problem.Code})
			}
			continue
		}
		problem.File = file
		byFile[file] = append(byFile[file], problem)
	}

	return byFile, nil
}
