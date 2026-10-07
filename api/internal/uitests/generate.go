package uitests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// The Playwright generation stage (BE-7.4).
//
// One spec per discovered flow, and the flows come from a graph that was walked in a
// real browser rather than from a router's declarations: a spec for a route nobody
// opened fails on its first navigation, which is the failure mode this whole phase
// exists to avoid (BE-7.2).
//
// Two gates stand between a generated file and the suite, in this order:
//
//  1. **The selector policy**, a pure function of the file's text. A spec containing a
//     positional selector is rejected and regenerated, not merged with a warning: the
//     one spec in twenty that locates a button by its position breaks the first time
//     somebody adds a wrapper div, and it breaks in a way that looks like a real
//     failure (selectors.go, BE-7.4.2).
//  2. **Static validation**, in the image that owns the toolchain, because Go cannot
//     parse TypeScript and a spec that does not compile is worse than a missing one
//     (BE-3.4).
//
// The policy runs first because it is free: no container, no network, same answer
// every time. Sending a file to a container to be compiled and only then discovering
// it used `nth-child` would spend a runner slot to learn something a regular
// expression knew.

// TypeGenerate is the stage name the pipeline declares.
const TypeGenerate = jobs.TypeUIGenerate

// Graphs reads the graph the specs are generated from.
type Graphs interface {
	Latest(ctx context.Context, projectID uuid.UUID) (Stored, bool, error)
	Get(ctx context.Context, id uuid.UUID) (Stored, error)
}

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

// SpecGateway is the slice of the AI gateway this stage needs.
type SpecGateway interface {
	UISpec(ctx context.Context, call llm.AgentCall, input llm.UISpecInput) (llm.AgentResult, error)
}

// GenerateDeps is everything the stage shares.
type GenerateDeps struct {
	Graphs    Graphs
	Files     Files
	Gateway   SpecGateway
	Validator Validator
	Settings  Settings
}

// GenerateHandler writes a Playwright spec per discovered flow.
type GenerateHandler struct {
	deps GenerateDeps
}

func NewGenerateHandler(deps GenerateDeps) *GenerateHandler {
	return &GenerateHandler{deps: deps}
}

func (h *GenerateHandler) Type() string { return TypeGenerate }

// GeneratePayload names what to generate from.
type GeneratePayload struct {
	ProjectID uuid.UUID `json:"projectId"`

	// FlowID generates from one particular graph rather than the newest. A reviewer
	// who read a graph and approved it means that graph, not whatever a discovery
	// produced since.
	FlowID *uuid.UUID `json:"flowId,omitempty"`

	// Flows narrows the run to named journeys, for somebody who wants a spec for the
	// checkout flow rather than for everything the discovery found.
	Flows []string `json:"flows,omitempty"`

	// RequestID is the per-request nonce the key is built from: generating again is
	// work somebody asks for.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *GenerateHandler) IdempotencyKey(payload GeneratePayload) string {
	return GenerateIdempotencyKey(payload)
}

// GenerateIdempotencyKey is exported so the API can declare the type as enqueue-only
// without building a handler it cannot run.
func GenerateIdempotencyKey(payload GeneratePayload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("ui.generate:%s", payload.ProjectID)
	}
	return fmt.Sprintf("ui.generate:%s", payload.RequestID)
}

func (h *GenerateHandler) Handle(
	ctx context.Context,
	payload GeneratePayload,
	jc jobs.JobContext,
) error {
	stored, err := h.graph(ctx, payload)
	if err != nil {
		return err
	}

	graph, err := stored.Decode()
	if err != nil {
		return err
	}

	flows := selected(graph.Flows, payload.Flows)
	if len(flows) == 0 {
		// Not a failure. A graph with no walked flows is a discovery that found nothing
		// worth a test, and generating from it would produce nothing either.
		jc.Event("The graph has no flows to generate from")
		jc.Progress(100)
		return nil
	}

	if stored.ReviewedAt == nil {
		// Said rather than refused. Blocking would mean an installation that wants the
		// whole path automated cannot have it; saying it means nobody discovers later
		// that a suite came from a graph nobody read (BE-7.2.3).
		jc.Event("Generating from a graph nobody has reviewed yet (%s)", stored.ID)
	}

	limit, err := h.deps.Settings.Int(ctx, "jobs.ai_fan_out_limit", settings.Target{})
	if err != nil {
		return apierr.Internal(fmt.Errorf("read the fan-out limit: %w", err))
	}
	if limit < 1 {
		limit = 1
	}

	jc.Event("Writing %d Playwright spec(s) from graph %s", len(flows), stored.ID)

	prefix := prefixFor(graph)
	jobID := jc.JobID()

	var (
		mutex    sync.Mutex
		done     int
		accepted int
		rejected []string
		failed   []string
	)

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(limit)

	for _, flow := range flows {
		group.Go(func() error {
			outcome, err := h.generate(groupCtx, specInput{
				projectID: payload.ProjectID,
				jobID:     jobID,
				graph:     prefix,
				flow:      flow,
			})

			mutex.Lock()
			defer mutex.Unlock()

			done++
			jc.Progress(done * 95 / len(flows))

			if err != nil {
				if groupCtx.Err() != nil || retryable(err) {
					// The AI service being unreachable is not this flow's fault, and every
					// other flow will hit it too. Returned so the stage retries rather than
					// reporting a suite it never wrote as merely incomplete.
					return err
				}
				// One flow failing is recorded and skipped: the other specs are worth
				// keeping, and this one is visible in the log for a re-run.
				failed = append(failed, flow.Name)
				slog.WarnContext(ctx, "ui spec generation failed",
					"flow", flow.Name, "error", err)
				return nil
			}

			switch {
			case outcome.acceptable():
				accepted++
				jc.Event("Wrote %s for %q", outcome.file.Path, flow.Name)
			case len(outcome.violations) > 0:
				rejected = append(rejected, flow.Name)
				jc.Event("%s still breaks the selector policy and needs a human: %s",
					outcome.file.Path, Summarise(outcome.violations))
			default:
				rejected = append(rejected, flow.Name)
				jc.Event("%s still fails validation and needs a human: %s",
					outcome.file.Path, summariseProblems(outcome.problems))
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}

	jc.Event("Wrote %d spec(s), %d of which the selector policy and the toolchain both accept",
		len(flows)-len(failed), accepted)
	if len(rejected) > 0 {
		jc.Event("%d spec(s) were kept but marked for review: %v", len(rejected), rejected)
	}
	if len(failed) > 0 {
		jc.Event("%d flow(s) produced no file and can be re-run: %v", len(failed), failed)
	}

	jc.Progress(100)
	return nil
}

// graph resolves which graph to generate from.
func (h *GenerateHandler) graph(
	ctx context.Context,
	payload GeneratePayload,
) (Stored, error) {
	if payload.FlowID != nil {
		stored, err := h.deps.Graphs.Get(ctx, *payload.FlowID)
		if err != nil {
			return Stored{}, err
		}
		if stored.ProjectID != payload.ProjectID {
			// A graph from another project is not this project's graph, and the two are
			// deliberately indistinguishable from "does not exist".
			return Stored{}, apierr.FlowGraphNotFound()
		}
		return stored, nil
	}

	stored, found, err := h.deps.Graphs.Latest(ctx, payload.ProjectID)
	if err != nil {
		return Stored{}, err
	}
	if !found {
		return Stored{}, apierr.NoFlowGraph()
	}
	return stored, nil
}

// specInput is one flow's work.
type specInput struct {
	projectID uuid.UUID
	jobID     uuid.UUID

	graph map[string]any
	flow  Flow
}

// outcome is what one flow produced.
type outcome struct {
	file       testfiles.File
	violations []Violation
	problems   []runner.ValidationProblem
}

func (o outcome) acceptable() bool {
	return len(o.violations) == 0 && len(o.problems) == 0
}

// generate writes one spec, checks it, and gives it one correction.
//
// The retry budget is one, for the reason it is one everywhere else in this platform:
// a model that cannot satisfy a stated finding on the second attempt will not satisfy
// it on the fifth, and the failing file is more useful to a reviewer than three more
// calls (BE-3.4.3).
func (h *GenerateHandler) generate(ctx context.Context, input specInput) (outcome, error) {
	feedback := ""
	var result outcome

	for attempt := 1; attempt <= 2; attempt++ {
		answer, err := h.deps.Gateway.UISpec(ctx, llm.AgentCall{
			ProjectID: &input.projectID,
			JobID:     &input.jobID,
		}, llm.UISpecInput{
			Graph:    input.graph,
			Flow:     flowAsMap(input.flow),
			Feedback: feedback,
		})
		if err != nil {
			return outcome{}, err
		}

		generated, err := decodeSpec(answer.Raw)
		if err != nil {
			feedback = err.Error()
			continue
		}

		path, err := specPath(generated.Path, input.flow.Name)
		if err != nil {
			feedback = err.Error()
			continue
		}

		// The policy first, before anything is stored or a container is started: it is a
		// pure function of the text, so a violation costs one regeneration rather than a
		// runner slot as well (BE-7.4.3).
		if violations := Check(generated.Content); len(violations) > 0 {
			result.violations = violations
			feedback = "The selector policy rejected these locators. Rewrite them using " +
				"getByTestId, or getByRole with the accessible name:\n" + Summarise(violations)

			if attempt == 1 {
				// Not stored yet. A spec that broke the policy on the first attempt is not
				// something a reviewer needs to see if the second attempt is clean.
				continue
			}
		} else {
			result.violations = nil
		}

		stored, err := h.deps.Files.Save(ctx, testfiles.SaveInput{
			ProjectID:   input.projectID,
			Framework:   testfiles.FrameworkPlaywright,
			Path:        path,
			Content:     generated.Content,
			GeneratedBy: answer.ModelName,
		})
		if err != nil {
			return outcome{}, err
		}
		result.file = stored

		if len(result.violations) > 0 {
			// Kept and marked, because a rejected file a reviewer can read beats a silent
			// gap in the suite.
			h.reject(ctx, stored, "Rejected by the selector policy: "+Summarise(result.violations))
			return result, nil
		}

		problems, err := h.validate(ctx, input.projectID, path, generated.Content)
		if err != nil {
			// The validator could not run. The file stays unmarked and the reason is in
			// the log: a missing runtime is not a failing spec.
			slog.WarnContext(ctx, "static validation could not run",
				"file", path, "error", err)
			return result, nil
		}

		result.problems = problems
		if len(problems) == 0 {
			if err := h.deps.Files.MarkValidated(ctx, stored.ID, ""); err != nil {
				slog.WarnContext(ctx, "record validation", "file", path, "error", err)
			}
			return result, nil
		}

		feedback = summariseProblems(problems)
	}

	if result.file.ID != uuid.Nil && len(result.problems) > 0 {
		h.reject(ctx, result.file, "Rejected by static validation: "+summariseProblems(result.problems))
	}
	return result, nil
}

func (h *GenerateHandler) reject(ctx context.Context, file testfiles.File, note string) {
	if err := h.deps.Files.MarkRejected(ctx, file.ID, note); err != nil {
		slog.WarnContext(ctx, "record a rejection", "file", file.Path, "error", err)
	}
}

// validate compiles the spec in the image that owns the toolchain.
//
// One file, unlike a unit test: a Playwright spec imports the framework rather than
// the application's own source, so there is nothing else to put beside it.
func (h *GenerateHandler) validate(
	ctx context.Context,
	projectID uuid.UUID,
	path, content string,
) ([]runner.ValidationProblem, error) {
	if h.deps.Validator == nil {
		return nil, fmt.Errorf("no container runtime on this worker")
	}

	problems, err := h.deps.Validator.Validate(ctx, projectID,
		testfiles.FrameworkPlaywright, map[string]string{path: content})
	if err != nil {
		return nil, err
	}
	return problems[path], nil
}

// prefixFor is the cacheable half of the prompt: the application, its pages, and what
// can be done on each. Identical for every flow of one graph, which is what makes a
// suite of twelve specs one cached prefix rather than twelve full prompts.
func prefixFor(graph Graph) map[string]any {
	pages := make([]map[string]any, 0, len(graph.Pages))
	for _, page := range graph.Pages {
		actions := make([]map[string]any, 0, len(page.Actions))
		for _, action := range page.Actions {
			actions = append(actions, map[string]any{
				"description": action.Description,
				"testid":      action.TestID,
				"role":        action.Role,
				"name":        action.Name,
				"label":       action.Label,
				"leads_to":    action.LeadsTo,
			})
		}
		pages = append(pages, map[string]any{
			"path":          page.Path,
			"title":         page.Title,
			"purpose":       page.Purpose,
			"requires_auth": page.RequiresAuth,
			"actions":       actions,
		})
	}

	return map[string]any{
		"target":    graph.Target,
		"auth":      graph.AuthMode,
		"summary":   graph.Summary,
		"pages":     pages,
		"selectors": Ranked(),
	}
}

// flowAsMap is one flow in the shape the agent's schema names its fields.
func flowAsMap(flow Flow) map[string]any {
	steps := make([]map[string]any, 0, len(flow.Steps))
	for _, step := range flow.Steps {
		steps = append(steps, map[string]any{
			"action": step.Action,
			"target": step.Target,
			"testid": step.TestID,
			"role":   step.Role,
			"name":   step.Name,
			"label":  step.Label,
			"value":  step.Value,
			"expect": step.Expect,
		})
	}

	return map[string]any{
		"name":          flow.Name,
		"purpose":       flow.Purpose,
		"requires_auth": flow.RequiresAuth,
		"steps":         steps,
	}
}

// selected narrows the flows to the ones asked for, by name.
func selected(flows []Flow, names []string) []Flow {
	if len(names) == 0 {
		return flows
	}

	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[strings.ToLower(strings.TrimSpace(name))] = true
	}

	chosen := make([]Flow, 0, len(names))
	for _, flow := range flows {
		if wanted[strings.ToLower(strings.TrimSpace(flow.Name))] {
			chosen = append(chosen, flow)
		}
	}
	return chosen
}

// spec is what the agent returned.
type spec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Notes   string `json:"notes"`
}

func decodeSpec(raw json.RawMessage) (spec, error) {
	var decoded spec
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return spec{}, fmt.Errorf("the generated spec could not be read: %w", err)
	}
	if strings.TrimSpace(decoded.Content) == "" {
		return spec{}, fmt.Errorf("the generated spec was empty")
	}
	return decoded, nil
}

// specPath keeps generated files inside the suite directory, under a name derived from
// the flow when the agent's path is unusable.
//
// Checked here rather than trusted: a model wrote it, and a path is exactly the field
// where a plausible-looking answer does damage. The file store checks again, because
// two checks on a path written by a model is the right number (backend-standards.md 13).
func specPath(proposed, flowName string) (string, error) {
	cleaned := strings.TrimSpace(strings.ReplaceAll(proposed, "\\", "/"))
	cleaned = strings.TrimPrefix(cleaned, "./")

	if cleaned == "" || strings.Contains(cleaned, "..") || strings.HasPrefix(cleaned, "/") {
		fallback := slug(flowName)
		if fallback == "" {
			return "", fmt.Errorf("the spec named no usable path and its flow has no name")
		}
		return "tests/ui/" + fallback + ".spec.ts", nil
	}

	if !strings.HasSuffix(cleaned, ".spec.ts") && !strings.HasSuffix(cleaned, ".spec.js") {
		// The runner image collects specs by that suffix, so a file named otherwise is a
		// file the suite never runs.
		cleaned = strings.TrimSuffix(cleaned, ".ts") + ".spec.ts"
	}
	return cleaned, nil
}

// slug turns a flow's name into a filename.
func slug(name string) string {
	lowered := strings.ToLower(strings.TrimSpace(name))
	replaced := nonFilename.ReplaceAllString(lowered, "-")
	trimmed := strings.Trim(replaced, "-")

	if len(trimmed) > 60 {
		trimmed = trimmed[:60]
	}
	return trimmed
}

var nonFilename = regexp.MustCompile(`[^a-z0-9]+`)

// retryable reports whether a failure is about the platform rather than about this
// flow: an unreachable AI service, a provider outage, a spend ceiling.
func retryable(err error) bool {
	var domain *apierr.Error
	if !errors.As(err, &domain) {
		return false
	}

	switch domain.Code {
	case apierr.CodeServiceDegraded, apierr.CodeProviderUnavailable:
		return true
	default:
		return false
	}
}

// summariseProblems renders validation findings for a prompt or a job log.
func summariseProblems(problems []runner.ValidationProblem) string {
	const maximum = 8

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
