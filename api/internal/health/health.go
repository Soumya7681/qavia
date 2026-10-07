// Package health answers the liveness and readiness probes.
//
// The two are deliberately different. Liveness touches nothing: a probe that
// fails because Postgres blipped causes a restart loop, which turns a recoverable
// dependency problem into an outage. Readiness checks every dependency the
// process needs to serve traffic, so a deploy fails visibly instead of serving
// errors.
package health

import (
	"context"
	"sync"
	"time"
)

// Status mirrors the CheckStatus enum in the contract.
type Status string

const (
	StatusOK Status = "ok"

	// StatusFailing means a dependency the process needs is unreachable.
	StatusFailing Status = "failing"

	// StatusNotConfigured means an optional integration is absent. It is never a
	// failure: an unconfigured integration reads "not configured, using built-in
	// X" (requirements.md 5.4), and readiness stays green.
	StatusNotConfigured Status = "not_configured"
)

// Check is one dependency's result.
type Check struct {
	Name   string
	Status Status

	// Detail says which implementation answered, or why it failed. It must never
	// contain a credential: it is returned over HTTP.
	Detail string
}

// Probe reports on one dependency.
//
// Required probes fail readiness. Optional ones report not_configured and do not,
// which is what keeps a zero-integration install green.
type Probe struct {
	Name     string
	Optional bool

	// Detail is the implementation name to report on success, for example
	// "local-disk". Optional.
	Detail string

	Check func(ctx context.Context) error
}

// Service runs the probes.
type Service struct {
	version string
	timeout time.Duration
	probes  []Probe
}

// New builds the service. Probes are registered explicitly in main, so reading
// main.go tells you what readiness actually covers.
func New(version string, probes ...Probe) *Service {
	return &Service{
		version: version,
		// A readiness probe that hangs is worse than one that fails: an
		// orchestrator waits instead of routing away.
		timeout: 3 * time.Second,
		probes:  probes,
	}
}

// Version backs the liveness response.
func (s *Service) Version() string { return s.version }

// Readiness runs every probe concurrently and reports whether the process should
// receive traffic.
//
// Concurrently because probes are independent and a serial sweep makes the
// timeout the sum rather than the maximum.
func (s *Service) Readiness(ctx context.Context) (bool, []Check) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	checks := make([]Check, len(s.probes))

	var wg sync.WaitGroup
	for i, probe := range s.probes {
		wg.Add(1)
		go func(i int, probe Probe) {
			defer wg.Done()
			checks[i] = run(ctx, probe)
		}(i, probe)
	}
	wg.Wait()

	ready := true
	for _, check := range checks {
		if check.Status == StatusFailing {
			ready = false
		}
	}
	return ready, checks
}

func run(ctx context.Context, probe Probe) Check {
	check := Check{Name: probe.Name, Status: StatusOK, Detail: probe.Detail}

	if probe.Check == nil {
		check.Status = StatusNotConfigured
		return check
	}

	if err := probe.Check(ctx); err != nil {
		if probe.Optional {
			check.Status = StatusNotConfigured
			check.Detail = "not configured"
			return check
		}
		check.Status = StatusFailing
		// The error text is returned over HTTP. Connection errors are safe and
		// diagnostic; anything holding a credential must not reach a probe error
		// in the first place, which is why probes are constructed in main from
		// already-open handles rather than from connection strings.
		check.Detail = err.Error()
	}
	return check
}
