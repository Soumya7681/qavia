package uitests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/hyscaler/qavia/api/internal/capability/browserdriver"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// The discovery loop (BE-7.2).
//
// The agent chooses one browser action per step and this loop performs it. Driving a
// real browser is the requirement rather than an implementation taste: source tells
// you which routes a router declares, and it does not tell you that three of them
// redirect to a login, that one renders only for an admin, or that the fourth's form
// fails silently without a tenant header. Runtime behaviour and conditional rendering
// are exactly what a generated suite has to know (BE-7.2.2).
//
// What the loop enforces, rather than trusting to a prompt:
//
//   - **A budget**, held here and again by the session, so discovery terminates
//     whatever the agent decides. Running out is a normal ending: the graph is written
//     from what was seen and marked cut short (BE-7.2.4).
//   - **Destructive actions are refused**, in code. "Do not click delete" in a prompt
//     is a preference; this is somebody's staging environment, and a refusal that
//     comes back as the action's result teaches the agent without costing the pass.
//   - **Refusals are results.** An action outside the vocabulary, a click on something
//     that moved, a navigation off the target: all come back as observations. Failing
//     the discovery over one bad guess would mean one dead link costs the whole graph.
//   - **No repeats.** The same action twice is answered from the first observation,
//     because a model reloading one page four times is spending money on nothing.
//   - **Credentials never appear in the record.** The browser substitutes them and
//     redacts its own replies; this loop scrubs what it writes down as well, because
//     the trail is stored and the trace is downloadable (BE-7.3.3).

// Budget bounds one discovery.
type Budget struct {
	// Steps is the maximum number of agent calls. Zero uses the default.
	Steps int
}

const defaultSteps = 40

// Graph is what a discovery produced: the application's navigable structure.
type Graph struct {
	Summary  string   `json:"summary"`
	Pages    []Page   `json:"pages"`
	Flows    []Flow   `json:"flows"`
	Unknowns []string `json:"unknowns"`

	// Unreachable is what was seen in a link and never opened. Kept because it is the
	// difference between "this application has no admin page" and "the admin page
	// refused us", and only one of those is worth a person's attention.
	Unreachable []string `json:"unreachable"`

	// Steps is how many agent calls it took, and CutShort says whether the budget ran
	// out: a graph produced in forty steps of a discovery that wanted sixty is a
	// different thing from one the agent chose to finish.
	Steps    int  `json:"steps"`
	CutShort bool `json:"cutShort"`

	// Target is what was explored. A graph with no target is a graph nobody can tell
	// is stale, the same reason a repository map records its commit.
	Target string `json:"target"`

	// AuthMode is how the discovery authenticated, so a graph with no authenticated
	// pages can be read as "none configured" rather than "none exist".
	AuthMode string `json:"authMode"`

	// Model is what produced the graph. Recorded because two graphs of one application
	// that disagree are usually two models, and without this nobody can tell.
	Model string `json:"model,omitempty"`

	// Trail is every action and what it showed, in order: the audit trail of a
	// discovery. A graph nobody can retrace is a graph nobody can check (BE-7.2.3).
	Trail []Step `json:"trail"`
}

// Page is one screen and what a user can do on it.
type Page struct {
	Path         string       `json:"path"`
	Title        string       `json:"title"`
	Purpose      string       `json:"purpose"`
	RequiresAuth bool         `json:"requiresAuth"`
	Actions      []PageAction `json:"actions"`
}

// PageAction is one thing a user can do, named the way the selector policy prefers so
// a spec can be written from it without inventing a locator (BE-7.4.2).
type PageAction struct {
	Description string `json:"description"`
	TestID      string `json:"testid,omitempty"`
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"`
	Label       string `json:"label,omitempty"`
	LeadsTo     string `json:"leadsTo,omitempty"`
}

// Flow is a journey worth a test.
type Flow struct {
	Name         string     `json:"name"`
	Purpose      string     `json:"purpose"`
	RequiresAuth bool       `json:"requiresAuth"`
	Steps        []FlowStep `json:"steps"`
}

// FlowStep is one step of a journey, with what should be true afterwards. The
// expectation is part of the graph rather than invented at generation time: a step
// with nothing to assert is a step that passes when the page is blank.
type FlowStep struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	TestID string `json:"testid,omitempty"`
	Role   string `json:"role,omitempty"`
	Name   string `json:"name,omitempty"`
	Label  string `json:"label,omitempty"`
	Value  string `json:"value,omitempty"`
	Expect string `json:"expect"`
}

// Step is one action and its outcome, kept for the trail.
type Step struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
	Reason string `json:"reason"`

	// URL is where the browser ended up, which is the one fact a reader always wants.
	URL string `json:"url,omitempty"`

	// Refused is true when the platform said no: an action outside the vocabulary, or
	// one that looked destructive.
	Refused bool `json:"refused,omitempty"`
}

// Gateway is the slice of the AI gateway this package needs.
type Gateway interface {
	UIFlowStep(ctx context.Context, call llm.AgentCall, input llm.UIFlowInput) (llm.AgentResult, error)
}

// Discoverer runs the loop.
type Discoverer struct {
	gateway Gateway
}

func NewDiscoverer(gateway Gateway) *Discoverer { return &Discoverer{gateway: gateway} }

// Progress reports a step to whoever is watching the job.
type Progress func(step int, action, detail string)

// Input is one discovery.
type Input struct {
	Target   string
	AuthMode string

	// LoginPath is where the sign-in form lives, when the project named it. Optional:
	// a discovery finds a login by following links, and naming it saves the actions
	// that takes.
	LoginPath string

	// Secrets are the values that must not appear in the trail: the configured
	// credential and password. Scrubbed here as well as in the container, because
	// defence at one layer is a single point of failure for a class of leak that is
	// permanent once it reaches storage (BE-7.3.3).
	Secrets []string

	Budget Budget
}

// Discover walks an application and records what it found.
//
// The session belongs to the caller, which opened it and will close it: artifacts are
// collected from a session that is still alive, and this loop has no business
// deciding when that ends.
func (d *Discoverer) Discover(
	ctx context.Context,
	session browserdriver.Session,
	call llm.AgentCall,
	input Input,
	report Progress,
) (Graph, error) {
	steps := input.Budget.Steps
	if steps <= 0 {
		steps = defaultSteps
	}

	prefix := d.context(input)

	var (
		history []map[string]any
		trail   []Step
		seen    = map[string]string{}
		visited = map[string]bool{}
		refused []string
		current string
	)

	for step := 1; step <= steps; step++ {
		result, err := d.gateway.UIFlowStep(ctx, call, llm.UIFlowInput{
			Context: prefix,
			AppURL:  input.Target,
			Auth:    input.AuthMode,
			Step:    step,
			Budget:  steps,
			History: history,
		})
		if err != nil {
			return Graph{}, err
		}

		decision, err := decodeStep(result.Raw)
		if err != nil {
			// A reply that does not parse is recorded and retried within the budget: the
			// alternative is losing a whole discovery to one malformed step.
			history = append(history, map[string]any{
				"action": "error",
				"detail": "",
				"result": err.Error(),
			})
			continue
		}

		if decision.Action == "answer" {
			produced := decision.toGraph()
			produced.Model = result.ModelName
			d.finalise(&produced, input, step, false, trail, visited, refused)
			return produced, nil
		}

		action, detail := decision.describe()
		if report != nil {
			report(step, action, detail)
		}

		key := action + " " + detail
		var (
			observed string
			blocked  bool
			landed   string
		)

		if cached, repeated := seen[key]; repeated {
			observed = cached + "\n(unchanged: this is where you already were)"
			landed = current
		} else {
			observed, blocked, landed = d.perform(ctx, session, decision, input)
			seen[key] = observed
		}

		if landed != "" {
			current = landed
			if path := pathOf(landed); path != "" && !blocked {
				visited[path] = true
			}
		}
		if blocked {
			refused = append(refused, strings.TrimSpace(action+" "+detail))
		}

		trail = append(trail, Step{
			Action:  action,
			Detail:  scrub(detail, input.Secrets),
			Reason:  scrub(decision.Reason, input.Secrets),
			URL:     current,
			Refused: blocked,
		})
		history = append(history, map[string]any{
			"action": action,
			"detail": scrub(detail, input.Secrets),
			"result": scrub(observed, input.Secrets),
		})
	}

	// The budget ran out. That is an ending rather than a failure: one more call asks
	// for the graph from what was seen, and a graph marked cut short is honest about
	// it (BE-7.2.4).
	final, err := d.finish(ctx, call, prefix, input, steps, history)
	if err != nil {
		return Graph{}, err
	}
	d.finalise(&final, input, steps, true, trail, visited, refused)
	return final, nil
}

// context is the cacheable prefix: what does not change between steps of one
// discovery. The vocabulary and the selector order are in it rather than in the
// per-step instruction for the same reason — they are identical on every call, and a
// cached prefix is paid for once (ai-architecture.md 3.6).
func (d *Discoverer) context(input Input) map[string]any {
	prefix := map[string]any{
		"target":       input.Target,
		"auth":         input.AuthMode,
		"actions":      []string{"goto", "click", "fill", "press", "back", "snapshot", "screenshot", "answer"},
		"selectors":    Ranked(),
		"placeholders": []string{"$QAVIA_USERNAME", "$QAVIA_PASSWORD"},
	}

	// Named only when the project set it. An empty key would read as "the login page is
	// at the empty path", which is a step spent on nothing.
	if input.LoginPath != "" {
		prefix["loginPath"] = input.LoginPath
	}
	return prefix
}

// finish asks for the graph once the budget is spent.
func (d *Discoverer) finish(
	ctx context.Context,
	call llm.AgentCall,
	prefix map[string]any,
	input Input,
	steps int,
	history []map[string]any,
) (Graph, error) {
	history = append(history, map[string]any{
		"action": "budget",
		"detail": "",
		"result": "The action budget is spent. Answer now with the graph from what you have " +
			"seen, and put what you did not reach in unreachable.",
	})

	result, err := d.gateway.UIFlowStep(ctx, call, llm.UIFlowInput{
		Context: prefix,
		AppURL:  input.Target,
		Auth:    input.AuthMode,
		Step:    steps,
		Budget:  steps,
		History: history,
	})
	if err != nil {
		return Graph{}, err
	}

	decision, err := decodeStep(result.Raw)
	if err != nil {
		return Graph{}, apierr.Internal(
			fmt.Errorf("the discovery produced no readable graph: %w", err))
	}
	if decision.Graph == nil {
		return Graph{}, apierr.Internal(
			fmt.Errorf("the discovery ended without a graph"))
	}

	produced := decision.toGraph()
	produced.Model = result.ModelName
	return produced, nil
}

// finalise attaches what the loop knows and the agent does not, then verifies.
func (d *Discoverer) finalise(
	graph *Graph,
	input Input,
	steps int,
	cutShort bool,
	trail []Step,
	visited map[string]bool,
	refused []string,
) {
	graph.Steps = steps
	graph.CutShort = cutShort
	graph.Target = input.Target
	graph.AuthMode = input.AuthMode
	graph.Trail = trail

	graph.Scrub(input.Secrets)
	graph.Verify(visited)

	// A refused action is worth reporting once, not once per attempt.
	for _, entry := range refused {
		if entry != "" && !contains(graph.Unreachable, entry) {
			graph.Unreachable = append(graph.Unreachable, entry+" (refused by the platform)")
		}
	}
}

// perform runs one action and reports the observation, whether the platform refused
// it, and where the browser ended up.
func (d *Discoverer) perform(
	ctx context.Context,
	session browserdriver.Session,
	decision step,
	input Input,
) (string, bool, string) {
	action := decision.toAction()

	if reason := destructive(action); reason != "" {
		// Recorded, not clicked. The prompt asks for this and the platform enforces it:
		// a discovery run against somebody's staging environment must not be one bad
		// guess away from emptying a table (BE-7.2.2).
		return "refused: " + reason + ". The action is recorded on the page; " +
			"choose something that does not change data.", true, ""
	}

	if err := browserdriver.Validate(action); err != nil {
		return "refused: " + err.Error(), true, ""
	}

	if action.Kind == "goto" {
		if reason := offTarget(action.URL, input.Target); reason != "" {
			// The browser's egress allowlist would refuse this anyway, further down. Said
			// here as well because a refusal with a reason is a step the agent learns
			// from, and a network timeout is thirty seconds of nothing (BE-4.7).
			return "refused: " + reason, true, ""
		}
	}

	observation, err := session.Do(ctx, action)
	if err != nil {
		// A transport failure is the session breaking rather than a page misbehaving,
		// and it is the one thing the loop cannot carry on from.
		return "the browser could not be reached: " + err.Error(), true, ""
	}

	return summarise(observation), false, observation.URL
}

// destructive names an action that would change data, or empty when it would not.
//
// Matched on what the element is called, because that is all a snapshot offers and it
// is what a person would read before clicking. False positives cost a step and are
// the right way to be wrong here.
func destructive(action browserdriver.Action) string {
	if action.Kind != "click" && action.Kind != "press" {
		return ""
	}

	against := strings.ToLower(strings.Join([]string{
		action.TestID, action.Name, action.Label, action.Text, action.Selector,
	}, " "))

	if match := destructiveWords.FindString(against); match != "" {
		return fmt.Sprintf("%q looks like it destroys or pays for something", strings.TrimSpace(match))
	}
	return ""
}

// destructiveWords is deliberately blunt. A word list that occasionally refuses a
// harmless "clear filters" button is a better trade than one that occasionally
// deletes a client's staging data.
var destructiveWords = regexp.MustCompile(`(?i)\b(delete|remove|destroy|drop|purge|wipe|erase|` +
	`deactivate|terminate|revoke|unsubscribe|cancel subscription|empty|reset all|` +
	`pay|checkout|place order|confirm order|buy now|transfer)\b`)

// offTarget refuses a navigation away from the application under discovery.
func offTarget(destination, target string) string {
	trimmed := strings.TrimSpace(destination)
	if trimmed == "" || strings.HasPrefix(trimmed, "/") {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Sprintf("%q is not a URL the platform can use", trimmed)
	}
	if parsed.Host == "" {
		return ""
	}

	base, err := url.Parse(target)
	if err != nil || base.Host == "" {
		return ""
	}
	if !strings.EqualFold(parsed.Host, base.Host) {
		return fmt.Sprintf("%s is not the application under discovery (%s), and the "+
			"browser is not allowed to reach it", parsed.Host, base.Host)
	}
	return ""
}

// summarise turns an observation into the compact text the next prompt carries.
//
// Compact because it is prompt input, and prompt input is paid for by the token on
// every remaining step of the discovery. The elements are the part worth spending on:
// they are what the next action is chosen from.
func summarise(observation browserdriver.Observation) string {
	var out strings.Builder

	if !observation.OK {
		out.WriteString("the action failed: " + observation.Error + "\n")
	}
	if observation.URL != "" {
		fmt.Fprintf(&out, "url: %s\n", observation.URL)
	}
	if observation.Title != "" {
		fmt.Fprintf(&out, "title: %s\n", observation.Title)
	}
	if observation.Status != 0 {
		fmt.Fprintf(&out, "status: %d\n", observation.Status)
	}
	if observation.File != "" {
		fmt.Fprintf(&out, "screenshot: %s\n", observation.File)
	}

	if len(observation.Elements) > 0 {
		out.WriteString("elements:\n")
		for index, element := range observation.Elements {
			if index == maxElementsInPrompt {
				fmt.Fprintf(&out, "  … and %d more\n", len(observation.Elements)-index)
				break
			}
			out.WriteString("  " + describeElement(element) + "\n")
		}
	}

	if trimmed := strings.TrimSpace(observation.Text); trimmed != "" {
		if len(trimmed) > maxTextInPrompt {
			trimmed = trimmed[:maxTextInPrompt] + "…"
		}
		out.WriteString("text: " + trimmed + "\n")
	}

	for _, line := range observation.Console {
		out.WriteString("console: " + line + "\n")
	}
	for _, line := range observation.FailedRequests {
		// Kept, because a page that looks fine and is missing an API call is the exact
		// shape of a flake nobody can reproduce later.
		out.WriteString("failed request: " + line + "\n")
	}

	return strings.TrimRight(out.String(), "\n")
}

const (
	maxElementsInPrompt = 60
	maxTextInPrompt     = 1200
)

// describeElement names one element the way the selector policy wants it named.
func describeElement(element browserdriver.Element) string {
	parts := []string{element.Tag}
	if element.Type != "" {
		parts = append(parts, "type="+element.Type)
	}
	if element.TestID != "" {
		parts = append(parts, "testid="+element.TestID)
	}
	if element.Role != "" {
		parts = append(parts, "role="+element.Role)
	}
	if element.Name != "" {
		parts = append(parts, "name="+element.Name)
	}
	if element.Label != "" {
		parts = append(parts, `label="`+element.Label+`"`)
	}
	if element.Href != "" {
		parts = append(parts, "href="+element.Href)
	}
	if element.Disabled {
		parts = append(parts, "disabled")
	}
	return strings.Join(parts, " ")
}

// Verify drops what the discovery did not actually reach.
//
// The agent is asked to record only flows it walked, and this is what makes the
// request enforceable rather than a request: a flow assembled from pages that were
// never opened is a spec that fails on its first navigation, which is worse than a
// smaller graph because somebody has to debug it. Same reasoning as evidence
// validation in the analysis phase (BE-5.3) and as dropping invented files from a
// repository map (BE-6.4).
func (g *Graph) Verify(visited map[string]bool) []string {
	if len(visited) == 0 {
		// Nothing was navigated, so there is nothing to check against. A graph from a
		// discovery that never loaded a page is empty for other reasons.
		return nil
	}

	var dropped []string

	pages := g.Pages[:0]
	for _, page := range g.Pages {
		if visited[normalisePath(page.Path)] {
			pages = append(pages, page)
			continue
		}
		dropped = append(dropped, page.Path)
		if !contains(g.Unreachable, page.Path) {
			g.Unreachable = append(g.Unreachable, page.Path+" (recorded but never opened)")
		}
	}
	g.Pages = pages

	flows := g.Flows[:0]
	for _, flow := range g.Flows {
		if missing := unwalked(flow, visited); missing != "" {
			dropped = append(dropped, flow.Name+" (navigates to "+missing+")")
			continue
		}
		flows = append(flows, flow)
	}
	g.Flows = flows

	return dropped
}

// unwalked returns the first path a flow navigates to that was never opened.
func unwalked(flow Flow, visited map[string]bool) string {
	for _, step := range flow.Steps {
		if step.Action != "goto" || step.Target == "" {
			continue
		}
		if !visited[normalisePath(step.Target)] {
			return step.Target
		}
	}
	return ""
}

// Scrub removes any credential that reached the record.
//
// The browser redacts its own replies and the placeholders mean a value should never
// have arrived here at all. This runs anyway: a graph is stored, and a secret written
// into jsonb is a secret in a backup (BE-7.3.3).
func (g *Graph) Scrub(secrets []string) {
	if len(secrets) == 0 {
		return
	}

	g.Summary = scrub(g.Summary, secrets)
	for index := range g.Unknowns {
		g.Unknowns[index] = scrub(g.Unknowns[index], secrets)
	}
	for index := range g.Unreachable {
		g.Unreachable[index] = scrub(g.Unreachable[index], secrets)
	}
	for pageIndex := range g.Pages {
		page := &g.Pages[pageIndex]
		page.Purpose = scrub(page.Purpose, secrets)
		for actionIndex := range page.Actions {
			page.Actions[actionIndex].Description = scrub(page.Actions[actionIndex].Description, secrets)
		}
	}
	for flowIndex := range g.Flows {
		flow := &g.Flows[flowIndex]
		flow.Purpose = scrub(flow.Purpose, secrets)
		for stepIndex := range flow.Steps {
			flow.Steps[stepIndex].Value = scrub(flow.Steps[stepIndex].Value, secrets)
			flow.Steps[stepIndex].Expect = scrub(flow.Steps[stepIndex].Expect, secrets)
		}
	}
	for index := range g.Trail {
		g.Trail[index].Detail = scrub(g.Trail[index].Detail, secrets)
		g.Trail[index].Reason = scrub(g.Trail[index].Reason, secrets)
		g.Trail[index].URL = scrub(g.Trail[index].URL, secrets)
	}
}

// scrub replaces a secret with the placeholder that stands for it, so a generated
// spec reads correctly and the value is gone.
func scrub(text string, secrets []string) string {
	if text == "" {
		return text
	}

	out := text
	for _, secret := range secrets {
		// Short values are skipped: replacing every "a" in a graph because somebody set a
		// one-character password would destroy the graph and protect nothing.
		if len(secret) < 4 {
			continue
		}
		out = strings.ReplaceAll(out, secret, "[redacted]")
	}
	return out
}

// normalisePath reduces a URL or path to the path a page is keyed by.
//
// Query strings and fragments are dropped, and a trailing slash is not a different
// page: `/orders`, `/orders/`, and `https://app/orders?page=2` are one screen.
func normalisePath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	if parsed, err := url.Parse(trimmed); err == nil && parsed.Path != "" {
		trimmed = parsed.Path
	} else if parsed != nil && parsed.Host != "" {
		trimmed = "/"
	}

	if index := strings.IndexAny(trimmed, "?#"); index >= 0 {
		trimmed = trimmed[:index]
	}
	if len(trimmed) > 1 {
		trimmed = strings.TrimRight(trimmed, "/")
	}
	if trimmed == "" {
		trimmed = "/"
	}
	return trimmed
}

// pathOf is normalisePath for a URL the browser reported.
func pathOf(raw string) string { return normalisePath(raw) }

func contains(list []string, value string) bool {
	for _, entry := range list {
		if entry == value || strings.HasPrefix(entry, value+" (") {
			return true
		}
	}
	return false
}

// step is the agent's reply: one action, or the finished graph.
type step struct {
	Action string `json:"action"`
	Reason string `json:"reason"`

	URL        string `json:"url"`
	TestID     string `json:"testid"`
	Role       string `json:"role"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Text       string `json:"text"`
	Value      string `json:"value"`
	Key        string `json:"key"`
	Screenshot string `json:"screenshot"`

	Graph *struct {
		Summary string `json:"summary"`
		Pages   []struct {
			Path         string `json:"path"`
			Title        string `json:"title"`
			Purpose      string `json:"purpose"`
			RequiresAuth bool   `json:"requires_auth"`
			Actions      []struct {
				Description string `json:"description"`
				TestID      string `json:"testid"`
				Role        string `json:"role"`
				Name        string `json:"name"`
				Label       string `json:"label"`
				LeadsTo     string `json:"leads_to"`
			} `json:"actions"`
		} `json:"pages"`
		Flows []struct {
			Name         string `json:"name"`
			Purpose      string `json:"purpose"`
			RequiresAuth bool   `json:"requires_auth"`
			Steps        []struct {
				Action string `json:"action"`
				Target string `json:"target"`
				TestID string `json:"testid"`
				Role   string `json:"role"`
				Name   string `json:"name"`
				Label  string `json:"label"`
				Value  string `json:"value"`
				Expect string `json:"expect"`
			} `json:"steps"`
		} `json:"flows"`
		Unreachable []string `json:"unreachable"`
		Unknowns    []string `json:"unknowns"`
	} `json:"graph"`
}

func decodeStep(raw json.RawMessage) (step, error) {
	var decoded step
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return step{}, fmt.Errorf("the step could not be read: %w", err)
	}
	if strings.TrimSpace(decoded.Action) == "" {
		return step{}, fmt.Errorf("the step named no action")
	}
	if decoded.Action == "answer" && decoded.Graph == nil {
		return step{}, fmt.Errorf("the step answered with no graph")
	}
	return decoded, nil
}

// toAction is the agent's step as the driver's vocabulary.
func (s step) toAction() browserdriver.Action {
	return browserdriver.Action{
		Kind:           s.Action,
		URL:            s.URL,
		TestID:         s.TestID,
		Role:           s.Role,
		Name:           s.Name,
		Label:          s.Label,
		Text:           s.Text,
		Value:          s.Value,
		Key:            s.Key,
		ScreenshotName: s.Screenshot,
	}
}

// describe names the action for the trail and the job log.
func (s step) describe() (string, string) {
	switch s.Action {
	case "goto":
		return "goto", s.URL
	case "click":
		return "click", s.locator()
	case "fill":
		// The value is deliberately absent. A placeholder is safe to log and a typed
		// value is not, and the trail is not the place to work out which this was.
		return "fill", s.locator()
	case "press":
		return "press", s.Key
	case "screenshot":
		return "screenshot", s.Screenshot
	default:
		return s.Action, ""
	}
}

// locator names the element the way the step chose it.
func (s step) locator() string {
	switch {
	case s.TestID != "":
		return "testid=" + s.TestID
	case s.Role != "" && s.Name != "":
		return s.Role + ` "` + s.Name + `"`
	case s.Label != "":
		return `label "` + s.Label + `"`
	case s.Text != "":
		return `text "` + s.Text + `"`
	default:
		return ""
	}
}

func (s step) toGraph() Graph {
	if s.Graph == nil {
		return Graph{}
	}

	produced := Graph{
		Summary:     s.Graph.Summary,
		Unknowns:    s.Graph.Unknowns,
		Unreachable: s.Graph.Unreachable,
	}

	for _, page := range s.Graph.Pages {
		converted := Page{
			Path:         page.Path,
			Title:        page.Title,
			Purpose:      page.Purpose,
			RequiresAuth: page.RequiresAuth,
		}
		for _, action := range page.Actions {
			converted.Actions = append(converted.Actions, PageAction{
				Description: action.Description,
				TestID:      action.TestID,
				Role:        action.Role,
				Name:        action.Name,
				Label:       action.Label,
				LeadsTo:     action.LeadsTo,
			})
		}
		produced.Pages = append(produced.Pages, converted)
	}

	for _, flow := range s.Graph.Flows {
		converted := Flow{
			Name:         flow.Name,
			Purpose:      flow.Purpose,
			RequiresAuth: flow.RequiresAuth,
		}
		for _, flowStep := range flow.Steps {
			converted.Steps = append(converted.Steps, FlowStep{
				Action: flowStep.Action,
				Target: flowStep.Target,
				TestID: flowStep.TestID,
				Role:   flowStep.Role,
				Name:   flowStep.Name,
				Label:  flowStep.Label,
				Value:  flowStep.Value,
				Expect: flowStep.Expect,
			})
		}
		produced.Flows = append(produced.Flows, converted)
	}

	return produced
}
