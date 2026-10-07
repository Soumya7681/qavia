package comprehension

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/workspace"
)

// The exploration loop (BE-6.4).
//
// The agent chooses one action per step and this loop executes it. What the loop
// enforces, rather than trusting to a prompt:
//
//   - **A budget.** Iterations come from the model's own declared
//     `max_tool_iterations`, and running out is a normal ending: the map is written
//     from what was seen, marked as cut short, rather than the pass failing (BE-6.4.4).
//   - **Refusals are results.** A path outside the workspace comes back to the agent
//     as the tool's output, so it learns and moves on. Failing the pass over it would
//     mean one bad guess costs the whole map.
//   - **No repeats.** The same action twice in a row is answered from the first
//     result, because a model that reads the same file three times is spending a
//     client's money on nothing.

// Budget bounds one exploration.
type Budget struct {
	// Steps is the maximum number of agent calls. Zero uses the default.
	Steps int
}

const defaultSteps = 24

// Map is what an exploration produced.
type Map struct {
	Summary     string       `json:"summary"`
	Controllers []Controller `json:"controllers"`
	Services    []Component  `json:"services"`
	DataAccess  []DataStore  `json:"dataAccess"`
	Uncovered   []Uncovered  `json:"uncovered"`
	Unknowns    []string     `json:"unknowns"`

	// Steps is how many agent calls it took, and CutShort says whether the budget ran
	// out. Both are recorded, because a map produced in twenty-four steps of an
	// exploration that wanted thirty is a different thing from one the agent finished.
	Steps    int  `json:"steps"`
	CutShort bool `json:"cutShort"`

	// Commit is the revision this map describes. A map with no revision is a map
	// nobody can tell is stale.
	Commit string `json:"commit"`

	// Trail is what the agent looked at, in order. It is the audit trail of an
	// exploration: a map nobody can retrace is a map nobody can check.
	Trail []Step `json:"trail"`
}

// Controller is an entry point.
type Controller struct {
	File   string   `json:"file"`
	Name   string   `json:"name"`
	Routes []string `json:"routes"`
	Calls  []string `json:"calls"`
}

// Component is a service or module with a stated responsibility.
type Component struct {
	File           string `json:"file"`
	Name           string `json:"name"`
	Responsibility string `json:"responsibility"`
}

// DataStore is a data-access site and the tables it touches.
type DataStore struct {
	File   string   `json:"file"`
	Name   string   `json:"name"`
	Tables []string `json:"tables"`
}

// Uncovered is a path with no test, named as a symbol rather than as a percentage:
// the number is the coverage tool's job, and this is the list of things worth writing
// a test for (BE-6.5).
type Uncovered struct {
	File   string `json:"file"`
	Symbol string `json:"symbol"`
	Why    string `json:"why"`
}

// Step is one action and its outcome, kept for the trail.
type Step struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`

	// Refused is true when the platform said no: a path outside the workspace, or a
	// file that is not text.
	Refused bool `json:"refused,omitempty"`
}

// Gateway is the slice of the AI gateway this package needs.
type Gateway interface {
	RepoStep(ctx context.Context, call llm.AgentCall, input llm.RepoStepInput) (llm.AgentResult, error)
}

// Explorer runs the loop.
type Explorer struct {
	gateway Gateway
}

func NewExplorer(gateway Gateway) *Explorer { return &Explorer{gateway: gateway} }

// Progress reports a step to whoever is watching the job.
type Progress func(step int, action, detail string)

// Explore maps a checkout.
func (e *Explorer) Explore(
	ctx context.Context,
	space *workspace.Workspace,
	call llm.AgentCall,
	stack string,
	commit string,
	budget Budget,
	report Progress,
) (Map, error) {
	steps := budget.Steps
	if steps <= 0 {
		steps = defaultSteps
	}

	tools := NewTools(space)
	tree, err := tools.Tree()
	if err != nil {
		return Map{}, apierr.Internal(err)
	}

	var (
		history []map[string]any
		trail   []Step
		seen    = map[string]string{}
	)

	for step := 1; step <= steps; step++ {
		result, err := e.gateway.RepoStep(ctx, call, llm.RepoStepInput{
			Tree:    tree,
			Stack:   stack,
			Step:    step,
			Budget:  steps,
			History: history,
		})
		if err != nil {
			return Map{}, err
		}

		decision, err := decodeStep(result.Raw)
		if err != nil {
			// A step that does not parse is recorded and retried within the budget: the
			// alternative is losing the whole exploration to one malformed reply.
			history = append(history, map[string]any{
				"action": "error",
				"detail": "",
				"result": err.Error(),
			})
			continue
		}

		if decision.Action == "answer" {
			produced := decision.toMap()
			produced.Steps = step
			produced.Commit = commit
			produced.Trail = trail
			return produced, nil
		}

		action, detail := decision.describe()
		if report != nil {
			report(step, action, detail)
		}

		key := action + " " + detail
		output, refused := "", false

		if cached, repeated := seen[key]; repeated {
			// The same action twice. Answered from the first result and said so, because
			// a model reading the same file three times is spending money on nothing.
			output = cached + "\n(unchanged: this was already read)"
		} else {
			output, refused = e.execute(tools, decision)
			seen[key] = output
		}

		trail = append(trail, Step{
			Action:  action,
			Detail:  detail,
			Reason:  decision.Reason,
			Refused: refused,
		})
		history = append(history, map[string]any{
			"action": action,
			"detail": detail,
			"result": output,
		})
	}

	// The budget ran out. That is an ending rather than a failure: one more call asks
	// for the map from what was seen, and a map marked cut short is honest about it
	// (BE-6.4.4).
	final, err := e.finish(ctx, call, tree, stack, steps, history)
	if err != nil {
		return Map{}, err
	}
	final.Steps = steps
	final.CutShort = true
	final.Commit = commit
	final.Trail = trail
	return final, nil
}

// finish asks for the map once the budget is spent.
func (e *Explorer) finish(
	ctx context.Context,
	call llm.AgentCall,
	tree map[string]any,
	stack string,
	steps int,
	history []map[string]any,
) (Map, error) {
	history = append(history, map[string]any{
		"action": "budget",
		"detail": "",
		"result": "The step budget is spent. Answer now with the map from what you have seen, " +
			"and put what you did not get to in unknowns.",
	})

	result, err := e.gateway.RepoStep(ctx, call, llm.RepoStepInput{
		Tree:    tree,
		Stack:   stack,
		Step:    steps,
		Budget:  steps,
		History: history,
	})
	if err != nil {
		return Map{}, err
	}

	decision, err := decodeStep(result.Raw)
	if err != nil {
		return Map{}, apierr.Internal(
			fmt.Errorf("the exploration produced no readable map: %w", err))
	}
	return decision.toMap(), nil
}

// execute runs one tool and reports whether the platform refused it.
func (e *Explorer) execute(tools *Tools, decision step) (string, bool) {
	var (
		output string
		err    error
	)

	switch decision.Action {
	case "read":
		output, err = tools.Read(decision.Path)
	case "grep":
		output, err = tools.Grep(decision.Pattern, decision.Include)
	case "glob":
		output, err = tools.Glob(decision.Glob)
	default:
		return fmt.Sprintf("%q is not an action this platform offers. "+
			"Use read, grep, glob, or answer.", decision.Action), true
	}

	if err != nil {
		// Returned as the tool's result rather than as a failure. A refused path is
		// something for the agent to learn from, and the budget already charges it a
		// step.
		return "refused: " + err.Error(), true
	}
	return output, false
}

// step is the agent's reply: one action, or the finished map.
type step struct {
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Include string `json:"include"`
	Glob    string `json:"glob"`

	Map *struct {
		Summary     string `json:"summary"`
		Controllers []struct {
			File   string   `json:"file"`
			Name   string   `json:"name"`
			Routes []string `json:"routes"`
			Calls  []string `json:"calls"`
		} `json:"controllers"`
		Services []struct {
			File           string `json:"file"`
			Name           string `json:"name"`
			Responsibility string `json:"responsibility"`
		} `json:"services"`
		DataAccess []struct {
			File   string   `json:"file"`
			Name   string   `json:"name"`
			Tables []string `json:"tables"`
		} `json:"data_access"`
		Uncovered []struct {
			File   string `json:"file"`
			Symbol string `json:"symbol"`
			Why    string `json:"why"`
		} `json:"uncovered"`
		Unknowns []string `json:"unknowns"`
	} `json:"map"`
}

func decodeStep(raw json.RawMessage) (step, error) {
	var decoded step
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return step{}, fmt.Errorf("the step could not be read: %w", err)
	}
	if strings.TrimSpace(decoded.Action) == "" {
		return step{}, fmt.Errorf("the step named no action")
	}
	if decoded.Action == "answer" && decoded.Map == nil {
		return step{}, fmt.Errorf("the step answered with no map")
	}
	return decoded, nil
}

// describe names the action for the trail and the job log.
func (s step) describe() (string, string) {
	switch s.Action {
	case "read":
		return "read", s.Path
	case "grep":
		detail := s.Pattern
		if s.Include != "" {
			detail += " in " + s.Include
		}
		return "grep", detail
	case "glob":
		return "glob", s.Glob
	default:
		return s.Action, ""
	}
}

func (s step) toMap() Map {
	if s.Map == nil {
		return Map{}
	}

	produced := Map{
		Summary:  s.Map.Summary,
		Unknowns: s.Map.Unknowns,
	}
	for _, controller := range s.Map.Controllers {
		produced.Controllers = append(produced.Controllers, Controller{
			File: controller.File, Name: controller.Name,
			Routes: controller.Routes, Calls: controller.Calls,
		})
	}
	for _, service := range s.Map.Services {
		produced.Services = append(produced.Services, Component{
			File: service.File, Name: service.Name, Responsibility: service.Responsibility,
		})
	}
	for _, store := range s.Map.DataAccess {
		produced.DataAccess = append(produced.DataAccess, DataStore{
			File: store.File, Name: store.Name, Tables: store.Tables,
		})
	}
	for _, gap := range s.Map.Uncovered {
		produced.Uncovered = append(produced.Uncovered, Uncovered{
			File: gap.File, Symbol: gap.Symbol, Why: gap.Why,
		})
	}
	return produced
}

// Verify drops entries naming files the repository does not have.
//
// The agent is asked not to invent paths, and this is what makes the request
// enforceable rather than a request. A map listing a plausible file that does not
// exist is worse than a shorter map, because somebody writes a test against it: the
// same reasoning as evidence validation in the analysis phase (BE-5.3).
func (m *Map) Verify(space *workspace.Workspace) []string {
	var dropped []string

	keep := func(file string) bool {
		if file == "" {
			return false
		}
		handle, err := space.Open(file)
		if err != nil {
			dropped = append(dropped, file)
			return false
		}
		_ = handle.Close() //nolint:errcheck // read-only
		return true
	}

	controllers := m.Controllers[:0]
	for _, controller := range m.Controllers {
		if keep(controller.File) {
			controllers = append(controllers, controller)
		}
	}
	m.Controllers = controllers

	services := m.Services[:0]
	for _, service := range m.Services {
		if keep(service.File) {
			services = append(services, service)
		}
	}
	m.Services = services

	stores := m.DataAccess[:0]
	for _, store := range m.DataAccess {
		if keep(store.File) {
			stores = append(stores, store)
		}
	}
	m.DataAccess = stores

	gaps := m.Uncovered[:0]
	for _, gap := range m.Uncovered {
		if keep(gap.File) {
			gaps = append(gaps, gap)
		}
	}
	m.Uncovered = gaps

	if len(dropped) > 0 {
		// Recorded in the map itself, so a reader sees that the exploration named files
		// which are not there.
		m.Unknowns = append(m.Unknowns,
			fmt.Sprintf("%d named file(s) do not exist in this revision and were dropped: %s",
				len(dropped), strings.Join(dropped, ", ")))
	}
	return dropped
}

// Encode renders the map for its jsonb column.
func (m Map) Encode() (json.RawMessage, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode the repository map: %w", err)
	}
	return raw, nil
}

// callFor builds the agent call for a project's exploration.
func CallFor(projectID, jobID uuid.UUID) llm.AgentCall {
	return llm.AgentCall{ProjectID: &projectID, JobID: &jobID}
}
