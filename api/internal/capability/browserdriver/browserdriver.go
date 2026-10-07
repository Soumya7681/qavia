package browserdriver

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
)

// The BrowserDriver capability (F-7.15, F-7.16, BE-7.1).
//
// Two implementations are planned and the order matters: the bundled Playwright
// container is built first and completely, and the external MCP server is added later
// for teams that already run one. That is the same rule every capability in this
// platform follows — the built-in has to be the one that works on an installation with
// nothing configured — and here it has teeth, because browser automation is where the
// temptation to depend on somebody else's daemon is strongest.
//
// The interface is deliberately a **session** rather than a script. An agent
// discovering an application clicks something, sees what happened, and decides what to
// click next; a driver that took a finished script would force the platform to guess
// the whole flow in advance, which is precisely what reading component source does
// badly (BE-7.2.2).
//
// It is also deliberately a **fixed vocabulary**. There is no "evaluate this
// JavaScript" action, because that tool can read any credential the browser holds and
// post it anywhere the egress allowlist permits.

// Action is one thing to do in a browser.
type Action struct {
	// Kind is goto, click, fill, press, back, snapshot, or screenshot.
	Kind string `json:"action"`

	// URL is a path or an absolute URL, for goto. A path is resolved against the
	// project's target.
	URL string `json:"url,omitempty"`

	// The element to act on, in the order the selector policy prefers: a test id, then
	// a role and an accessible name, then a label, then text. A raw selector is last
	// and exists for the cases the others cannot express (BE-7.4.2).
	TestID   string `json:"testid,omitempty"`
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	Label    string `json:"label,omitempty"`
	Text     string `json:"text,omitempty"`
	Selector string `json:"selector,omitempty"`

	// Value is what to type, for fill. The two placeholders `$QAVIA_USERNAME` and
	// `$QAVIA_PASSWORD` are substituted inside the container, so a credential never
	// reaches the agent and never appears in a recorded step (BE-7.3.2).
	Value string `json:"value,omitempty"`

	// Key is what to press.
	Key string `json:"key,omitempty"`

	// ScreenshotName names a captured image.
	//
	// Its own JSON field rather than reusing `name`, because `name` already carries an
	// element's accessible name: two Go fields with one tag are two fields the encoder
	// silently drops, which is how a screenshot ends up saved under the default name.
	ScreenshotName string `json:"screenshot,omitempty"`
	FullPage       bool   `json:"fullPage,omitempty"`
}

// Element is one thing on a page a person could interact with.
//
// Described the way the selector policy wants it named, so a generated spec can be
// written from a snapshot without inventing a selector: the test id if there is one,
// otherwise the role and the accessible name.
type Element struct {
	Tag      string `json:"tag"`
	Type     string `json:"type,omitempty"`
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	TestID   string `json:"testid,omitempty"`
	Label    string `json:"label,omitempty"`
	Href     string `json:"href,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// Observation is what the browser looked like after an action.
type Observation struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	URL    string `json:"url,omitempty"`
	Title  string `json:"title,omitempty"`
	Status int    `json:"status,omitempty"`

	Elements []Element `json:"elements,omitempty"`

	// Text is the page's visible text, truncated inside the container: it is prompt
	// input, and prompt input is paid for by the token.
	Text string `json:"text,omitempty"`

	// Console and FailedRequests are where half of what makes a UI flaky shows up, and
	// nowhere else.
	Console        []string `json:"console,omitempty"`
	FailedRequests []string `json:"failedRequests,omitempty"`

	// File is set by screenshot: a workspace-relative path inside the session.
	File string `json:"file,omitempty"`
}

// Session is a live browser.
type Session interface {
	// Do performs one action and returns what the page looked like afterwards.
	//
	// A failed action returns an Observation with OK false rather than an error: a
	// click on something that moved is exactly what discovery is for, and the caller
	// decides what to do next.
	Do(ctx context.Context, action Action) (Observation, error)

	// Artifacts collects the video, trace, and screenshots. Called before Close,
	// because a session's workspace does not outlive its container.
	Artifacts(ctx context.Context) (map[string][]byte, error)

	// Close ends the session and destroys whatever was holding it.
	Close(ctx context.Context) error
}

// Options are what a session needs to start.
type Options struct {
	ProjectID uuid.UUID

	// Target is the base URL a relative goto resolves against. Already checked against
	// the project's allowlist by the caller: a browser is as good an SSRF primitive as
	// a test runner (BE-4.7).
	Target string

	// AuthMode and the credentials are passed to the driver rather than to the agent.
	// The driver injects them as environment variables into the container, and the
	// container substitutes them; nothing about them crosses back (BE-7.3.2).
	AuthMode   string
	Token      string
	Username   string
	Password   string
	HeaderName string

	// Budget is how many actions the session will accept, so a discovery run
	// terminates whatever the agent decides (BE-7.2.4).
	Budget int
}

// Driver opens browser sessions.
type Driver interface {
	ID() string
	Available(ctx context.Context) bool

	Open(ctx context.Context, options Options) (Session, error)
}

// Registry holds every driver. The bundled container registers first and is therefore
// the fallback for an external one.
type Registry = capability.Registry[Driver]

// NewRegistry builds the driver registry.
func NewRegistry() *Registry { return capability.NewRegistry[Driver]("browserdriver") }

// Actions the vocabulary permits. Fixed, and checked before anything is sent: an
// action this platform does not offer is a mistake to correct rather than a string to
// forward to a browser.
var actions = map[string]bool{
	"goto": true, "click": true, "fill": true, "press": true,
	"back": true, "snapshot": true, "screenshot": true,
}

// Validate rejects an action the vocabulary does not cover, or one that names nothing
// to act on.
func Validate(action Action) error {
	if !actions[action.Kind] {
		return fmt.Errorf("browserdriver: %q is not an action this platform offers", action.Kind)
	}

	switch action.Kind {
	case "click", "fill":
		named := action.Role != "" && action.Name != ""
		if action.TestID == "" && action.Label == "" && action.Text == "" &&
			action.Selector == "" && !named {
			return fmt.Errorf(
				"browserdriver: %s needs a testid, a role and name, a label, text, or a selector",
				action.Kind)
		}
	}
	return nil
}
