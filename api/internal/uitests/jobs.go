package uitests

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/browserdriver"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/llm"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/targets"
)

// The discovery stage (BE-7.2, BE-7.3).
//
// It runs on a worker rather than in the API for the same reason execution does: it
// needs a container, and a request that waits for a browser to walk forty pages is a
// request that has timed out. What this handler owns beyond the loop:
//
//   - **The credentials.** Read from settings, decrypted here, injected into the
//     container's environment, and never returned to the agent or written into the
//     graph. The container substitutes the two placeholders itself (BE-7.3.2).
//   - **The session's lifetime.** Opened, closed on every path out, and the artifacts
//     collected while it is still alive: a session's workspace is a tmpfs, so after
//     Close there is nothing left to read (BE-7.5).
//   - **The target check.** The same allowlist and address rules a test run goes
//     through, because a browser is as good an SSRF primitive as a test runner
//     (BE-4.7).

// TypeDiscover is the stage name the pipeline declares.
const TypeDiscover = jobs.TypeUIDiscover

// Flows is the slice of this package's own service the stage needs.
type Flows interface {
	Store(ctx context.Context, input StoreInput) (Stored, error)
}

// Settings is the slice of the settings service this stage needs.
type Settings interface {
	Int(ctx context.Context, key string, target settings.Target) (int, error)
	String(ctx context.Context, key string, target settings.Target) (string, error)
	Secret(ctx context.Context, key string, target settings.Target) (string, bool, error)
}

// Targets checks the project's target the same way a run does.
type Targets interface {
	Check(ctx context.Context, projectID uuid.UUID) (targets.Target, error)
}

// Drivers resolves the browser driver settings ask for.
//
// The registry satisfies this. Declared as one method so switching to an external
// Playwright server later changes a settings value and nothing in this file
// (BE-7.7.3).
type Drivers interface {
	Resolve(id string) (browserdriver.Driver, error)
}

// Deps is everything the stage shares.
type Deps struct {
	Flows    Flows
	Gateway  Gateway
	Settings Settings
	Targets  Targets
	Drivers  Drivers
	Objects  objectstore.Store
}

// DiscoverHandler walks an application and records what it found.
type DiscoverHandler struct {
	deps       Deps
	discoverer *Discoverer
}

func NewDiscoverHandler(deps Deps) *DiscoverHandler {
	return &DiscoverHandler{deps: deps, discoverer: NewDiscoverer(deps.Gateway)}
}

func (h *DiscoverHandler) Type() string { return TypeDiscover }

// Payload names the project to explore.
type Payload struct {
	ProjectID uuid.UUID `json:"projectId"`

	// TargetURL overrides the project's configured target, checked against the same
	// allowlist. An override is exactly the input that must not be trusted.
	TargetURL string `json:"targetUrl,omitempty"`

	// RequestID is the per-request nonce the idempotency key is built from. Keying on
	// the project would make a second discovery find the first one's row and push
	// nothing, and discovering an application again is work somebody asks for.
	RequestID uuid.UUID `json:"requestId"`
}

func (h *DiscoverHandler) IdempotencyKey(payload Payload) string {
	return DiscoverIdempotencyKey(payload)
}

// DiscoverIdempotencyKey is exported so the API can declare the type as enqueue-only
// without building a handler it cannot run.
func DiscoverIdempotencyKey(payload Payload) string {
	if payload.RequestID == uuid.Nil {
		return fmt.Sprintf("ui.discover:%s", payload.ProjectID)
	}
	return fmt.Sprintf("ui.discover:%s", payload.RequestID)
}

func (h *DiscoverHandler) Handle(
	ctx context.Context,
	payload Payload,
	jc jobs.JobContext,
) error {
	target, err := h.deps.Targets.Check(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	credentials, err := h.credentials(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	budget, err := h.budget(ctx, payload.ProjectID)
	if err != nil {
		return err
	}

	driver, err := h.driver(ctx, payload.ProjectID)
	if err != nil {
		return err
	}
	if !driver.Available(ctx) {
		return apierr.NoBrowserDriver(
			"This worker has no container runtime, or its runtime is not answering.")
	}

	jc.Event("Discovering %s with %s, up to %d actions, %s",
		target.URL, driver.ID(), budget.Steps, credentials.describe())

	session, err := driver.Open(ctx, browserdriver.Options{
		ProjectID:  payload.ProjectID,
		Target:     target.URL,
		AuthMode:   credentials.Mode,
		Token:      credentials.Token,
		Username:   credentials.Username,
		Password:   credentials.Password,
		HeaderName: credentials.HeaderName,
		Budget:     budget.Steps,
	})
	if err != nil {
		return err
	}

	// Closed on every path out. A session left open is a browser left running, which
	// is the most expensive thing this platform can leak.
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()

		if err := session.Close(closeCtx); err != nil {
			slog.WarnContext(ctx, "close the browser session",
				"project_id", payload.ProjectID, "error", err)
		}
	}()

	started := time.Now()
	jobID := jc.JobID()

	graph, err := h.discoverer.Discover(ctx, session, CallFor(payload.ProjectID, jobID), Input{
		Target:    target.URL,
		AuthMode:  credentials.describe(),
		LoginPath: credentials.LoginPath,
		Secrets:   credentials.secrets(),
		Budget:    budget,
	}, func(step int, action, detail string) {
		// Every action is in the job log, because a discovery is a model deciding what
		// to click forty times and a reader has to be able to follow it.
		jc.Event("Step %d: %s %s", step, action, detail)
		jc.Progress(step * 85 / max(budget.Steps, 1))
	})
	if err != nil {
		return err
	}

	// Collected before the session closes, and before the row is written: a graph
	// stored without its recordings is a graph whose evidence is gone, and after Close
	// there is nothing left to read (BE-7.5).
	artifacts := h.collect(ctx, session, payload.ProjectID, jobID, jc)

	stored, err := h.deps.Flows.Store(ctx, StoreInput{
		ProjectID: payload.ProjectID,
		Graph:     graph,
		Artifacts: artifacts,
		ModelName: graph.Model,
		JobID:     &jobID,
	})
	if err != nil {
		return err
	}

	ending := "finished"
	if graph.CutShort {
		// Said plainly: a graph produced because the budget ran out describes less than
		// one the agent chose to finish, and a reader should know which they have.
		ending = "cut short by the action budget"
	}

	jc.Event("Mapped %d page(s) and %d flow(s) in %d action(s), %s, in %s (graph %s)",
		len(graph.Pages), len(graph.Flows), graph.Steps, ending,
		time.Since(started).Round(time.Second), stored.ID)

	if len(graph.Unreachable) > 0 {
		jc.Event("Not reached: %v", graph.Unreachable)
	}
	if len(graph.Unknowns) > 0 {
		jc.Event("Unknowns: %v", graph.Unknowns)
	}

	jc.Progress(100)
	return nil
}

// CallFor names the project and job an agent call belongs to, so spend is
// attributable per project rather than to "the platform".
func CallFor(projectID, jobID uuid.UUID) llm.AgentCall {
	return llm.AgentCall{ProjectID: &projectID, JobID: &jobID}
}

// credentials are how a discovery signs in.
type credentials struct {
	// Mode is the transport mode the browser context applies: a bearer header, an API
	// key header, HTTP basic. It is not the same thing as a form login, which is a flow
	// the agent walks.
	Mode       string
	Token      string
	HeaderName string

	Username  string
	Password  string
	LoginPath string
}

// describe says how the discovery will authenticate, in words safe to log.
//
// The username is named because it is not a secret and because a graph that reached
// no authenticated pages is read differently depending on who it signed in as. The
// password never appears here or anywhere else (BE-7.3.3).
func (c credentials) describe() string {
	switch {
	case c.Username != "" && c.Mode != "" && c.Mode != "none":
		return fmt.Sprintf("a form login as %s and %s transport auth", c.Username, c.Mode)
	case c.Username != "":
		return fmt.Sprintf("a form login as %s", c.Username)
	case c.Mode != "" && c.Mode != "none":
		return c.Mode + " transport auth"
	default:
		return "no authentication"
	}
}

// secrets are the values that must not appear in a stored graph or a job log.
func (c credentials) secrets() []string {
	var found []string
	if c.Password != "" {
		found = append(found, c.Password)
	}
	if c.Token != "" {
		found = append(found, c.Token)
	}
	return found
}

// credentials reads what the project configured, decrypted at the point of use.
//
// Point of use is the whole rule: nothing here is held by a long-lived process, and
// the values go straight into a container's environment. A worker that keeps a
// decrypted password in a struct for the length of its life is a worker whose core
// dump holds one (BE-7.3.2).
func (h *DiscoverHandler) credentials(
	ctx context.Context,
	projectID uuid.UUID,
) (credentials, error) {
	scope := settings.Target{ProjectID: &projectID}

	mode, err := h.deps.Settings.String(ctx, "targets.auth_mode", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the target auth mode: %w", err))
	}

	header, err := h.deps.Settings.String(ctx, "targets.auth_header_name", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the auth header name: %w", err))
	}

	username, err := h.deps.Settings.String(ctx, "ui.login_username", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the UI login username: %w", err))
	}

	loginPath, err := h.deps.Settings.String(ctx, "ui.login_path", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the login path: %w", err))
	}

	// Both secrets are optional. A project whose target needs no authentication is a
	// project with no credential, not a misconfigured one.
	token, _, err := h.deps.Settings.Secret(ctx, "targets.auth_credential", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the target credential: %w", err))
	}

	password, _, err := h.deps.Settings.Secret(ctx, "ui.login_password", scope)
	if err != nil {
		return credentials{}, apierr.Internal(fmt.Errorf("read the UI login password: %w", err))
	}

	return credentials{
		Mode:       mode,
		Token:      token,
		HeaderName: header,
		Username:   username,
		Password:   password,
		LoginPath:  loginPath,
	}, nil
}

// budget reads how many actions a discovery may take.
func (h *DiscoverHandler) budget(ctx context.Context, projectID uuid.UUID) (Budget, error) {
	steps, err := h.deps.Settings.Int(ctx, "ui.discovery_budget",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return Budget{}, apierr.Internal(fmt.Errorf("read the discovery budget: %w", err))
	}
	return Budget{Steps: steps}, nil
}

// driver resolves the browser driver settings ask for.
//
// An empty setting means the built-in, which is the registry's rule everywhere: the
// bundled container is what an installation with nothing configured gets (F-13.6).
func (h *DiscoverHandler) driver(
	ctx context.Context,
	projectID uuid.UUID,
) (browserdriver.Driver, error) {
	configured, err := h.deps.Settings.String(ctx, "ui.browser_driver",
		settings.Target{ProjectID: &projectID})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the browser driver setting: %w", err))
	}

	driver, err := h.deps.Drivers.Resolve(strings.TrimSpace(configured))
	if err != nil {
		// A settings value naming a driver nobody registered is a misconfiguration
		// somebody has to see, not a reason to silently use another one.
		return nil, apierr.NoBrowserDriver(err.Error())
	}
	return driver, nil
}

// collect stores the session's recordings and returns their keys.
//
// Best effort throughout: a discovery whose video could not be stored is still a
// discovery with a graph, and losing the recording is not a reason to lose the map.
func (h *DiscoverHandler) collect(
	ctx context.Context,
	session browserdriver.Session,
	projectID, jobID uuid.UUID,
	jc jobs.JobContext,
) []Artifact {
	if h.deps.Objects == nil {
		return nil
	}

	// Its own timeout: the discovery may have used all of the job's, and a trace is
	// worth a minute of its own rather than being lost to a context that just expired.
	collectCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()

	recordings, err := session.Artifacts(collectCtx)
	if err != nil {
		slog.WarnContext(ctx, "collect the discovery artifacts",
			"project_id", projectID, "error", err)
		return nil
	}

	stored := make([]Artifact, 0, len(recordings))
	for name, content := range recordings {
		key := objectstore.Key(projectID, "ui-discovery", jobID, path.Base(name))

		if _, err := h.deps.Objects.Put(collectCtx, key, bytes.NewReader(content),
			objectstore.PutOptions{
				ContentType: contentTypeFor(name),
				Size:        int64(len(content)),
			}); err != nil {
			slog.WarnContext(ctx, "store a discovery artifact",
				"project_id", projectID, "artifact", name, "error", err)
			continue
		}

		stored = append(stored, Artifact{
			Name:        path.Base(name),
			Key:         key,
			Bytes:       int64(len(content)),
			ContentType: contentTypeFor(name),
		})
	}

	if len(stored) > 0 {
		jc.Event("Recorded %d artifact(s): %s", len(stored), names(stored))
	}
	return stored
}

// contentTypeFor names a recording so a browser plays it rather than downloading it.
func contentTypeFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".webm":
		return "video/webm"
	case ".zip":
		// A Playwright trace. Not application/zip: a trace opened in the wrong tool is
		// a directory of JSON nobody reads, and the viewer keys off the name.
		return "application/zip"
	case ".png":
		return "image/png"
	}
	if guessed := mime.TypeByExtension(path.Ext(name)); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func names(artifacts []Artifact) string {
	list := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		list = append(list, artifact.Name)
	}
	return strings.Join(list, ", ")
}
