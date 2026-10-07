package runs

import (
	"bytes"
	"context"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/jobs"
)

// Live log fan-out (BE-4.11).
//
// The driver writes container output to an io.Writer as it arrives. This is that
// writer: it splits the stream into lines and hands each one to the SSE hub, so a
// viewer sees output while the suite runs rather than all of it at the end.
//
// Two rules make it safe to attach to a run:
//
//   - **Nothing here blocks the run.** Publishing is a non-blocking hand-off; a
//     viewer that cannot keep up loses lines, and the run does not slow down. The
//     full log is in object storage when the run ends, so nothing is actually lost.
//   - **The buffer is bounded.** A suite printing a megabyte on one line must not
//     turn into a megabyte held per writer, so an over-long line is flushed as it
//     is rather than accumulated.

// maxLineBytes caps one buffered line.
const maxLineBytes = 8 << 10

// publishWriter turns a byte stream into published lines.
type publishWriter struct {
	ctx       context.Context
	runID     uuid.UUID
	publisher Publisher

	// events is the job's own log, which is what a client watching the chain sees.
	// Only occasional lines go here: the run's full output would flood it.
	events jobs.JobContext

	mu      sync.Mutex
	pending bytes.Buffer
	lines   int
}

func (w *publishWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending.Write(p)

	for {
		line, found := w.take()
		if !found {
			break
		}
		w.emit(line)
	}
	return len(p), nil
}

// take pulls one line out of the buffer, or forces one out when it has grown past
// the cap.
func (w *publishWriter) take() (string, bool) {
	if index := bytes.IndexByte(w.pending.Bytes(), '\n'); index >= 0 {
		line := make([]byte, index+1)
		if _, err := w.pending.Read(line); err != nil {
			return "", false
		}
		return strings.TrimRight(string(line), "\r\n"), true
	}

	if w.pending.Len() >= maxLineBytes {
		line := w.pending.String()
		w.pending.Reset()
		return line, true
	}
	return "", false
}

func (w *publishWriter) emit(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}

	w.lines++
	if w.publisher != nil {
		w.publisher.PublishRunLog(w.ctx, w.runID, line)
	}

	// The job's own event log gets a sample rather than the stream. It is stored per
	// line in Postgres, and a 400-test suite's output is not something to write
	// there row by row.
	if w.events != nil && w.lines%50 == 0 {
		w.events.Event("… %d lines of runner output", w.lines)
	}
}

// Flush publishes whatever is left, which is the last line when a run's output did
// not end with a newline.
func (w *publishWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.pending.Len() == 0 {
		return
	}
	line := w.pending.String()
	w.pending.Reset()
	w.emit(line)
}
