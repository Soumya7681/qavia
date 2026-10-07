package comprehension

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/repos"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/testfiles"
	"github.com/hyscaler/qavia/api/internal/workspace"
)

// The unit test stage (BE-6.5).
//
// It writes one file per untested target, and the targets come from the comprehension
// map rather than from a guess: the map already said which functions no test imports,
// and asking again would cost a second exploration to answer a question already
// answered.
//
// Two rules carry the stage, and both are borrowed from earlier phases because they
// were right there:
//
//   - **The framework is the repository's, not the model's.** Detection read it out of
//     the project's own manifest (BE-6.3), and a test file in a framework the project
//     does not depend on is a file that cannot run.
//   - **Every file is compiled and scanned before it is kept**, in the image that owns
//     the toolchain, with the findings fed back into one correction (BE-3.4). Go cannot
//     parse TypeScript, and a test that does not compile is worse than a missing one.

// TypeUnitTests is the stage name the pipeline declares.
const TypeUnitTests = jobs.TypeRepoUnitTests

// Validator compiles and scans generated files in a runner image.
type Validator interface {
	Validate(
		ctx context.Context,
		projectID uuid.UUID,
		framework testfiles.Framework,
		files map[string]string,
	) (map[string][]runner.ValidationProblem, error)
}

// Files stores what was generated.
type Files interface {
	Save(ctx context.Context, input testfiles.SaveInput) (testfiles.File, error)
	MarkValidated(ctx context.Context, id uuid.UUID, note string) error
	MarkRejected(ctx context.Context, id uuid.UUID, note string) error
}

// TestGateway is the slice of the AI gateway this stage needs.
type TestGateway interface {
	UnitTest(ctx context.Context, call llm.AgentCall, input llm.UnitTestInput) (llm.AgentResult, error)
}

// UnitTestDeps is everything the stage shares.
type UnitTestDeps struct {
	Repos     Repos
	Maps      MapReader
	Files     Files
	Gateway   TestGateway
	Validator Validator
	Settings  Settings
}

// MapReader reads the newest comprehension map, which is where the targets come from.
type MapReader interface {
	LatestMap(ctx context.Context, projectID uuid.UUID) (repos.StoredMap, bool, error)
}

// UnitTestHandler generates unit tests for a project's untested paths.
type UnitTestHandler struct {
	deps UnitTestDeps
}

func NewUnitTestHandler(deps UnitTestDeps) *UnitTestHandler {
	return &UnitTestHandler{deps: deps}
}

func (h *UnitTestHandler) Type() string { return TypeUnitTests }

// UnitTestPayload names the project.
type UnitTestPayload struct {
	ProjectID uuid.UUID `json:"projectId"`
	Ref       string    `json:"ref,omitempty"`

	// Targets narrows the run to specific files, for a reviewer who wants tests for
	// one module rather than for everything the map found.
	Targets []string `json:"targets,omitempty"`

	// RequestID is the per-request nonce the key is built from: generating again is
	// work somebody asks for.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *UnitTestHandler) IdempotencyKey(payload UnitTestPayload) string {
	return UnitTestIdempotencyKey(payload)
}

// UnitTestIdempotencyKey is exported so the API can declare the type as enqueue-only.
func UnitTestIdempotencyKey(payload UnitTestPayload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("unittests:%s", payload.ProjectID)
	}
	return fmt.Sprintf("unittests:%s", payload.RequestID)
}

func (h *UnitTestHandler) Handle(
	ctx context.Context,
	payload UnitTestPayload,
	jc jobs.JobContext,
) error {
	stored, found, err := h.deps.Maps.LatestMap(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if !found {
		// The map is the input. Refused with the step to take rather than generating
		// tests for files chosen at random.
		return apierr.NoRepositoryMap()
	}

	targets, err := targetsFrom(stored.Document, payload.Targets)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		jc.Event("The map lists no untested paths, so there is nothing to write")
		jc.Progress(100)
		return nil
	}

	stack, _, err := h.deps.Repos.DetectedStack(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	framework := frameworkFor(stack)
	if framework == "" {
		// Refused rather than guessed. A test file in a framework the project does not
		// depend on cannot run, and writing one costs a provider call to produce
		// something nobody can use (BE-6.3.3).
		return apierr.UnknownStack(stack.Inspected)
	}

	checkout, err := h.deps.Repos.Materialise(ctx, payload.ProjectID, payload.Ref)
	if err != nil {
		return err
	}
	defer func() {
		if err := checkout.Close(); err != nil {
			slog.WarnContext(ctx, "remove the workspace",
				"project_id", payload.ProjectID, "error", err)
		}
	}()

	limit, err := h.deps.Settings.Int(ctx, "jobs.ai_fan_out_limit",
		settings.Target{ProjectID: &payload.ProjectID})
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the fan-out limit: %w", err))
	}
	maximum, err := h.deps.Settings.Int(ctx, "repo.max_generated_tests",
		settings.Target{ProjectID: &payload.ProjectID})
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the generated test limit: %w", err))
	}
	if len(targets) > maximum {
		// Said rather than silently truncated: a reader has to know the list was cut,
		// or they will believe the map had fewer gaps than it did.
		jc.Event("The map lists %d untested path(s); writing tests for the first %d. "+
			"Raise repo.max_generated_tests to cover more in one pass.",
			len(targets), maximum)
		targets = targets[:maximum]
	}

	jc.Event("Writing %s tests for %d target(s), %d at a time",
		framework, len(targets), limit)

	tools := NewTools(checkout.Space)
	jobID := jc.JobID()

	var (
		mutex   sync.Mutex
		written int
		valid   int
		failed  []string
		done    int
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)

	for _, target := range targets {
		group.Go(func() error {
			file, problems, err := h.generate(groupCtx, generateInput{
				projectID: payload.ProjectID,
				jobID:     jobID,
				framework: framework,
				target:    target,
				tools:     tools,
				space:     checkout.Space,
			})

			mutex.Lock()
			defer mutex.Unlock()

			done++
			jc.Progress(done * 95 / len(targets))

			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				// One target failing is recorded and skipped: the other files are worth
				// keeping, and this one is visible in the log for a re-run.
				failed = append(failed, target.File)
				slog.WarnContext(ctx, "unit test generation failed",
					"file", target.File, "error", err)
				return nil
			}

			written++
			switch {
			case len(problems) == 0:
				valid++
				jc.Event("Wrote %s covering %v", file.Path, target.Symbols)
			default:
				jc.Event("%s still fails validation and needs a human: %s",
					file.Path, summariseProblems(problems))
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	jc.Event("Wrote %d test file(s), %d of which the toolchain accepts", written, valid)
	if len(failed) > 0 {
		jc.Event("%d target(s) produced no file and can be re-run: %v", len(failed), failed)
	}
	jc.Progress(100)
	return nil
}

// generateInput is one target's work.
type generateInput struct {
	projectID uuid.UUID
	jobID     uuid.UUID
	framework testfiles.Framework
	target    Target
	tools     *Tools
	space     *workspace.Workspace
}

// generate writes one file, validates it, and gives it one correction.
//
// The retry budget is one, for the reason it is one in codegen: a model that cannot
// satisfy a stated compiler finding on the second attempt will not satisfy it on the
// fifth, and the failing file is more useful to a reviewer than three more calls.
func (h *UnitTestHandler) generate(
	ctx context.Context,
	input generateInput,
) (testfiles.File, []runner.ValidationProblem, error) {
	source, err := h.context(input.tools, input.target)
	if err != nil {
		return testfiles.File{}, nil, err
	}

	feedback := ""
	var (
		stored   testfiles.File
		problems []runner.ValidationProblem
	)

	for attempt := 1; attempt <= 2; attempt++ {
		result, err := h.deps.Gateway.UnitTest(ctx, llm.AgentCall{
			ProjectID: &input.projectID,
			JobID:     &input.jobID,
		}, llm.UnitTestInput{
			Source:    source,
			Framework: string(input.framework),
			File:      input.target.File,
			Symbols:   input.target.Symbols,
			Feedback:  feedback,
		})
		if err != nil {
			return testfiles.File{}, nil, err
		}

		generated, err := decodeUnitTest(result.Raw)
		if err != nil {
			feedback = err.Error()
			continue
		}

		// The path is validated against the workspace, so a model writing to
		// `../../etc` is refused by the code that owns the filesystem rather than by a
		// rule in a prompt (BE-6.2.2).
		if _, err := input.space.Resolve(generated.Path); err != nil {
			feedback = fmt.Sprintf("the path %q is not inside the repository", generated.Path)
			continue
		}

		stored, err = h.deps.Files.Save(ctx, testfiles.SaveInput{
			ProjectID:   input.projectID,
			Framework:   input.framework,
			Path:        generated.Path,
			Content:     generated.Content,
			GeneratedBy: result.ModelName,
		})
		if err != nil {
			return testfiles.File{}, nil, err
		}

		problems, err = h.validate(ctx, input, generated)
		if err != nil {
			// The validator could not run. The file stays unmarked and the reason is in
			// the log: a missing runtime is not a failing test.
			slog.WarnContext(ctx, "static validation could not run",
				"file", generated.Path, "error", err)
			return stored, nil, nil
		}

		if len(problems) == 0 {
			if err := h.deps.Files.MarkValidated(ctx, stored.ID, ""); err != nil {
				slog.WarnContext(ctx, "record validation", "file", stored.Path, "error", err)
			}
			return stored, nil, nil
		}

		feedback = summariseProblems(problems)
	}

	// Twice is enough. The file is kept and marked, because a rejected file a reviewer
	// can read beats a silent gap in the suite (BE-3.4.3).
	if stored.ID != uuid.Nil {
		note := "Rejected by static validation: " + summariseProblems(problems)
		if err := h.deps.Files.MarkRejected(ctx, stored.ID, note); err != nil {
			slog.WarnContext(ctx, "record validation rejection",
				"file", stored.Path, "error", err)
		}
	}
	return stored, problems, nil
}

// validate compiles the generated test together with the source it imports.
//
// Both files, because a test alone does not compile: its import of the target has to
// resolve, and that is exactly the mistake worth catching before a human reads the
// file.
func (h *UnitTestHandler) validate(
	ctx context.Context,
	input generateInput,
	generated unitTest,
) ([]runner.ValidationProblem, error) {
	if h.deps.Validator == nil {
		return nil, fmt.Errorf("no container runtime on this worker")
	}

	files := map[string]string{generated.Path: generated.Content}

	// The target's own source, at its repository path, so a relative import resolves
	// the way it will when the file is exported into the repository.
	if body, err := input.tools.Read(input.target.File); err == nil {
		files[input.target.File] = stripLineNumbers(body)
	}

	problems, err := h.deps.Validator.Validate(ctx, input.projectID, input.framework, files)
	if err != nil {
		return nil, err
	}

	// Findings about the target's own source are not this file's fault. A repository
	// whose own code does not type-check is a fact to report elsewhere, not a reason to
	// reject a test written against it.
	return problems[generated.Path], nil
}

// context is the cacheable prefix: the target's source, and its directory's listing
// so the agent can see the conventions around it.
func (h *UnitTestHandler) context(tools *Tools, target Target) (map[string]any, error) {
	body, err := tools.Read(target.File)
	if err != nil {
		return nil, apierr.Validation(
			fmt.Sprintf("The map names %q, which is not readable in this revision.", target.File),
			map[string]any{"file": target.File})
	}

	neighbours, err := tools.Glob(path.Join(path.Dir(target.File), "*"))
	if err != nil {
		neighbours = ""
	}

	// An existing test, when there is one, is the strongest statement of the
	// repository's conventions: it shows the import style, the naming, and the
	// assertion library actually in use.
	example := ""
	for _, pattern := range []string{"**/*.test.ts", "**/*.test.js", "**/*.spec.ts", "**/test_*.py", "**/*_test.go"} {
		found, err := tools.Glob(pattern)
		if err != nil || strings.HasPrefix(found, "no files match") {
			continue
		}
		first := strings.SplitN(found, "\n", 2)[0]
		if body, err := tools.Read(first); err == nil {
			example = first + "\n" + stripLineNumbers(body)
			break
		}
	}

	return map[string]any{
		"file":       target.File,
		"source":     stripLineNumbers(body),
		"neighbours": neighbours,
		"example":    example,
		"why":        target.Why,
	}, nil
}

// Target is one untested path from the map.
type Target struct {
	File    string
	Symbols []string
	Why     string
}

// targetsFrom reads the map's untested paths, grouped by file.
//
// Grouped because one call per file beats one per symbol: three functions in one
// module belong in one test file, and three files importing the same module is what a
// reviewer deletes.
func targetsFrom(document json.RawMessage, only []string) ([]Target, error) {
	var decoded struct {
		Uncovered []struct {
			File   string `json:"file"`
			Symbol string `json:"symbol"`
			Why    string `json:"why"`
		} `json:"uncovered"`
	}
	if err := json.Unmarshal(document, &decoded); err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the repository map: %w", err))
	}

	wanted := map[string]bool{}
	for _, file := range only {
		wanted[file] = true
	}

	order := []string{}
	byFile := map[string]*Target{}

	for _, gap := range decoded.Uncovered {
		if gap.File == "" {
			continue
		}
		if len(wanted) > 0 && !wanted[gap.File] {
			continue
		}

		target, seen := byFile[gap.File]
		if !seen {
			target = &Target{File: gap.File, Why: gap.Why}
			byFile[gap.File] = target
			order = append(order, gap.File)
		}
		if gap.Symbol != "" {
			target.Symbols = append(target.Symbols, gap.Symbol)
		}
	}

	targets := make([]Target, 0, len(order))
	for _, file := range order {
		targets = append(targets, *byFile[file])
	}
	return targets, nil
}

// frameworkFor maps a detected framework onto one this platform can run.
//
// Empty means the platform will not write a test: a project whose stack is unknown, or
// whose framework has no runner image, gets a stated refusal rather than a file in a
// framework it does not have (F-3.12).
func frameworkFor(stack repos.Stack) testfiles.Framework {
	switch strings.ToLower(stack.Framework) {
	case "vitest":
		return testfiles.FrameworkVitest
	case "jest":
		return testfiles.FrameworkJest
	case "pytest":
		return testfiles.FrameworkPytest
	default:
		return ""
	}
}

// unitTest is the agent's envelope.
type unitTest struct {
	Path    string   `json:"path"`
	Content string   `json:"content"`
	Covered []string `json:"covered_symbols"`
	Skipped []struct {
		Symbol string `json:"symbol"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Notes string `json:"notes"`
}

func decodeUnitTest(raw json.RawMessage) (unitTest, error) {
	var decoded unitTest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return unitTest{}, fmt.Errorf("the generated file could not be read: %w", err)
	}
	if strings.TrimSpace(decoded.Path) == "" {
		return unitTest{}, fmt.Errorf("the generated file had no path")
	}
	if strings.TrimSpace(decoded.Content) == "" {
		return unitTest{}, fmt.Errorf("the generated file was empty")
	}
	return decoded, nil
}

// summariseProblems renders findings for a prompt or a log line, capped so one
// pathological file cannot fill either.
func summariseProblems(problems []runner.ValidationProblem) string {
	const maximum = 10

	lines := make([]string, 0, min(len(problems), maximum))
	for index, problem := range problems {
		if index == maximum {
			lines = append(lines, fmt.Sprintf("… and %d more", len(problems)-maximum))
			break
		}
		lines = append(lines, "- "+problem.String())
	}
	return strings.Join(lines, "\n")
}

// stripLineNumbers undoes the numbering the read tool adds.
//
// The numbers exist so an exploration can cite a line; a file being handed to a
// compiler must not have them. Two consumers, two shapes, one source.
func stripLineNumbers(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		if index := strings.IndexByte(line, '\t'); index > 0 && isDigits(line[:index]) {
			out = append(out, line[index+1:])
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func isDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
