package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// workspaceRoot is where a suite is materialised inside the container. It matches
// the WORKDIR baked into the runner images.
const workspaceRoot = "/workspace"

// cleanupTimeout bounds the removal of a run's network and firewall rules. It runs
// on a context detached from the run, so it needs its own limit.
const cleanupTimeout = 60 * time.Second

// maxLogBytes caps what is kept from a run's output.
//
// A suite in an infinite loop printing to stdout is a plausible outcome of
// generated code, and holding all of it would turn that into memory pressure on
// the worker. The tail is what a failure needs anyway.
const maxLogBytes = 4 << 20

// Docker runs specs as one-shot containers.
type Docker struct {
	client *client.Client

	// helperImage carries iptables for the firewall helper (network.go). It is a
	// field rather than a constant because an air-gapped runner host mirrors it
	// somewhere else.
	helperImage string
}

// defaultHelperImage is the image the firewall helper runs. Small, and the only
// thing it needs is iptables.
const defaultHelperImage = "ghcr.io/hyscaler/qavia-net-helper:build"

// NewDocker connects to the local daemon.
//
// From the environment, so a runner host that talks to a remote or rootless daemon
// is configuration rather than a code change.
func NewDocker(helperImage string) (*Docker, error) {
	api, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("connect to the container runtime: %w", err)
	}
	if helperImage == "" {
		helperImage = defaultHelperImage
	}
	return &Docker{client: api, helperImage: helperImage}, nil
}

func (d *Docker) Close() error { return d.client.Close() }

// Available reports whether the daemon answers, for readiness.
func (d *Docker) Available(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := d.client.Ping(pingCtx, client.PingOptions{})
	return err == nil
}

// Run creates a container, feeds it the workspace, waits for it, and destroys it.
//
// The order is deliberate. The container is created without a network, the files
// are copied in, and only then is it started: a container that is running before
// its workspace exists is a container that could be reading something else.
func (d *Docker) Run(ctx context.Context, spec Spec, logs io.Writer) (Result, error) {
	spec.Limits = spec.Limits.withDefaults()
	if spec.Runtime == "" {
		spec.Runtime = RuntimeGVisor
	}

	if !d.Available(ctx) {
		return Result{}, ErrUnavailable
	}

	// The wall clock is the driver's, not the framework's. A hung suite does not
	// notice its own timeout, and this context is what kills the container.
	runCtx, cancel := context.WithTimeout(ctx, spec.Limits.Timeout)
	defer cancel()

	name := ContainerName(spec.RunID)

	// Built before the container exists: a workspace this platform will not accept
	// should fail before anything is created.
	archive, err := workspaceArchive(spec.Workspace)
	if err != nil {
		return Result{}, err
	}

	// A run with an allowlisted target gets a network of its own with firewall rules
	// that permit exactly that target (network.go). A run with no target gets no
	// network at all, which is the stronger case and needs no rules.
	var prepared runNetwork
	if len(spec.Egress.AllowedHosts) > 0 {
		prepared, err = d.prepareNetwork(runCtx, spec)
		if err != nil {
			return Result{}, err
		}

		// Rules and network go on the way out, and the sweeper repeats both if this
		// process never gets here.
		//
		// On a context that outlives the run's own: a cancelled run is exactly when
		// this cleanup matters, and a removal issued on the cancelled context is a
		// removal that does not happen.
		defer func() {
			cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancelCleanup()

			d.removeEgressRules(cleanupCtx, spec.RunID, prepared.Bridge)
			d.removeNetwork(cleanupCtx, prepared.ID)
		}()
	}

	created, err := d.client.ContainerCreate(runCtx, client.ContainerCreateOptions{
		Name:             name,
		Config:           d.containerConfig(spec),
		HostConfig:       d.hostConfig(spec, prepared),
		NetworkingConfig: networkConfigFor(prepared),
	})
	if err != nil {
		return Result{}, fmt.Errorf("create runner container: %w", err)
	}

	// Reaped in a defer on the way out, and by the sweeper if this process dies
	// before it gets here.
	defer d.reap(ctx, created.ID)

	started := time.Now()
	if _, err := d.client.ContainerStart(runCtx, created.ID, client.ContainerStartOptions{}); err != nil {
		return Result{}, fmt.Errorf("start runner container: %w", err)
	}

	// The container is already running its extractor, which is blocked on stdin, so
	// it has read nothing and can do nothing until the workspace arrives.
	if err := d.sendWorkspace(runCtx, created.ID, archive); err != nil {
		return Result{}, err
	}

	// Streaming starts before the wait so a viewer sees output while the suite runs
	// rather than all of it at the end (BE-4.11).
	//
	// The splitter sits in front of both sinks: report blocks are pulled out of the
	// stream and never reach the log a person reads, which keeps a 200 KB JSON report
	// out of the middle of the output.
	captured := &boundedBuffer{limit: maxLogBytes}
	splitter := newReportSplitter(io.MultiWriter(captured, logs))
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		d.stream(runCtx, created.ID, splitter)
	}()

	result := Result{}
	wait := d.client.ContainerWait(runCtx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})

	select {
	case waitErr := <-wait.Error:
		if waitErr != nil {
			if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
				result.TimedOut = true
			} else if ctx.Err() != nil {
				// Cancelled from outside: the run was stopped deliberately, and the
				// defer above still reaps the container.
				return result, ctx.Err()
			} else {
				return result, fmt.Errorf("wait for runner container: %w", waitErr)
			}
		}
	case status := <-wait.Result:
		result.ExitCode = int(status.StatusCode)
	}

	// The inspect happens before the reap, because it is what distinguishes "killed:
	// memory limit" from a bare exit code.
	inspected, err := d.client.ContainerInspect(context.WithoutCancel(ctx), created.ID,
		client.ContainerInspectOptions{})
	if err == nil && inspected.Container.State != nil {
		result.OOMKilled = inspected.Container.State.OOMKilled
		if inspected.Container.State.ExitCode != 0 {
			result.ExitCode = inspected.Container.State.ExitCode
		}
	}

	if result.TimedOut {
		// Stopped rather than killed outright, so a reporter that flushes on SIGTERM
		// still writes what it had.
		timeout := 5
		if _, err := d.client.ContainerStop(context.WithoutCancel(ctx), created.ID,
			client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			slog.WarnContext(ctx, "stop timed-out container", "run_id", spec.RunID, "error", err)
		}
	}

	<-streamDone

	result.Logs = captured.String()
	result.Reports = splitter.Reports()
	result.Duration = time.Since(started)
	return result, nil
}

// containerConfig is what runs, and as whom.
func (d *Docker) containerConfig(spec Spec) *container.Config {
	env := make([]string, 0, len(spec.Env))
	for key, value := range spec.Env {
		env = append(env, key+"="+value)
	}

	return &container.Config{
		Image: spec.Image,

		// The suite is not the entry point: the workspace arrives on stdin, so the
		// container extracts it and then execs what it was actually asked to run.
		// Entrypoint is cleared because the images set one, and this has to be the
		// first thing that runs.
		Entrypoint: []string{"sh", "-c", extractThenRun(spec.Command, spec.Reports)},

		Env:        env,
		WorkingDir: workspaceRoot,

		// Non-root, and not by convention: the images bake the user in, and this
		// says so again so an image that forgot still runs unprivileged.
		User: "1000:1000",

		AttachStdout: true,
		AttachStderr: true,

		// Stdin is open for exactly one purpose: receiving the workspace archive,
		// which is closed immediately afterwards. StdinOnce is what guarantees the
		// suite cannot then sit waiting for input, which would be a run that hangs
		// until the wall clock kills it.
		OpenStdin:   true,
		StdinOnce:   true,
		AttachStdin: true,
		Tty:         false,

		Labels: map[string]string{
			// The sweeper only touches containers carrying this, so a shared host is
			// safe from it.
			"qavia.run":    spec.RunID,
			"qavia.owner":  "qavia",
			"qavia.reaped": "pending",
		},
	}
}

// extractThenRun builds the shell line the container starts with.
//
// Two problems are solved here, and both come from the same decision: the workspace
// is a tmpfs inside a container with a read-only root filesystem, and no part of the
// host is mounted into it.
//
//   - **Getting the suite in.** Docker refuses an archive copy into a container whose
//     rootfs is read-only, even when the destination is a tmpfs. Dropping the
//     read-only rootfs would trade a real boundary for a convenience, so the archive
//     arrives on stdin and the container untars it itself.
//   - **Getting the report out.** A tmpfs is unmounted when the container stops, so a
//     report read after the run is a report that no longer exists. Reading it while
//     the container is alive is a race against its own exit, so the container prints
//     it instead, framed by markers the driver splits back out of the log stream.
//
// The report is the suite's own account of what it did, and that is all it ever was:
// generated code that wanted to lie about its results could do so by printing a
// passing report, with or without this mechanism. The isolation is the control; the
// report is data (BE-4.9.2).
//
// `tar` is present in every runner image: the Debian-based ones ship GNU tar and the
// Alpine-based one has busybox tar.
func extractThenRun(command []string, reports []string) string {
	quoted := make([]string, 0, len(command))
	for _, argument := range command {
		quoted = append(quoted, shellQuote(argument))
	}

	script := &strings.Builder{}

	// A missing archive is not fatal: a run whose image carries everything it needs
	// has an empty workspace, and tar reading an empty stdin exits zero.
	fmt.Fprintf(script, "tar -xf - -C %s 2>/dev/null || true\n", shellQuote(workspaceRoot))

	// The suite's exit code is kept and re-raised at the end, because it is what
	// distinguishes "the suite ran and failed" from "the suite could not run", and
	// printing the report must not overwrite it.
	fmt.Fprintf(script, "%s\nqavia_status=$?\n", strings.Join(quoted, " "))

	for _, report := range reports {
		fmt.Fprintf(script, "if [ -f %s ]; then\n", shellQuote(report))
		fmt.Fprintf(script, "  printf '%%s\\n' %s\n", shellQuote(reportBegin+report+reportSuffix))
		fmt.Fprintf(script, "  cat %s\n", shellQuote(report))
		fmt.Fprintf(script, "  printf '\\n%%s\\n' %s\n", shellQuote(reportEnd))
		fmt.Fprintf(script, "fi\n")
	}

	fmt.Fprintf(script, "exit $qavia_status\n")
	return script.String()
}

// The markers framing a report in the container's output. They are deliberately
// unlikely to appear in a test's own output, and a block that does not close is
// discarded rather than parsed.
const (
	reportBegin  = "=== QAVIA-REPORT:"
	reportSuffix = " ==="
	reportEnd    = "=== QAVIA-REPORT-END ==="
)

// shellQuote wraps a value so a shell treats it as one literal argument.
//
// Single quotes, with an embedded quote spliced rather than escaped, because that is
// the only form POSIX sh guarantees: a test name containing a space, a dollar sign,
// or a backtick has to reach the framework exactly as written.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// hostConfig is the boundary: limits, filesystem, and network mode.
func (d *Docker) hostConfig(spec Spec, prepared runNetwork) *container.HostConfig {
	config := &container.HostConfig{
		Runtime: string(spec.Runtime),

		// Nothing is kept. A run that failed leaves no container to inspect on the
		// host, which is deliberate: its logs and artifacts are already in object
		// storage, and a stopped container is a thing somebody eventually reuses.
		AutoRemove: false,

		ReadonlyRootfs: true,

		// The only writable space, and it is capped: a run that fills the disk takes
		// the host's other work with it.
		// mode=1777 because the container runs as uid 1000 and a tmpfs Docker
		// mounts is root-owned and mode 755 by default, which makes the only
		// writable directory in the container unwritable. Verified by running the
		// image: without it the suite fails on its first file write.
		Tmpfs: map[string]string{
			workspaceRoot: fmt.Sprintf("rw,noexec,nosuid,mode=1777,size=%dm", spec.Limits.TmpfsMiB),
			"/tmp":        fmt.Sprintf("rw,noexec,nosuid,mode=1777,size=%dm", spec.Limits.TmpfsMiB),
		},

		Resources: container.Resources{
			NanoCPUs:  int64(spec.Limits.CPUs * 1e9),
			Memory:    spec.Limits.MemoryMiB << 20,
			PidsLimit: &spec.Limits.PIDs,

			// Swap equal to memory means no swap: without this a memory limit is a
			// suggestion, because the kernel spills to disk instead of killing.
			MemorySwap: spec.Limits.MemoryMiB << 20,
		},

		SecurityOpt: []string{
			// No privilege escalation, so a setuid binary in the image cannot lift
			// the run out of its user.
			"no-new-privileges",
		},

		CapDrop: []string{"ALL"},

		// No host mounts at all. The workspace is copied in, because a writable host
		// mount is a hole straight through everything above (BE-4.9).
		Mounts: []mount.Mount{},

		// The one-shot rule again: a container that restarts is a container that
		// outlives its run.
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	if len(spec.Egress.AllowedHosts) == 0 {
		// Default deny, at the network layer: no interface, so no route, no DNS, and
		// no metadata endpoint to block in the first place (F-7.4, F-7.5).
		config.NetworkMode = "none"
		return config
	}

	// With a target, the container joins the network prepared for this run, whose
	// firewall chain accepts the allowlisted addresses and rejects the rest
	// (network.go). The name is pinned to the address the platform already checked,
	// so no resolver is needed inside the container and a second lookup cannot move
	// the target.
	config.NetworkMode = container.NetworkMode(prepared.Name)
	for _, allowed := range spec.Egress.AllowedHosts {
		config.ExtraHosts = append(config.ExtraHosts, allowed.Host+":"+allowed.IP)
	}

	// The metadata endpoint is named as well as filtered. The chain drops the
	// address, and this makes the name resolve nowhere, so a suite that looks it up
	// gets a clear failure instead of a hang.
	config.ExtraHosts = append(config.ExtraHosts,
		"metadata.google.internal:127.0.0.1",
		"metadata:127.0.0.1",
		"instance-data:127.0.0.1")

	return config
}

// workspaceArchive renders the suite as a tar stream.
//
// Deterministic: sorted names and a fixed modification time, so the same suite
// produces the same bytes. That is not cosmetic here, it is what makes a re-run
// comparable to the run before it.
func workspaceArchive(files map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cleaned := path.Clean(strings.TrimPrefix(name, "/"))
		if cleaned == "." || strings.HasPrefix(cleaned, "..") {
			return nil, fmt.Errorf("runner: refusing to write %q into the workspace", name)
		}

		content := files[name]
		header := &tar.Header{
			Name:    cleaned,
			Mode:    0o640,
			Size:    int64(len(content)),
			ModTime: time.Unix(0, 0),
		}
		if err := archive.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("stage %s for the runner: %w", cleaned, err)
		}
		if _, err := io.WriteString(archive, content); err != nil {
			return nil, fmt.Errorf("stage %s for the runner: %w", cleaned, err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("finish the runner workspace archive: %w", err)
	}
	return buffer.Bytes(), nil
}

// sendWorkspace writes the archive to the container's stdin and closes it.
//
// The close is the important half. The container's first command is a tar reading
// stdin, so it blocks until end of file: a workspace that is written but never
// closed is a run that hangs at zero percent.
func (d *Docker) sendWorkspace(ctx context.Context, containerID string, archive []byte) error {
	attached, err := d.client.ContainerAttach(ctx, containerID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
	})
	if err != nil {
		return fmt.Errorf("attach to the runner: %w", err)
	}
	defer attached.Close()

	if _, err := attached.Conn.Write(archive); err != nil {
		return fmt.Errorf("write the workspace into the runner: %w", err)
	}
	if err := attached.CloseWrite(); err != nil {
		return fmt.Errorf("close the runner workspace stream: %w", err)
	}
	return nil
}

// readFile copies one file out of a live container as a tar stream of one entry.
//
// Only useful while the container is running: the workspace is a tmpfs, so after the
// container stops there is nothing to read. A run's reports therefore come out through
// its log stream, and this exists for sessions, which are alive when their artifacts
// are collected (BE-7.1).
func (d *Docker) readFile(ctx context.Context, containerID, filename string) ([]byte, error) {
	copied, err := d.client.CopyFromContainer(ctx, containerID,
		client.CopyFromContainerOptions{SourcePath: path.Join(workspaceRoot, filename)})
	if err != nil {
		return nil, fmt.Errorf("read %s from the session: %w", filename, err)
	}
	defer func() {
		if err := copied.Content.Close(); err != nil {
			slog.WarnContext(ctx, "close session file stream", "file", filename, "error", err)
		}
	}()

	archive := tar.NewReader(copied.Content)
	for {
		header, err := archive.Next()
		if err != nil {
			return nil, fmt.Errorf("read %s from the session: %w", filename, err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		// Bounded: an artifact is a trace or a video, and one that does not fit this is
		// one the platform will not hold in memory to hand to an object store.
		content, err := io.ReadAll(io.LimitReader(archive, maxArtifactBytes))
		if err != nil {
			return nil, fmt.Errorf("read %s from the session: %w", filename, err)
		}
		return content, nil
	}
}

// maxArtifactBytes caps one collected artifact. A minute of 1280x720 video is a few
// megabytes; past this the platform is being handed something else.
const maxArtifactBytes = 128 << 20

// stream copies container output into the writer until it ends or the context is
// cancelled.
func (d *Docker) stream(ctx context.Context, containerID string, out io.Writer) {
	reader, err := d.client.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "stream runner logs", "error", err)
		}
		return
	}
	defer func() {
		if err := reader.Close(); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "close runner log stream", "error", err)
		}
	}()

	// The multiplexed stream carries an 8-byte header per frame. Demultiplexing
	// keeps the header bytes out of a log somebody reads.
	if err := demultiplex(reader, out); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "read runner logs", "error", err)
	}
}

// reap removes a container, whatever state it is in.
//
// Force, because a container that ignored a stop is exactly the one that must not
// be left behind, and on a context that survives cancellation: reaping is the
// thing that must still happen when everything else was abandoned.
func (d *Docker) reap(ctx context.Context, containerID string) {
	removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if _, err := d.client.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	}); err != nil && !cerrdefs.IsNotFound(err) {
		// Logged rather than returned: the run's result is already decided, and the
		// sweeper is the second chance.
		slog.ErrorContext(ctx, "reap runner container",
			"container", containerID, "error", err)
	}
}

// Sweep removes containers a previous process left behind (BE-4.2).
//
// The second half of reaping. A worker killed mid-run cannot run its defers, so
// something has to notice the orphan later; without this, a crash loop leaves a
// host full of stopped containers holding disk.
func (d *Docker) Sweep(ctx context.Context, olderThan time.Duration) (int, error) {
	listed, err := d.client.ContainerList(ctx, client.ContainerListOptions{
		All: true,
		// Only ours. A runner host shared with anything else is untouched by this.
		Filters: client.Filters{}.Add("label", "qavia.owner=qavia"),
	})
	if err != nil {
		return 0, fmt.Errorf("list runner containers: %w", err)
	}

	cutoff := time.Now().Add(-olderThan).Unix()

	var swept int
	for _, found := range listed.Items {
		if found.Created > cutoff {
			// Young enough that it may belong to a run in flight in another process.
			continue
		}
		d.reap(ctx, found.ID)
		swept++
	}

	// Networks and firewall rules are swept too, and separately, because a container
	// can be gone while its network survives: a worker killed between the two, or a
	// removal issued on a context that was already cancelled, leaves exactly that.
	swept += d.sweepNetworks(ctx, olderThan)

	return swept, nil
}

// sweepNetworks removes run networks nothing is attached to any more, along with the
// firewall chain each one carried.
//
// The age check is what makes it safe to run while other workers are executing: a
// network younger than the cutoff may belong to a run that has created its network
// and not yet started its container.
func (d *Docker) sweepNetworks(ctx context.Context, olderThan time.Duration) int {
	listed, err := d.client.NetworkList(ctx, client.NetworkListOptions{
		Filters: client.Filters{}.Add("label", "qavia.owner=qavia"),
	})
	if err != nil {
		slog.WarnContext(ctx, "list runner networks", "error", err)
		return 0
	}

	cutoff := time.Now().Add(-olderThan)

	var swept int
	for _, found := range listed.Items {
		if found.Created.After(cutoff) {
			continue
		}

		inspected, err := d.client.NetworkInspect(ctx, found.ID, client.NetworkInspectOptions{})
		if err == nil && len(inspected.Network.Containers) > 0 {
			// Something is still attached, so a run is still using it.
			continue
		}

		// The run ID is on the network's label, which is what lets the chain be
		// removed by name rather than guessed at.
		runID := found.Labels["qavia.run"]
		if runID != "" {
			d.removeEgressRules(ctx, runID, "")
		}
		d.removeNetwork(ctx, found.ID)
		swept++
	}

	if swept > 0 {
		slog.InfoContext(ctx, "reaped orphaned runner networks", "count", swept)
	}
	return swept
}

// boundedBuffer keeps the first maxLogBytes and counts the rest.
//
// Truncating with a marker rather than growing: the alternative is a suite that
// prints in a loop deciding how much memory the worker uses.
type boundedBuffer struct {
	limit   int
	buffer  bytes.Buffer
	dropped int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.dropped += len(p)
		return len(p), nil
	}
	if len(p) > remaining {
		b.buffer.Write(p[:remaining])
		b.dropped += len(p) - remaining
		return len(p), nil
	}
	return b.buffer.Write(p)
}

func (b *boundedBuffer) String() string {
	if b.dropped == 0 {
		return b.buffer.String()
	}
	return fmt.Sprintf("%s\n[%d more bytes of output were dropped]\n",
		b.buffer.String(), b.dropped)
}

// demultiplex splits Docker's stdout and stderr framing into a plain stream.
func demultiplex(reader io.Reader, out io.Writer) error {
	header := make([]byte, 8)

	for {
		if _, err := io.ReadFull(reader, header); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}

		size := int64(header[4])<<24 | int64(header[5])<<16 | int64(header[6])<<8 | int64(header[7])
		if size <= 0 {
			continue
		}
		if _, err := io.CopyN(out, reader, size); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// reportSplitter pulls report blocks out of a container's output stream.
//
// It is a Writer rather than a post-pass over the captured log for two reasons: the
// log is truncated at a ceiling, and a report that arrives after the ceiling would be
// lost; and the live log a person watches should not have a JSON document dumped into
// the middle of it.
//
// Line-oriented, because the markers are lines. A partial line is held until its
// newline arrives, so a report split across two reads still parses.
type reportSplitter struct {
	out io.Writer

	pending  bytes.Buffer
	reports  map[string][]byte
	current  string
	body     bytes.Buffer
	overflow bool
}

// maxReportBytes caps one report. A 400-test JSON report is well under a megabyte;
// past this the suite is writing something else, and the platform stops collecting
// rather than holding it in memory.
const maxReportBytes = 8 << 20

func newReportSplitter(out io.Writer) *reportSplitter {
	return &reportSplitter{out: out, reports: map[string][]byte{}}
}

func (r *reportSplitter) Write(p []byte) (int, error) {
	r.pending.Write(p)

	for {
		index := bytes.IndexByte(r.pending.Bytes(), '\n')
		if index < 0 {
			break
		}

		line := make([]byte, index+1)
		if _, err := r.pending.Read(line); err != nil {
			break
		}
		r.consume(line)
	}

	// The whole write is always reported as accepted: this is a filter, and a caller
	// writing container output cannot do anything useful with a short write. A failed
	// sink is already handled where it is written to.
	return len(p), nil //nolint:nilerr // a filter never fails its own caller
}

// consume routes one line, newline included.
func (r *reportSplitter) consume(line []byte) {
	trimmed := strings.TrimRight(string(line), "\r\n")

	switch {
	case r.current == "" && strings.HasPrefix(trimmed, reportBegin):
		name := strings.TrimSuffix(strings.TrimPrefix(trimmed, reportBegin), reportSuffix)
		r.current = strings.TrimSpace(name)
		r.body.Reset()
		r.overflow = false

	case r.current != "" && trimmed == reportEnd:
		if !r.overflow {
			r.reports[r.current] = append([]byte(nil), bytes.TrimSpace(r.body.Bytes())...)
		}
		r.current = ""
		r.body.Reset()

	case r.current != "":
		if r.body.Len()+len(line) > maxReportBytes {
			// Stated in the log rather than silently truncated: a report this large
			// means something is wrong with the run, and a half-parsed report would
			// look like a smaller suite.
			if !r.overflow {
				if _, err := fmt.Fprintf(r.out,
					"… report %s exceeded %d bytes and was not collected\n",
					r.current, maxReportBytes); err != nil {
					slog.Warn("write report overflow notice", "report", r.current, "error", err)
				}
			}
			r.overflow = true
			return
		}
		r.body.Write(line)

	default:
		if _, err := r.out.Write(line); err != nil {
			// The log sink is a buffer and a live stream; neither failing is a reason
			// to stop reading the container.
			return
		}
	}
}

// Reports returns what was collected. Called after the stream has ended.
//
// An unterminated block is dropped: a report whose end marker never arrived is a
// report the container did not finish writing, and half of one is worse than none.
func (r *reportSplitter) Reports() map[string][]byte {
	if r.pending.Len() > 0 && r.current == "" {
		// A last line with no newline still belongs in the log.
		if _, err := r.out.Write(r.pending.Bytes()); err == nil {
			r.pending.Reset()
		}
	}
	if len(r.reports) == 0 {
		return nil
	}
	return r.reports
}
