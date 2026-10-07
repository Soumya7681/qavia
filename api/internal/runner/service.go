package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Long-lived service containers (BE-8.6.3).
//
// Everything else this driver runs is one container, one command, one exit; a session
// (session.go) is that plus a command channel. A mock server is the third shape and the
// only one that **listens**: a client application has to be able to reach it, which
// means a published port, which is the one property of the phase-4 boundary that has to
// give way.
//
// Everything else stays. Read-only root filesystem, non-root user, dropped
// capabilities, no host mounts, cgroup and pids limits, gVisor, the same labels the
// sweeper keys off. What changes is narrow and stated:
//
//   - **A published port**, bound to the host, because that is the whole purpose.
//   - **No egress.** A mock answers requests; it does not make them. The network is the
//     bridge that carries the published port and nothing outbound is allowlisted, so a
//     mock cannot be used as a way out of the host.
//   - **It outlives the call that started it.** Which makes the lifecycle explicit —
//     start, status, stop — and makes the sweeper's label the backstop it already is
//     for a process that died holding one.

// ServiceSpec is one long-lived container.
type ServiceSpec struct {
	// ID names the service, stamped on the container so a container found on a host
	// traces back to what asked for it.
	ID    string
	Image string

	// Command overrides the image's entry point. Usually empty: a service image has
	// one job.
	Command []string

	Env map[string]string

	// Port is the port the process inside listens on. The host port is chosen by
	// Docker and reported back, because a platform that picked its own would have to
	// track which ones are free.
	Port int

	// Config is written to the container's stdin and stdin is then closed. A mock's
	// routes arrive this way rather than as a file, because the root filesystem is
	// read-only and a config file would need something to write it first.
	Config []byte

	Limits  Limits
	Runtime Runtime
}

// Service is a running service container.
type Service struct {
	ID          string
	ContainerID string

	// HostPort is where the service is reachable on the host running the container.
	HostPort int

	StartedAt time.Time
}

// StartService starts a long-lived container and waits for it to report ready.
//
// The ready line is read from the container's own output rather than by polling the
// port: a port that is not open yet and a process that crashed on its configuration
// look identical from outside, and only one of them is worth waiting for.
func (d *Docker) StartService(ctx context.Context, spec ServiceSpec) (*Service, error) {
	spec.Limits = spec.Limits.withDefaults()
	if spec.Runtime == "" {
		spec.Runtime = RuntimeGVisor
	}
	if spec.Port <= 0 {
		spec.Port = 8080
	}

	if !d.Available(ctx) {
		return nil, ErrUnavailable
	}

	config := d.serviceConfig(spec)
	hostConfig := d.serviceHostConfig(spec)

	created, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       ContainerName(spec.ID),
		Config:     config,
		HostConfig: hostConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("create the service container: %w", err)
	}

	service := &Service{ID: spec.ID, ContainerID: created.ID, StartedAt: time.Now()}

	// Attached before the start, so a configuration error written in the first
	// millisecond is not lost to a race.
	attached, err := d.client.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		d.StopService(ctx, created.ID)
		return nil, fmt.Errorf("attach to the service: %w", err)
	}
	defer func() { _ = attached.Conn.Close() }() //nolint:errcheck // The container owns its own lifetime from here.

	if _, err := d.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		d.StopService(ctx, created.ID)
		return nil, fmt.Errorf("start the service container: %w", err)
	}

	if len(spec.Config) > 0 {
		// One line, and stdin stays open. Closing the write half of a hijacked Docker
		// stream tears down the whole connection, which means the reply this function is
		// waiting for never arrives — found by watching a mock that started correctly and
		// reported no output. The process inside reads exactly one line for this reason.
		if _, err := attached.Conn.Write(append(spec.Config, '\n')); err != nil {
			d.StopService(ctx, created.ID)
			return nil, fmt.Errorf("send the service configuration: %w", err)
		}
	}

	if err := waitServiceReady(ctx, attached.Reader, serviceReadyTimeout); err != nil {
		d.StopService(ctx, created.ID)
		return nil, err
	}

	port, err := d.publishedPort(ctx, created.ID, spec.Port)
	if err != nil {
		d.StopService(ctx, created.ID)
		return nil, err
	}
	service.HostPort = port

	return service, nil
}

// serviceReadyTimeout bounds the wait. A mock server with two hundred routes starts in
// milliseconds; anything past this is a container that is not going to start.
const serviceReadyTimeout = 30 * time.Second

func (d *Docker) serviceConfig(spec ServiceSpec) *container.Config {
	env := make([]string, 0, len(spec.Env)+1)
	for key, value := range spec.Env {
		env = append(env, key+"="+value)
	}
	env = append(env, "QAVIA_MOCK_PORT="+strconv.Itoa(spec.Port))

	config := &container.Config{
		Image: spec.Image,
		Env:   env,
		User:  "1000:1000",

		AttachStdout: true,
		AttachStderr: true,

		// Stdin carries the configuration and is closed straight after. StdinOnce is
		// false because closing it must not stop the container: unlike a run, this
		// process is expected to keep serving afterwards.
		OpenStdin:   true,
		StdinOnce:   false,
		AttachStdin: true,
		Tty:         false,

		ExposedPorts: network.PortSet{servicePort(spec.Port): {}},

		Labels: map[string]string{
			"qavia.run":     spec.ID,
			"qavia.owner":   "qavia",
			"qavia.service": "mock",

			// Not "pending": the sweeper's age rule would otherwise reap a mock somebody
			// deliberately left running, and a mock that disappears after an hour is worse
			// than no mock (BE-8.6.4).
			"qavia.reaped": "service",
		},
	}

	if len(spec.Command) > 0 {
		config.Entrypoint = append([]string{}, spec.Command...)
	}
	return config
}

func (d *Docker) serviceHostConfig(spec ServiceSpec) *container.HostConfig {
	return &container.HostConfig{
		Runtime: string(spec.Runtime),

		ReadonlyRootfs: true,

		Tmpfs: map[string]string{
			"/tmp": fmt.Sprintf("rw,noexec,nosuid,mode=1777,size=%dm", spec.Limits.TmpfsMiB),
		},

		Resources: container.Resources{
			NanoCPUs:   int64(spec.Limits.CPUs * 1e9),
			Memory:     spec.Limits.MemoryMiB << 20,
			MemorySwap: spec.Limits.MemoryMiB << 20,
			PidsLimit:  &spec.Limits.PIDs,
		},

		SecurityOpt: []string{"no-new-privileges"},
		CapDrop:     []string{"ALL"},
		Mounts:      []mount.Mount{},

		// The published port. Bound to every interface because the point is that a
		// client application on the developer's machine, or a generated suite in another
		// container, can reach it; the host port is Docker's choice and is read back
		// after the start.
		PortBindings: network.PortMap{
			servicePort(spec.Port): []network.PortBinding{{HostPort: ""}},
		},

		// No restart. A mock that comes back after the host reboots is a mock nobody
		// remembers starting, and the row that tracks it would be stale.
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}
}

// publishedPort reads the host port Docker assigned.
func (d *Docker) publishedPort(ctx context.Context, containerID string, port int) (int, error) {
	inspected, err := d.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return 0, fmt.Errorf("inspect the service container: %w", err)
	}
	if inspected.Container.NetworkSettings == nil {
		return 0, fmt.Errorf("runner: the service container reported no network settings")
	}

	key := servicePort(port)
	bindings := inspected.Container.NetworkSettings.Ports[key]
	if len(bindings) == 0 {
		return 0, fmt.Errorf("runner: the service published no port for %s", key)
	}

	assigned, err := strconv.Atoi(bindings[0].HostPort)
	if err != nil {
		return 0, fmt.Errorf("runner: the published port %q is not a number", bindings[0].HostPort)
	}
	return assigned, nil
}

// ServiceStatus is what a service container looks like now.
type ServiceStatus struct {
	Running  bool
	ExitCode int
	HostPort int
	Started  time.Time
}

// ServiceStatus inspects a running service.
//
// A container that is gone is reported as not running rather than as an error: a mock
// somebody stopped by hand, or one the host lost in a reboot, is a normal thing for the
// platform to find and the row it belongs to needs correcting rather than an incident.
func (d *Docker) ServiceStatus(ctx context.Context, containerID string) (ServiceStatus, error) {
	inspected, err := d.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			// Gone. A mock somebody stopped by hand, or one the host lost in a reboot, is
			// a normal thing to find: the row needs correcting, not an incident.
			return ServiceStatus{}, nil
		}
		return ServiceStatus{}, fmt.Errorf("inspect the service container: %w", err)
	}

	state := inspected.Container.State
	status := ServiceStatus{Running: state != nil && state.Running}
	if state != nil {
		status.ExitCode = state.ExitCode
		if started, err := time.Parse(time.RFC3339Nano, state.StartedAt); err == nil {
			status.Started = started
		}
	}
	if inspected.Container.NetworkSettings == nil {
		return status, nil
	}
	for _, bindings := range inspected.Container.NetworkSettings.Ports {
		if len(bindings) > 0 {
			if port, err := strconv.Atoi(bindings[0].HostPort); err == nil {
				status.HostPort = port
			}
			break
		}
	}
	return status, nil
}

// StopService stops and removes a service container.
//
// Best effort and idempotent: stopping a mock that is already gone is a success, which
// is what lets the API's stop endpoint be safe to call twice.
func (d *Docker) StopService(ctx context.Context, containerID string) {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	d.reap(stopCtx, containerID)
}

// waitServiceReady reads the container's first line and checks it says ready.
func waitServiceReady(ctx context.Context, reader interface{ Read([]byte) (int, error) }, timeout time.Duration) error {
	type result struct {
		line []byte
		err  error
	}

	lines := make(chan result, 1)
	go func() {
		framed := &frameReader{source: reader}
		buffer := make([]byte, 0, 512)
		chunk := make([]byte, 256)

		for {
			read, err := framed.Read(chunk)
			if read > 0 {
				buffer = append(buffer, chunk[:read]...)
				if index := strings.IndexByte(string(buffer), '\n'); index >= 0 {
					lines <- result{line: buffer[:index]}
					return
				}
			}
			if err != nil {
				lines <- result{line: buffer, err: err}
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return fmt.Errorf("the service did not report ready within %s", timeout)
	case answer := <-lines:
		var ready struct {
			OK    bool `json:"ok"`
			Ready bool `json:"ready"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(answer.line))), &ready); err != nil || !ready.Ready {
			if answer.err != nil {
				return fmt.Errorf("the service stopped before it was ready: %s", excerptLine(answer.line))
			}
			return fmt.Errorf("the service did not report ready: %s", excerptLine(answer.line))
		}
		return nil
	}
}

// servicePort renders a container port for the Docker API.
//
// Parsed rather than constructed, because the type keeps its number and protocol
// private; a malformed port here is a programming error rather than input, so the
// panicking form is the honest one.
func servicePort(port int) network.Port {
	return network.MustParsePort(strconv.Itoa(port) + "/tcp")
}
