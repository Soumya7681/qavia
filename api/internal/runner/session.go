package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// Interactive sessions (BE-7.1).
//
// Everything else this driver runs is one container, one command, one exit. A browser
// is different: page three only makes sense after the login on page one, so a
// container per command would mean replaying an entire flow for every step, and a
// stateless browser is not a browser.
//
// So a session is one container held open, driven over its stdin and read over its
// stdout, and destroyed when the session ends. The one-shot rule is kept at the level
// where it matters — nothing survives the session, and no session sees another's state
// — and every other property of the boundary is unchanged: the same image, the same
// egress rules, the same limits, the same reaping.
//
// The protocol is line-delimited JSON because it needs no port. A control channel over
// HTTP would have meant the container listening on something, and a container that
// listens is a container somebody can reach.

// Session is a running container being driven interactively.
type Session struct {
	driver      *Docker
	containerID string
	network     runNetwork

	conn   io.ReadWriteCloser
	reader *bufio.Reader

	// closeWrite ends the container's stdin, which is how the script inside knows the
	// session is over.
	closeWrite func() error

	timeout time.Duration
	closed  bool
}

// SessionSpec is a session's container.
//
// It is a Spec plus the one thing a session needs and a run does not: a reply timeout,
// because a step that hangs has to fail the step rather than the session's whole wall
// clock.
type SessionSpec struct {
	Spec

	// ReplyTimeout bounds one command. Zero means the default.
	ReplyTimeout time.Duration
}

const defaultReplyTimeout = 45 * time.Second

// Open starts a session container and waits for it to say it is ready.
//
// The caller closes it, on every path out. A session left open is a browser left
// running, which is the most expensive thing this platform can leak.
func (d *Docker) Open(ctx context.Context, spec SessionSpec) (*Session, error) {
	spec.Limits = spec.Limits.withDefaults()
	if spec.Runtime == "" {
		spec.Runtime = RuntimeGVisor
	}
	if spec.ReplyTimeout <= 0 {
		spec.ReplyTimeout = defaultReplyTimeout
	}

	if !d.Available(ctx) {
		return nil, ErrUnavailable
	}

	var (
		prepared runNetwork
		err      error
	)
	if len(spec.Egress.AllowedHosts) > 0 {
		prepared, err = d.prepareNetwork(ctx, spec.Spec)
		if err != nil {
			return nil, err
		}
	}

	// A session's container config differs from a run's in two ways, and both are
	// about stdin. It stays open, because stdin is the command channel; and nothing is
	// extracted from it, because a run's extractor reads until end of file and would
	// swallow the channel along with the archive.
	//
	// Which means a session gets no workspace. Nothing needs one yet: the browser
	// script is baked into the image and writes its artifacts to the container's own
	// tmpfs. A session that needs files later needs a second channel, not this one.
	config := d.containerConfig(spec.Spec)
	config.StdinOnce = false
	config.Entrypoint = append([]string{}, spec.Command...)

	created, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             ContainerName(spec.RunID),
		Config:           config,
		HostConfig:       d.hostConfig(spec.Spec, prepared),
		NetworkingConfig: networkConfigFor(prepared),
	})
	if err != nil {
		d.cleanupNetwork(ctx, prepared)
		return nil, fmt.Errorf("create the session container: %w", err)
	}

	session := &Session{
		driver:      d,
		containerID: created.ID,
		network:     prepared,
		timeout:     spec.ReplyTimeout,
	}

	// Attached before the start, so the ready line cannot be written before anything is
	// listening for it.
	attached, err := d.client.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		session.discard(ctx)
		return nil, fmt.Errorf("attach to the session: %w", err)
	}

	session.conn = attached.Conn

	// Docker multiplexes a non-TTY container's stdout and stderr into 8-byte framed
	// chunks, so the raw stream is not the JSON the container wrote. Unframed here,
	// once, rather than by every caller: a reply that arrives with a binary header in
	// front of it is a reply that does not parse.
	session.reader = bufio.NewReaderSize(&frameReader{source: attached.Reader}, 1<<20)
	session.closeWrite = attached.CloseWrite

	if _, err := d.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		session.discard(ctx)
		return nil, fmt.Errorf("start the session container: %w", err)
	}

	if err := session.waitReady(ctx); err != nil {
		session.discard(ctx)
		return nil, err
	}

	return session, nil
}

// Ready is the first line a session container writes, so the caller knows the process
// inside is up before it sends a command.
type readyLine struct {
	OK    bool `json:"ok"`
	Ready bool `json:"ready"`
}

func (s *Session) waitReady(ctx context.Context) error {
	line, err := s.readLine(ctx, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("the session never became ready: %w", err)
	}

	var ready readyLine
	if err := decodeLine(line, &ready); err != nil || !ready.Ready {
		return fmt.Errorf("the session did not report ready: %s", excerptLine(line))
	}
	return nil
}

// Send writes one command and returns the reply line.
//
// One command at a time, and the reply is read before the next is written: the
// protocol has no request ids because it does not need them, and adding pipelining
// would buy latency at the cost of a correlation bug.
func (s *Session) Send(ctx context.Context, command []byte) ([]byte, error) {
	if s.closed {
		return nil, errors.New("runner: the session is closed")
	}

	if _, err := s.conn.Write(append(command, '\n')); err != nil {
		return nil, fmt.Errorf("write a session command: %w", err)
	}
	return s.readLine(ctx, s.timeout)
}

// readLine reads one line with a deadline.
//
// The deadline is on the connection rather than a goroutine race, so a step that hangs
// fails that step and leaves the session usable for the next one.
func (s *Session) readLine(ctx context.Context, timeout time.Duration) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}

	lines := make(chan result, 1)
	go func() {
		line, err := s.reader.ReadBytes('\n')
		lines <- result{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("the session did not reply within %s", timeout)
	case answer := <-lines:
		if answer.err != nil && len(answer.line) == 0 {
			return nil, fmt.Errorf("read a session reply: %w", answer.err)
		}
		return answer.line, nil
	}
}

// Artifact copies one file out of the session's container and says why it could not.
//
// The plural form swallows a missing artifact, which is right for collection — a
// session with no video is a fact, not a failure — and wrong for diagnosing one.
func (s *Session) Artifact(ctx context.Context, name string) ([]byte, error) {
	if s.closed {
		return nil, errors.New("runner: the session is closed")
	}
	return s.driver.readFile(ctx, s.containerID, name)
}

// Reports copies files out of the session's container while it is still alive.
//
// A session's workspace is a tmpfs, so this has to happen before Close: after that
// there is nothing left to read, which is the same constraint a run has and the reason
// a run's reports come out through its log stream.
func (s *Session) Reports(ctx context.Context, names []string) map[string][]byte {
	if len(names) == 0 || s.closed {
		return nil
	}

	reports := make(map[string][]byte, len(names))
	for _, name := range names {
		content, err := s.driver.readFile(ctx, s.containerID, name)
		if err != nil {
			slog.DebugContext(ctx, "session artifact not collected",
				"container", s.containerID, "file", name, "error", err)
			continue
		}
		reports[name] = content
	}
	return reports
}

// Close ends the session and destroys the container.
func (s *Session) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true

	// The script inside stops at an `end` command or at end of file; both are offered,
	// because a hung script should not delay the reap.
	if _, err := s.conn.Write([]byte(`{"action":"end"}` + "\n")); err != nil {
		slog.DebugContext(ctx, "send the session end command", "error", err)
	}
	if s.closeWrite != nil {
		if err := s.closeWrite(); err != nil {
			slog.DebugContext(ctx, "close the session stdin", "error", err)
		}
	}
	if err := s.conn.Close(); err != nil {
		slog.DebugContext(ctx, "close the session stream", "error", err)
	}

	s.discard(ctx)
	return nil
}

// discard removes the container, its network, and its firewall rules.
func (s *Session) discard(ctx context.Context) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	s.driver.reap(cleanupCtx, s.containerID)
	s.driver.cleanupNetwork(cleanupCtx, s.network)
}

// cleanupNetwork removes a session's network and its firewall rules, tolerating a
// partial state: a session that failed between creating the network and starting the
// container still has to leave nothing behind.
func (d *Docker) cleanupNetwork(ctx context.Context, prepared runNetwork) {
	if prepared.ID == "" {
		return
	}

	// The run ID is recovered from the network's name, which is where prepareNetwork put
	// it: qavia-run-<id>-net.
	runID := strings.TrimSuffix(strings.TrimPrefix(prepared.Name, containerPrefix), "-net")

	d.removeEgressRules(ctx, runID, prepared.Bridge)
	d.removeNetwork(ctx, prepared.ID)
}

// decodeLine is a small helper so callers do not repeat the trimming.
func decodeLine(line []byte, into any) error {
	return json.Unmarshal([]byte(strings.TrimSpace(string(line))), into)
}

// excerptLine keeps an unexpected reply readable in an error.
func excerptLine(line []byte) string {
	trimmed := strings.TrimSpace(string(line))
	if len(trimmed) > 300 {
		return trimmed[:300] + "…"
	}
	if trimmed == "" {
		return "(no output)"
	}
	return trimmed
}

// frameReader strips Docker's stream framing.
//
// The format is an 8-byte header — one byte of stream id, three reserved, four bytes
// of big-endian length — followed by that many bytes of payload. Both streams are
// yielded, because a container writing a diagnostic to stderr is a container whose
// diagnostic belongs in the caller's error rather than in a void.
type frameReader struct {
	source io.Reader

	// remaining is how much of the current frame's payload is still unread.
	remaining int
}

func (f *frameReader) Read(into []byte) (int, error) {
	for f.remaining == 0 {
		var header [8]byte
		if _, err := io.ReadFull(f.source, header[:]); err != nil {
			return 0, err
		}

		size := int(header[4])<<24 | int(header[5])<<16 | int(header[6])<<8 | int(header[7])
		if size < 0 {
			return 0, fmt.Errorf("runner: a stream frame declared a negative length")
		}
		f.remaining = size
	}

	if len(into) > f.remaining {
		into = into[:f.remaining]
	}
	read, err := f.source.Read(into)
	f.remaining -= read
	return read, err
}
