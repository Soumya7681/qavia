// Package mocks runs a spec-shaped API a client application can point at (BE-8.6,
// F-10.6 to F-10.8).
//
// The interesting part is what the mock is *for*. A team building a front end against
// an API that does not exist yet needs something that answers in the right shape. A team
// testing error handling needs something that fails on purpose — and that is the part a
// real staging environment cannot give them, because nobody can make staging return a
// 500 for exactly 30% of requests for ten minutes.
//
// Three decisions carry the design:
//
//   - **The responses are generated in Go, not in the container.** Schema-valid payloads
//     come from the same seeded generator everything else uses (internal/datagen), and
//     the container serves what it was handed. So the mock has no dependencies, no npm
//     install, and no second faker to keep in step.
//   - **It is a container like any other**, with one exception it has to have: a
//     published port. Read-only root filesystem, non-root, dropped capabilities, cgroup
//     limits, gVisor, and no outbound network — a mock answers requests, it does not
//     make them (BE-8.6.4).
//   - **Its URL is added to the project's allowlist automatically**, because a generated
//     suite pointed at a target the platform refuses is a suite that cannot run, and
//     making somebody paste the port into settings after every restart is a step they
//     will forget (BE-8.6.5).
package mocks

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status is what a project's mock is doing.
type Status string

const (
	StatusStopped Status = "stopped"
	StatusRunning Status = "running"

	// StatusFailed is a start that did not come up. The reason is on the row, because
	// "it is not running" and "it could not start, and here is why" are different
	// answers to somebody looking at a button that did nothing.
	StatusFailed Status = "failed"
)

// Route is one endpoint the mock serves.
type Route struct {
	// Key identifies the route for the container's response rotation, and is the
	// endpoint's own "METHOD /path".
	Key string `json:"key"`

	Method string `json:"method"`
	Path   string `json:"path"`

	// Responses are the samples the mock rotates through, so a client that lists twice
	// does not see byte-identical data both times and a test asserting on "the second
	// item" is not accidentally passing.
	Responses []Response `json:"responses"`
}

// Response is one sample the mock may return.
type Response struct {
	Status int `json:"status"`

	// Body is a schema-valid payload generated in Go. Kept as a raw value rather than
	// a typed shape, because the shape is the client's schema and this package has no
	// business knowing it.
	Body json.RawMessage `json:"body,omitempty"`
}

// Faults is the fault injection configuration (F-10.7).
type Faults struct {
	// DelayMs delays every response, which is how a client's timeout handling gets
	// exercised without waiting for a slow day in production.
	DelayMs int `json:"delayMs"`

	// FailureRate is the share of requests that get a failure status, between 0 and 1.
	FailureRate float64 `json:"failureRate"`

	// StatusCodes are the failures to choose from. Empty means 500.
	StatusCodes []int `json:"statusCodes,omitempty"`

	// TimeoutRate is the share of requests that get no answer at all, which is a
	// different failure from a 504 and the one that finds missing timeouts in a client.
	TimeoutRate float64 `json:"timeoutRate"`

	// Seed makes the fault pattern reproducible: "30% of requests fail" is otherwise a
	// different 30% every run, and a client test that fails intermittently is
	// indistinguishable from a client bug.
	Seed uint64 `json:"seed"`
}

// Config is what the container receives on stdin.
type Config struct {
	Schema string  `json:"schema"`
	Routes []Route `json:"routes"`
	Faults Faults  `json:"faults"`
}

// ConfigSchema is the version the container checks. A mismatch fails the start rather
// than serving something the image half understands.
const ConfigSchema = "qavia.mock/1"

// Mock is a project's mock server as the platform records it.
type Mock struct {
	ProjectID   uuid.UUID
	ContainerID string
	Status      Status

	// URL is where a client app points, and HostPort is the published port it came
	// from. Stored rather than derived, because the port is Docker's choice.
	URL      string
	HostPort int

	Routes []Route
	Faults Faults

	RouteCount int

	Image string
	Error string

	StartedBy *uuid.UUID
	StartedAt *time.Time
	StoppedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Running reports whether the platform believes the mock is up.
func (m Mock) Running() bool { return m.Status == StatusRunning && m.ContainerID != "" }

// Uptime is how long it has been serving, for a status panel.
func (m Mock) Uptime() time.Duration {
	if !m.Running() || m.StartedAt == nil {
		return 0
	}
	return time.Since(*m.StartedAt)
}

// Validate checks a fault configuration a caller supplied.
//
// Bounded rather than trusted: a delay of an hour would hold a connection open until
// something times out, and a failure rate above one is a caller who meant a percentage.
func (f *Faults) Validate() error {
	if f.DelayMs < 0 || f.DelayMs > maxDelayMs {
		return fmt.Errorf("a delay of %dms is outside the 0 to %d this platform allows",
			f.DelayMs, maxDelayMs)
	}
	if f.FailureRate < 0 || f.FailureRate > 1 {
		return fmt.Errorf("a failure rate of %v is not a share between 0 and 1", f.FailureRate)
	}
	if f.TimeoutRate < 0 || f.TimeoutRate > 1 {
		return fmt.Errorf("a timeout rate of %v is not a share between 0 and 1", f.TimeoutRate)
	}
	if f.FailureRate+f.TimeoutRate > 1 {
		return fmt.Errorf("a failure rate of %v and a timeout rate of %v leave no successful requests",
			f.FailureRate, f.TimeoutRate)
	}

	for _, code := range f.StatusCodes {
		if code < 400 || code > 599 {
			return fmt.Errorf("%d is not a failure status a mock should inject", code)
		}
	}
	return nil
}

// maxDelayMs bounds an injected delay. Thirty seconds is longer than any client's
// timeout worth testing; a minute is a mock somebody forgot they configured.
const maxDelayMs = 30_000
