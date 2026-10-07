// Package runner executes generated test suites in throwaway containers.
//
// This package is a security boundary rather than a feature. What it runs is
// model-generated code derived from a file a client uploaded, so the design
// assumption is that the code inside the container is hostile: it may try to read
// the host, reach the cloud metadata endpoint, exhaust the machine, or phone home.
//
// Four properties carry that weight, and each is enforced here rather than trusted
// to the test framework:
//
//   - **One-shot.** A container is created, used once, and destroyed. No reuse and
//     no cached state, so nothing a run leaves behind can reach the next one.
//   - **Deny outbound by default.** The network is created per run with no route
//     out, and only an allowlisted target changes that (BE-4.4).
//   - **Limits from settings**, applied as cgroup and kernel limits: CPU, memory,
//     process count, and a wall clock the driver enforces itself (BE-4.5).
//   - **Reaped twice.** Once in a defer, and again by a sweeper, because a worker
//     killed mid-run cannot run its own defers.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Runtime is the container runtime a run uses.
//
// gVisor is the default because Docker alone is not a sufficient boundary for
// running untrusted code: it intercepts syscalls in userspace, so a container
// escape has to get through the sentry first (tech-stack.md 7). runc exists for a
// developer's laptop and is labelled unsafe wherever it appears.
type Runtime string

const (
	RuntimeGVisor Runtime = "runsc"
	RuntimeRunc   Runtime = "runc"
)

// RuntimeFor maps the settings value onto the runtime name Docker expects.
//
// The setting says "gvisor" because that is what an operator recognises; Docker
// wants the binary's name, "runsc". Anything unrecognised resolves to gVisor rather
// than to runc: a typo in a setting must not silently downgrade the boundary.
func RuntimeFor(setting string) Runtime {
	switch strings.ToLower(strings.TrimSpace(setting)) {
	case "runc":
		return RuntimeRunc
	default:
		return RuntimeGVisor
	}
}

// Unsafe reports whether this runtime is a real boundary for untrusted code.
//
// runc shares the host kernel, so a container escape is a host compromise. It exists
// here for a developer's laptop and is labelled wherever it appears.
func (r Runtime) Unsafe() bool { return r == RuntimeRunc }

// Limits bound one run. Every value comes from settings, so an operator tightens
// them without a deploy.
type Limits struct {
	CPUs      float64
	MemoryMiB int64

	// PIDs stops a fork bomb from taking the host down with the container.
	PIDs int64

	// Timeout is enforced by the driver rather than by the test framework: a
	// framework that has hung is not going to notice its own timeout.
	Timeout time.Duration

	// TmpfsMiB is the writable space a run gets. The root filesystem is read-only,
	// so this is where a reporter writes its output.
	TmpfsMiB int64
}

func (l Limits) withDefaults() Limits {
	if l.CPUs <= 0 {
		l.CPUs = 2
	}
	if l.MemoryMiB <= 0 {
		l.MemoryMiB = 1024
	}
	if l.PIDs <= 0 {
		l.PIDs = 256
	}
	if l.Timeout <= 0 {
		l.Timeout = 15 * time.Minute
	}
	if l.TmpfsMiB <= 0 {
		l.TmpfsMiB = 256
	}
	return l
}

// Egress is what a run may reach.
//
// Empty means nothing: no DNS, no route, no metadata endpoint. That is the default
// and it is a network-level property, not a check in application code (F-7.4).
type Egress struct {
	// AllowedHosts are hostnames the run may reach, already checked against the
	// project's allowlist and resolved.
	AllowedHosts []HostAddress
}

// HostAddress is an allowlisted hostname and the address it resolved to.
//
// Both, because the container is given a fixed host entry rather than a resolver:
// pinning the name to the address the platform checked closes the window where a
// second lookup inside the container returns something else (BE-4.7).
type HostAddress struct {
	Host string
	IP   string
}

// Spec is one run.
type Spec struct {
	// RunID is stamped on the container name and on every command log row, so a
	// container found on a host traces back to a run.
	RunID string

	Image string

	// Command is what the container runs. It is logged verbatim, after redaction.
	Command []string

	// Workspace is the files to materialise inside the container, keyed by
	// repository-relative path. Written into the container rather than mounted from
	// the host: a writable host mount is a hole through the boundary (BE-4.9).
	Workspace map[string]string

	// Env is the environment the suite reads its target and credentials from. It is
	// the only channel for a credential, and it never reaches the command log.
	Env map[string]string

	Limits Limits
	Egress Egress

	Runtime Runtime

	// Reports are workspace-relative paths to copy back out when the run ends, such
	// as a JSON reporter's output. Collected before the container is reaped, because
	// after that there is nothing left to read (BE-4.9.2).
	Reports []string
}

// Result is what a run produced.
type Result struct {
	ExitCode int

	// Logs is the combined output, already truncated to the configured ceiling: an
	// infinite loop printing to stdout must not become memory pressure.
	Logs string

	Duration time.Duration

	// Reports are the files the run wrote that the caller asked for, such as a JSON
	// reporter's output.
	Reports map[string][]byte

	// TimedOut and OOMKilled are separated from a non-zero exit because "killed:
	// memory limit" is an answer and "exit 137" is a riddle (BE-4.5).
	TimedOut  bool
	OOMKilled bool
}

// Killed reports whether the platform stopped the run rather than the suite
// finishing.
func (r Result) Killed() bool { return r.TimedOut || r.OOMKilled }

// Reason explains a non-zero outcome in words a user can act on.
func (r Result) Reason() string {
	switch {
	case r.TimedOut:
		return "killed: wall clock limit reached"
	case r.OOMKilled:
		return "killed: memory limit reached"
	case r.ExitCode != 0:
		return fmt.Sprintf("the suite exited %d", r.ExitCode)
	default:
		return ""
	}
}

// Driver runs a spec.
//
// An interface so the execute job can be exercised without Docker, and because the
// runtime is a deployment decision: a host with gVisor and a developer's laptop
// present the same surface here.
type Driver interface {
	// Run executes the spec to completion, or until the context is cancelled.
	Run(ctx context.Context, spec Spec, logs io.Writer) (Result, error)

	// Sweep removes containers a previous process left behind. It is the second
	// half of reaping: a defer cannot run in a process that was killed.
	Sweep(ctx context.Context, olderThan time.Duration) (int, error)

	// Available reports whether the runtime is reachable, for the readiness probe.
	Available(ctx context.Context) bool
}

// SessionDriver is a driver that can also hold a container open and talk to it.
//
// Separate from Driver because most of the platform wants one-shot execution and
// should not be handed a way to keep a container alive: a session is the exception
// browsers need, and an exception is easier to reason about when it is named
// (BE-7.1).
type SessionDriver interface {
	Driver

	Open(ctx context.Context, spec SessionSpec) (*Session, error)
}

// ErrUnavailable is returned when the container runtime cannot be reached. It is
// distinguished from a failing run so the job retries rather than reporting the
// suite as broken.
var ErrUnavailable = errors.New("runner: container runtime is unavailable")

// containerPrefix names every container this platform creates.
//
// It is what makes the sweeper safe: it only ever removes containers it can prove
// are ours, so a runner host shared with anything else stays intact.
const containerPrefix = "qavia-run-"

// ContainerName is the name for a run's container.
func ContainerName(runID string) string {
	return containerPrefix + strings.ToLower(runID)
}

// redactEnvInCommand keeps a credential out of the command log.
//
// The command is logged for reconstruction (F-17.2), and a suite invoked with a
// token on its command line would put that token in a table somebody exports. The
// value is replaced rather than the whole argument dropped, so the log still shows
// the shape of what ran.
// RedactedCommand is the exported form, for the command log (BE-4.6).
func RedactedCommand(command []string, env map[string]string) string {
	return redactEnvInCommand(command, env)
}

func redactEnvInCommand(command []string, env map[string]string) string {
	joined := strings.Join(command, " ")

	for key, value := range env {
		if value == "" || !isSecretKey(key) {
			continue
		}
		joined = strings.ReplaceAll(joined, value, "[redacted]")
	}
	return joined
}

func isSecretKey(key string) bool {
	lowered := strings.ToLower(key)
	for _, marker := range []string{"token", "secret", "password", "key", "credential"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
