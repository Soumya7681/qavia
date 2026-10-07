package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store"
)

// eventChannel carries a job ID whenever that job's status or log changed.
const eventChannel = "qavia_job_changed"

// Hub fans one database listener out to many watching browsers.
//
// The alternative, a listener per viewer, opens a database connection per open tab
// and exhausts the pool long before it exhausts anybody's patience. One connection
// per process is what makes "two people watching the same run" free
// (BE-0.24).
//
// The notification carries only the job ID. The database is the source of truth
// for what changed, so a subscriber reads the rows it has not seen rather than
// trusting a payload that could arrive out of order.
type Hub struct {
	db *store.DB

	mu     sync.RWMutex
	nextID uint64
	byJob  map[uuid.UUID]map[uint64]chan struct{}
}

func NewHub(db *store.DB) *Hub {
	return &Hub{db: db, byJob: make(map[uuid.UUID]map[uint64]chan struct{})}
}

// Subscribe returns a channel that receives a signal whenever the job changes,
// and a function that removes the subscription.
//
// The channel is buffered by one and sends are non-blocking, so a slow reader
// coalesces bursts into one wake-up instead of blocking the listener that serves
// every other viewer.
//
// Callers must defer the cancel. A leaked subscription holds a channel and a map
// entry for the life of the process, and this is exactly the code where that
// happens (BE-0.24).
func (h *Hub) Subscribe(jobID uuid.UUID) (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	id := h.nextID

	channel := make(chan struct{}, 1)
	if h.byJob[jobID] == nil {
		h.byJob[jobID] = make(map[uint64]chan struct{})
	}
	h.byJob[jobID][id] = channel

	return channel, func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		subscribers, watching := h.byJob[jobID]
		if !watching {
			return
		}
		delete(subscribers, id)
		if len(subscribers) == 0 {
			delete(h.byJob, jobID)
		}
	}
}

// Publish tells every process that a job changed.
//
// pg_notify rather than the NOTIFY statement, because the payload is a parameter
// and therefore never interpolated into SQL. A failure is logged and swallowed:
// the change is already committed, and refusing a job because a live view might
// lag would be the wrong trade.
func (h *Hub) Publish(ctx context.Context, jobID uuid.UUID) {
	if _, err := h.db.Pool().Exec(context.WithoutCancel(ctx),
		"SELECT pg_notify($1, $2)", eventChannel, jobID.String()); err != nil {
		slog.WarnContext(ctx, "publish job change", "job_id", jobID, "error", err)
	}
}

// Listen subscribes to job changes until the context is cancelled.
//
// It holds one dedicated connection and reconnects with backoff. A dropped
// listener would mean every open stream in this process silently stopping updating
// while still looking connected, so the reconnect wakes every subscriber on
// recovery: a viewer catches up from the database rather than staying behind
// forever.
func (h *Hub) Listen(ctx context.Context) error {
	const (
		minBackoff = 500 * time.Millisecond
		maxBackoff = 30 * time.Second
	)

	backoff := minBackoff
	for {
		err := h.listenOnce(ctx)
		if ctx.Err() != nil {
			// Shutting down. Whatever the listener returned is a consequence of
			// that, not a fault worth reporting.
			return nil //nolint:nilerr // the error is the shutdown, not a failure
		}
		if err != nil {
			slog.WarnContext(ctx, "job event listener dropped; reconnecting",
				"error", err, "retry_in", backoff.String())
		}

		h.wakeAll()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (h *Hub) listenOnce(ctx context.Context) error {
	conn, err := h.db.Pool().Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire job event listener connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+eventChannel); err != nil {
		return fmt.Errorf("listen on %s: %w", eventChannel, err)
	}
	slog.InfoContext(ctx, "listening for job changes", "channel", eventChannel)

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wait for job notification: %w", err)
		}

		jobID, parseErr := uuid.Parse(notification.Payload)
		if parseErr != nil {
			slog.WarnContext(ctx, "job notification with an unreadable payload",
				"payload", notification.Payload)
			continue
		}
		h.wake(jobID)
	}
}

func (h *Hub) wake(jobID uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, channel := range h.byJob[jobID] {
		select {
		case channel <- struct{}{}:
		default:
			// A signal is already pending. One wake-up is enough: the subscriber
			// reads everything new when it gets there.
		}
	}
}

// wakeAll nudges every subscriber, used after a reconnect where notifications
// were missed.
func (h *Hub) wakeAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, subscribers := range h.byJob {
		for _, channel := range subscribers {
			select {
			case channel <- struct{}{}:
			default:
			}
		}
	}
}

// Watchers reports how many streams are open, for the readiness detail line and
// for a test that asserts teardown left nothing behind.
func (h *Hub) Watchers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var total int
	for _, subscribers := range h.byJob {
		total += len(subscribers)
	}
	return total
}
