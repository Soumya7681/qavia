package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// The live log stream (BE-4.11).
//
// It reuses the job event hub rather than adding a second fan-out: the hub is one
// Postgres listener per process serving many browsers, and the run's own job is what
// the worker already notifies when it writes a batch of lines.
//
// Three properties the endpoint has to have, and where each one comes from:
//
//   - **A late joiner sees history.** The stream drains stored lines from id 0 on
//     connect, so a viewer who opened the page ten minutes into a run sees those ten
//     minutes and then live output.
//   - **A reconnect does not leave a gap.** `Last-Event-ID` is the line id, so a
//     dropped connection resumes exactly where it stopped.
//   - **A stalled client cannot affect the run.** Nothing in the run's path waits on
//     this: the worker writes to Postgres and moves on, and a client that is not
//     reading has its connection buffer fill and its stream end. The run does not
//     notice.

// Subscriber is the hub's subscribe half, declared here so this package depends on
// the behaviour rather than on the jobs service.
type Subscriber interface {
	Subscribe(jobID uuid.UUID) (<-chan struct{}, func())
}

const (
	// streamHeartbeat keeps proxies from closing an idle connection. A queued run
	// can be silent for minutes before its container starts.
	streamHeartbeat = 15 * time.Second

	// streamPoll is the backstop for a missed notification. The hub is the fast
	// path; this makes a lost NOTIFY a quarter-second delay rather than a stream
	// that never updates again.
	streamPoll = 2 * time.Second
)

// StreamRunLogs opens the stream.
//
// Access is checked here, before the stream object is built, so an unauthorised
// caller gets a normal JSON error rather than an event stream that says no.
func (h *Handler) StreamRunLogs(
	ctx context.Context,
	request api.StreamRunLogsRequestObject,
) (api.StreamRunLogsResponseObject, error) {
	run, err := h.authorizedRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}

	var lastLineID int64
	if request.Params.LastEventID != nil {
		// An unreadable value resumes from the beginning rather than failing: a
		// reconnect that has to resend the log is a slow recovery, and refusing it is
		// no recovery at all.
		if parsed, parseErr := strconv.ParseInt(*request.Params.LastEventID, 10, 64); parseErr == nil {
			lastLineID = parsed
		}
	}

	return &logStream{
		ctx:        ctx,
		service:    h.service,
		hub:        h.hub,
		run:        run,
		lastLineID: lastLineID,
	}, nil
}

// logStream writes server-sent events until the run finishes or the client leaves.
//
// It satisfies the generated response interface directly rather than using the
// generated text/event-stream response, which takes an io.Reader and would buffer: a
// stream that arrives in one lump at the end is not a live view.
type logStream struct {
	ctx     context.Context
	service *Service
	hub     Subscriber

	run        Run
	lastLineID int64
}

func (s *logStream) VisitStreamRunLogsResponse(w http.ResponseWriter) error {
	flusher, streamable := w.(http.Flusher)
	if !streamable {
		return fmt.Errorf("runs: response writer does not support streaming")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which turns a live stream into a
	// single delivery at the end.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribed before the first read, so a batch written between the read and the
	// subscription still wakes this stream instead of being missed.
	var changed <-chan struct{}
	unsubscribe := func() {}
	if s.hub != nil && s.run.JobID != nil {
		changed, unsubscribe = s.hub.Subscribe(*s.run.JobID)
	}
	defer unsubscribe()

	if err := s.drain(w, flusher); err != nil {
		return err
	}
	if s.run.Status.Terminal() {
		return s.finish(w, flusher)
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()

	poll := time.NewTicker(streamPoll)
	defer poll.Stop()

	for {
		select {
		case <-s.ctx.Done():
			// The client disconnected or the server is shutting down. Returning runs
			// the deferred unsubscribe, which is what stops this leaking a
			// subscription per abandoned tab.
			return nil

		case <-changed:
			if err := s.drain(w, flusher); err != nil {
				return err
			}
			if s.run.Status.Terminal() {
				return s.finish(w, flusher)
			}

		case <-poll.C:
			if err := s.drain(w, flusher); err != nil {
				return err
			}
			if s.run.Status.Terminal() {
				return s.finish(w, flusher)
			}

		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil //nolint:nilerr // a disconnected client is a normal end of stream
			}
			flusher.Flush()
		}
	}
}

// drain sends the run's status and every line the client has not seen.
func (s *logStream) drain(w http.ResponseWriter, flusher http.Flusher) error {
	run, err := s.service.Get(s.ctx, s.run.ID)
	if err != nil {
		// The run was deleted, or the database is unreachable. Either way the stream
		// is over; the error goes to the log through the caller.
		return err
	}
	s.run = run

	if err := writeFrame(w, flusher, "", "status", statusFrame{
		RunID:   run.ID,
		Status:  run.Status,
		Total:   run.Total,
		Passed:  run.Passed,
		Failed:  run.Failed,
		Flaky:   run.Flaky,
		Skipped: run.Skipped,
	}); err != nil {
		return err
	}

	for {
		lines, err := s.service.LogLinesSince(s.ctx, run.ID, s.lastLineID, logPageSize)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			return nil
		}

		for _, line := range lines {
			if err := writeFrame(w, flusher,
				strconv.FormatInt(line.ID, 10), "log", logFrame{
					ID:   strconv.FormatInt(line.ID, 10),
					Line: line.Line,
					At:   line.At,
				}); err != nil {
				return err
			}
			s.lastLineID = line.ID
		}

		if len(lines) < logPageSize {
			return nil
		}
	}
}

// finish closes the stream cleanly and says where the full log lives.
//
// The live tail is pruned once a run's log is stored, so a viewer arriving after
// that gets the outcome and a pointer rather than an empty stream that looks broken.
func (s *logStream) finish(w http.ResponseWriter, flusher http.Flusher) error {
	return writeFrame(w, flusher, "", "done", doneFrame{
		RunID:      s.run.ID,
		Status:     s.run.Status,
		DurationMs: s.run.Duration.Milliseconds(),
		Error:      s.run.Error,
		LogStored:  s.run.LogKey != "",
	})
}

type statusFrame struct {
	RunID   uuid.UUID `json:"runId"`
	Status  Status    `json:"status"`
	Total   int       `json:"total"`
	Passed  int       `json:"passed"`
	Failed  int       `json:"failed"`
	Flaky   int       `json:"flaky"`
	Skipped int       `json:"skipped"`
}

type logFrame struct {
	ID   string    `json:"id"`
	Line string    `json:"line"`
	At   time.Time `json:"at"`
}

type doneFrame struct {
	RunID      uuid.UUID `json:"runId"`
	Status     Status    `json:"status"`
	DurationMs int64     `json:"durationMs"`
	Error      string    `json:"error,omitempty"`

	// LogStored tells a client whether fetching the archived log is worth trying.
	LogStored bool `json:"logStored"`
}

// writeFrame renders one SSE frame.
//
// The id field is what a browser sends back as Last-Event-ID on reconnect, which is
// why run_log_lines uses a bigserial: the ordering and the resume cursor are the
// same number.
func writeFrame(w http.ResponseWriter, flusher http.Flusher, id, name string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("runs: encode stream payload: %w", err)
	}

	// A write failure means the client went away mid-frame. That ends the stream and
	// is not a server error, so it is reported as a clean finish rather than logged
	// as a failure on every closed tab.
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return nil //nolint:nilerr // a disconnected client is a normal end of stream
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body); err != nil {
		return nil //nolint:nilerr // a disconnected client is a normal end of stream
	}
	flusher.Flush()
	return nil
}
