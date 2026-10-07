// Package perf generates and runs load tests (F-11.1, F-11.2, BE-9.1).
//
// A load test shares the execution boundary with every other run — the same container
// rules, the same egress allowlist, the same reaping — and differs in two ways that
// matter. The script is a k6 script the model wrote under a profile a person chose, and
// the result is a latency-and-throughput series rather than a pass/fail per test.
//
// The profile is the important input, and it is deliberately not the model's to decide.
// It says how many virtual users, how long to ramp, how long to hold — which is to say,
// how hard to hit somebody's server. That is a person's authorisation, made in the UI,
// and the agent is told to use the numbers exactly (BE-9.1.1).
package perf

import (
	"encoding/json"
	"fmt"
)

// Profile is the authorised load.
type Profile struct {
	// VirtualUsers is the peak concurrency, reached at the end of the ramp.
	VirtualUsers int `json:"virtualUsers"`

	// RampUpSeconds is how long to climb to peak, so a target is not hit with full load
	// in the first instant — a spike tests the load balancer, a ramp tests the service.
	RampUpSeconds int `json:"rampUpSeconds"`

	// HoldSeconds is how long to sustain peak, which is where the steady-state
	// percentiles come from.
	HoldSeconds int `json:"holdSeconds"`

	// P95TargetMs and MaxErrorRate become k6 thresholds, so the run has a stated pass
	// condition rather than only numbers to read afterwards.
	P95TargetMs  int     `json:"p95TargetMs"`
	MaxErrorRate float64 `json:"maxErrorRate"`
}

// Validate checks a profile against the installation's ceiling.
//
// Bounded rather than trusted: the profile decides how much traffic this platform
// generates at somebody's server, and a request for a million users is a request to
// refuse before a container starts (BE-9.5).
func (p Profile) Validate(maxVirtualUsers int) error {
	if p.VirtualUsers < 1 {
		return fmt.Errorf("a load test needs at least one virtual user")
	}
	if maxVirtualUsers > 0 && p.VirtualUsers > maxVirtualUsers {
		return fmt.Errorf("%d virtual users is above the %d this project allows",
			p.VirtualUsers, maxVirtualUsers)
	}
	if p.RampUpSeconds < 0 || p.HoldSeconds < 1 {
		return fmt.Errorf("a load test needs a non-negative ramp and a hold of at least one second")
	}
	if p.RampUpSeconds+p.HoldSeconds > maxDurationSeconds {
		return fmt.Errorf("a run of %ds is longer than the %ds ceiling",
			p.RampUpSeconds+p.HoldSeconds, maxDurationSeconds)
	}
	if p.MaxErrorRate < 0 || p.MaxErrorRate > 1 {
		return fmt.Errorf("the error-rate threshold must be a share between 0 and 1")
	}
	return nil
}

// maxDurationSeconds caps one load run. Twenty minutes is long enough to reach steady
// state and short enough that a run nobody remembers starting does not hammer a target
// for an hour.
const maxDurationSeconds = 20 * 60

// WithDefaults fills the fields a caller left at zero with sensible ones.
func (p Profile) WithDefaults() Profile {
	if p.VirtualUsers <= 0 {
		p.VirtualUsers = 10
	}
	if p.HoldSeconds <= 0 {
		p.HoldSeconds = 30
	}
	if p.RampUpSeconds < 0 {
		p.RampUpSeconds = 0
	}
	if p.P95TargetMs <= 0 {
		p.P95TargetMs = 500
	}
	if p.MaxErrorRate <= 0 {
		p.MaxErrorRate = 0.01
	}
	return p
}

// Describe renders the profile for the agent's instruction, as the numbers it must use
// exactly.
func (p Profile) Describe() string {
	return fmt.Sprintf(
		"ramp from 0 to %d virtual users over %ds, then hold %d virtual users for %ds",
		p.VirtualUsers, p.RampUpSeconds, p.VirtualUsers, p.HoldSeconds)
}

// TotalSeconds is the load phase's length, used to bound the container's wall clock and
// to fill the metrics duration k6's summary does not carry.
func (p Profile) TotalSeconds() int { return p.RampUpSeconds + p.HoldSeconds }

// JSON is the profile stored on the run, so a latency series is read against the load
// that produced it.
func (p Profile) JSON() json.RawMessage {
	encoded, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
