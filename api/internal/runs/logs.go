package runs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Live log storage and fan-out (BE-4.11).
//
// Lines are batched rather than written one row at a time. A suite printing a
// thousand lines a second would otherwise be a thousand inserts and a thousand
// NOTIFYs a second, which is a load pattern the database did not need to carry: the
// batch is a CopyFrom, and one notify per batch wakes every viewer.
//
// Nothing here can slow a run down. The batch is flushed by a timer and by size,
// the buffer is capped, and a full buffer drops lines with a marker rather than
// blocking the container's output stream. The complete log is uploaded to object
// storage when the run ends, so a dropped line is a gap in the live view and not a
// gap in the record.

// LogNotifier wakes SSE subscribers. Satisfied by the jobs hub, which already owns
// the Postgres LISTEN/NOTIFY fan-out (BE-0.24), so this adds no second mechanism.
type LogNotifier interface {
	Publish(ctx context.Context, jobID uuid.UUID)
}

const (
	// logBatchSize is how many lines accumulate before a flush.
	logBatchSize = 200

	// logFlushInterval bounds how stale the live view can be. A quarter second
	// reads as live to a person and batches heavily under load.
	logFlushInterval = 250 * time.Millisecond

	// logBufferLimit is the backpressure point. Past it, lines are dropped and the
	// gap is stated, because holding a run's stdout hostage to a database write is
	// how a slow disk becomes a slow test suite.
	logBufferLimit = 5000

	// logPageSize is how many lines one SSE drain reads at a time.
	logPageSize = 500
)

// LogWriter buffers a run's output and flushes it in batches.
//
// One per run, created by the execute stage and closed when the run ends.
type LogWriter struct {
	db       *store.DB
	notifier LogNotifier
	runID    uuid.UUID
	jobID    *uuid.UUID

	mu      sync.Mutex
	pending []string
	dropped int
	closed  bool

	flushed chan struct{}
	done    chan struct{}
}

// NewLogWriter starts the batcher. The caller must Close it.
func NewLogWriter(db *store.DB, notifier LogNotifier, runID uuid.UUID, jobID *uuid.UUID) *LogWriter {
	writer := &LogWriter{
		db:       db,
		notifier: notifier,
		runID:    runID,
		jobID:    jobID,
		flushed:  make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	go writer.loop()
	return writer
}

// PublishRunLog queues one line. It never blocks and never fails: this is called
// from the goroutine reading the container's output.
func (w *LogWriter) PublishRunLog(_ context.Context, _ uuid.UUID, line string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return
	}
	if len(w.pending) >= logBufferLimit {
		w.dropped++
		return
	}

	w.pending = append(w.pending, line)
	if len(w.pending) >= logBatchSize {
		// Non-blocking nudge: if a flush is already pending, this line rides along
		// with it.
		select {
		case w.flushed <- struct{}{}:
		default:
		}
	}
}

// loop flushes on a timer and on demand until Close.
func (w *LogWriter) loop() {
	defer close(w.done)

	ticker := time.NewTicker(logFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.flush()
		case _, open := <-w.flushed:
			if !open {
				w.flush()
				return
			}
			w.flush()
		}
	}
}

// Close flushes what is left and stops the batcher.
func (w *LogWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()

	close(w.flushed)
	<-w.done
}

// flush writes one batch. Failures are logged rather than returned: losing a live
// line is not a reason to fail a run whose results are what matter.
func (w *LogWriter) flush() {
	w.mu.Lock()
	if len(w.pending) == 0 && w.dropped == 0 {
		w.mu.Unlock()
		return
	}
	batch := w.pending
	dropped := w.dropped
	w.pending = nil
	w.dropped = 0
	w.mu.Unlock()

	if dropped > 0 {
		// The gap is stated in the stream itself. A viewer seeing "3 lines dropped"
		// knows what happened; a viewer seeing nothing assumes the run is silent.
		batch = append(batch,
			fmt.Sprintf("… %d line(s) dropped: the live view could not keep up. "+
				"The full log is stored with the run.", dropped))
	}

	rows := make([]dbgen.AppendRunLogLinesParams, 0, len(batch))
	for _, line := range batch {
		rows = append(rows, dbgen.AppendRunLogLinesParams{RunID: w.runID, Line: line})
	}

	// Deliberately not the run's context: the flush must complete even when the run
	// has just been cancelled, because a cancelled run's last lines are the ones that
	// explain why. The batcher outlives the run by exactly one flush.
	//
	//nolint:contextcheck // detached on purpose, see above
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := w.db.Queries().AppendRunLogLines(ctx, rows); err != nil {
		slog.WarnContext(ctx, "append run log lines", "run_id", w.runID, "error", err)
		return
	}

	if w.notifier != nil && w.jobID != nil {
		w.notifier.Publish(ctx, *w.jobID)
	}
}

// LogLine is one stored line.
type LogLine struct {
	ID   int64
	Line string
	At   time.Time
}

// LogLinesSince reads a run's lines after an id, which is what the SSE stream sends
// on connect and on resume.
func (s *Service) LogLinesSince(
	ctx context.Context,
	runID uuid.UUID,
	after int64,
	limit int,
) ([]LogLine, error) {
	if limit <= 0 || limit > logPageSize {
		limit = logPageSize
	}

	rows, err := s.db.Queries().ListRunLogLinesSince(ctx, dbgen.ListRunLogLinesSinceParams{
		RunID: runID,
		ID:    after,
		Limit: int32(limit), //nolint:gosec // Clamped above.
	})
	if err != nil {
		return nil, apierr.Internal(fmt.Errorf("read the run log: %w", err))
	}

	lines := make([]LogLine, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, LogLine{ID: row.ID, Line: row.Line, At: row.At})
	}
	return lines, nil
}

// PruneLogLines drops the live tail once the full log is stored.
//
// Called only when the upload succeeded. If it failed, these rows are the only copy
// of the output and deleting them would throw away the evidence of whatever went
// wrong.
func (s *Service) PruneLogLines(ctx context.Context, runID uuid.UUID) error {
	if err := s.db.Queries().DeleteRunLogLines(ctx, runID); err != nil {
		return apierr.Internal(fmt.Errorf("prune the run log tail: %w", err))
	}
	return nil
}
