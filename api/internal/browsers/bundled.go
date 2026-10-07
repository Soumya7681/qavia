// Package browsers is the bundled BrowserDriver: a Playwright container this platform
// builds and runs itself (F-7.15, BE-7.1).
//
// It is the built-in, and it is built first and completely, because browser automation
// is where the pull towards depending on somebody else's daemon is strongest. An
// installation with an empty MCP settings table has to be able to drive a browser, or
// the whole UI-testing half of the platform is optional in a way the requirements say
// it is not (F-13.6).
//
// It reuses the execution boundary from phase 4 rather than inventing a second one:
// the same image family, the same egress allowlist, the same cgroup limits, the same
// non-root user, the same reaping. The one thing a browser needs that a test run does
// not is a container that stays alive across steps, and that is a session
// (internal/runner/session.go).
package browsers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/browserdriver"
	"github.com/hyscaler/qavia/api/internal/runner"
	"github.com/hyscaler/qavia/api/internal/testfiles"
)

// BundledID is the built-in driver's ID.
const BundledID = "bundled-playwright"

// Runner is the slice of the container runtime this package needs.
type Runner interface {
	Open(ctx context.Context, spec runner.SessionSpec) (*runner.Session, error)
	Available(ctx context.Context) bool
}

// Settings supplies the image, limits, and runtime, so this package does not re-read
// the runner configuration it does not own.
type Settings interface {
	ImageFor(ctx context.Context, projectID uuid.UUID, framework testfiles.Framework) (string, error)
	Limits(ctx context.Context, projectID uuid.UUID) (runner.Limits, error)
	Runtime(ctx context.Context) (runner.Runtime, error)
}

// Resolver turns the project's target into the host and address the container is
// allowed to reach. The same check a test run's target goes through.
type Resolver interface {
	Allowed(ctx context.Context, projectID uuid.UUID, target string) ([]runner.HostAddress, error)
}

// Bundled is the built-in driver.
type Bundled struct {
	runner   Runner
	settings Settings
	resolver Resolver
}

func NewBundled(containers Runner, settings Settings, resolver Resolver) *Bundled {
	return &Bundled{runner: containers, settings: settings, resolver: resolver}
}

func (b *Bundled) ID() string { return BundledID }

// Available reports whether a container runtime is reachable. A worker without one
// cannot drive a browser, and the request is refused with a reason rather than hanging.
func (b *Bundled) Available(ctx context.Context) bool {
	return b.runner != nil && b.runner.Available(ctx)
}

// Open starts a browser session.
func (b *Bundled) Open(
	ctx context.Context,
	options browserdriver.Options,
) (browserdriver.Session, error) {
	image, err := b.settings.ImageFor(ctx, options.ProjectID, testfiles.FrameworkPlaywright)
	if err != nil {
		return nil, err
	}
	limits, err := b.settings.Limits(ctx, options.ProjectID)
	if err != nil {
		return nil, err
	}
	runtime, err := b.settings.Runtime(ctx)
	if err != nil {
		return nil, err
	}

	// A browser is memory-hungry in a way an API suite is not: Chromium with a page
	// open needs more than the default a Supertest run is sized for. Raised rather than
	// replaced, so an operator who tightened the limit still gets their ceiling.
	if limits.MemoryMiB < minBrowserMemoryMiB {
		limits.MemoryMiB = minBrowserMemoryMiB
	}
	if limits.TmpfsMiB < minBrowserTmpfsMiB {
		limits.TmpfsMiB = minBrowserTmpfsMiB
	}

	allowed, err := b.resolver.Allowed(ctx, options.ProjectID, options.Target)
	if err != nil {
		return nil, err
	}

	session, err := b.runner.Open(ctx, runner.SessionSpec{
		Spec: runner.Spec{
			RunID: "browse-" + uuid.New().String(),
			Image: image,

			// The image's own browse entry point. The protocol is line-delimited JSON on
			// stdin and stdout, so the container needs no port and keeps exactly the
			// network its allowlist opened.
			Command: []string{"qavia-run", "browse"},

			Env:     environmentFor(options),
			Limits:  limits,
			Runtime: runtime,
			Egress:  runner.Egress{AllowedHosts: allowed},
		},
	})
	if err != nil {
		return nil, err
	}

	budget := options.Budget
	if budget <= 0 {
		budget = defaultBudget
	}

	return &bundledSession{session: session, remaining: budget}, nil
}

// Limits a browser needs beyond an API suite's.
const (
	minBrowserMemoryMiB = 2048
	minBrowserTmpfsMiB  = 1024

	// defaultBudget bounds one session. Discovery terminates because the platform stops
	// accepting actions, not because the agent decides to (BE-7.2.4).
	defaultBudget = 40
)

// environmentFor is how the container learns the target and the credentials.
//
// Environment variables, injected into the container, never written into a file and
// never returned to the agent: the container substitutes the two placeholders itself,
// so a credential is used without ever being seen by the thing deciding what to type
// (BE-7.3.2).
func environmentFor(options browserdriver.Options) map[string]string {
	env := map[string]string{
		"QAVIA_TARGET_URL": options.Target,
		"QAVIA_AUTH_MODE":  options.AuthMode,
		"CI":               "true",
	}
	if options.Token != "" {
		env["QAVIA_AUTH_TOKEN"] = options.Token
	}
	if options.Username != "" {
		env["QAVIA_AUTH_USERNAME"] = options.Username
	}
	if options.Password != "" {
		env["QAVIA_AUTH_PASSWORD"] = options.Password
	}
	if options.HeaderName != "" {
		env["QAVIA_AUTH_HEADER"] = options.HeaderName
	}
	return env
}

// bundledSession is one live browser.
type bundledSession struct {
	session *runner.Session

	mutex     sync.Mutex
	remaining int
}

// artifactReply is what the container answers a finish or artifact command with.
type artifactReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`

	Artifacts []struct {
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
	} `json:"artifacts,omitempty"`

	Name   string `json:"name,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

func (s *bundledSession) Do(
	ctx context.Context,
	action browserdriver.Action,
) (browserdriver.Observation, error) {
	if err := browserdriver.Validate(action); err != nil {
		// Returned as a refused observation rather than an error, so an agent that asked
		// for something the vocabulary does not offer learns and continues.
		return browserdriver.Observation{OK: false, Error: err.Error()}, nil
	}

	s.mutex.Lock()
	defer s.mutex.Unlock()

	if s.remaining <= 0 {
		return browserdriver.Observation{
			OK:    false,
			Error: "the session's action budget is spent",
		}, nil
	}
	s.remaining--

	command, err := json.Marshal(action)
	if err != nil {
		return browserdriver.Observation{}, fmt.Errorf("encode a browser action: %w", err)
	}

	reply, err := s.session.Send(ctx, command)
	if err != nil {
		return browserdriver.Observation{}, err
	}

	var observation browserdriver.Observation
	if err := json.Unmarshal(reply, &observation); err != nil {
		return browserdriver.Observation{}, fmt.Errorf(
			"the browser replied with something unreadable: %w", err)
	}
	return observation, nil
}

// Artifacts collects the video, the trace, and any screenshots.
//
// They come back over the command channel, base64 encoded, rather than being copied
// out of the container. That is not a preference: Docker cannot copy a file out of a
// tmpfs mount, and the workspace is a tmpfs precisely because the root filesystem is
// read-only. A trace that cannot be copied has to be written down the pipe or not
// collected at all (BE-7.5).
//
// The finish command comes first, because a trace is written when tracing stops and a
// video when the context closes: collecting before that would find nothing, which is
// exactly the bug this ordering exists to avoid.
func (s *bundledSession) Artifacts(ctx context.Context) (map[string][]byte, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	listing, err := s.ask(ctx, map[string]any{"action": "finish"})
	if err != nil {
		return nil, err
	}
	if !listing.OK {
		return nil, fmt.Errorf("the browser could not finish its session: %s", listing.Error)
	}

	collected := make(map[string][]byte, len(listing.Artifacts))
	for _, artifact := range listing.Artifacts {
		if !isRecording(artifact.Name) {
			// The image writes its own report file into the same directory, and a run
			// report is not a recording of a browser session: collecting it would put an
			// empty JSON document in front of the video somebody came to watch.
			continue
		}
		if artifact.Bytes == 0 {
			// An empty file is a file the browser started and never wrote: skipped rather
			// than stored, so nobody downloads a zero-byte video.
			continue
		}

		reply, err := s.ask(ctx, map[string]any{"action": "artifact", "name": artifact.Name})
		if err != nil {
			return nil, err
		}
		if !reply.OK {
			// One artifact refused is not the collection failing: a trace that grew past the
			// limit should not cost the video as well.
			continue
		}

		content, err := base64.StdEncoding.DecodeString(reply.Base64)
		if err != nil {
			continue
		}
		collected[artifact.Name] = content
	}

	return collected, nil
}

// isRecording keeps the three things a session records and nothing else.
//
// An allowlist rather than a deny list: the artifact directory is shared with whatever
// the image writes there, and a new file appearing in a future image should not
// silently become an artifact somebody has to explain.
func isRecording(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".webm", ".zip", ".png":
		return true
	default:
		return false
	}
}

// ask sends one protocol command and decodes its reply.
func (s *bundledSession) ask(ctx context.Context, command map[string]any) (artifactReply, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return artifactReply{}, fmt.Errorf("encode a session command: %w", err)
	}

	line, err := s.session.Send(ctx, encoded)
	if err != nil {
		return artifactReply{}, err
	}

	var reply artifactReply
	if err := json.Unmarshal(line, &reply); err != nil {
		return artifactReply{}, fmt.Errorf("the browser replied with something unreadable: %w", err)
	}
	return reply, nil
}

func (s *bundledSession) Close(ctx context.Context) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	return s.session.Close(ctx)
}
