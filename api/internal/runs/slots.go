package runs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyscaler/qavia/api/internal/settings"
)

// Concurrency limiting (BE-4.10).
//
// The queue's own concurrency is not the control here, for two reasons. A worker
// runs every kind of job, and a runner slot is a much scarcer resource than a
// parse: limiting the queue to the number of containers a host can hold would also
// limit ingest and generation to that. And the limit has to be a setting an
// operator changes without a deploy, which queue configuration read at boot is not.
//
// So it is a semaphore, sized from settings, re-read on each acquire. Growing it
// takes effect immediately; shrinking it takes effect as slots are released,
// because killing a container mid-run to satisfy a new limit would throw away work
// for no benefit.

// Semaphore bounds how many runs execute at once.
type Semaphore struct {
	settings Settings

	mu      sync.Mutex
	holders int
	limit   int
	queue   []chan struct{}

	waiting atomic.Int64
}

func NewSemaphore(config Settings) *Semaphore {
	return &Semaphore{settings: config}
}

// Waiting reports how many acquires are queued, for the "waiting for a runner slot"
// message.
func (s *Semaphore) Waiting() int { return int(s.waiting.Load()) }

// Acquire blocks until a slot is free or the context ends.
//
// The returned release must be called exactly once. It is a closure rather than a
// method so a caller cannot release a slot it does not hold.
func (s *Semaphore) Acquire(ctx context.Context) (func(), error) {
	limit, err := s.currentLimit(ctx)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.limit = limit
	if s.holders < s.limit {
		s.holders++
		s.mu.Unlock()
		return s.releaseOnce(), nil
	}

	// No slot: join the queue and wait to be handed one. FIFO, so a run that has
	// been waiting does not lose its place to one that just arrived.
	ticket := make(chan struct{})
	s.queue = append(s.queue, ticket)
	s.mu.Unlock()

	s.waiting.Add(1)
	defer s.waiting.Add(-1)

	select {
	case <-ticket:
		return s.releaseOnce(), nil
	case <-ctx.Done():
		s.abandon(ticket)
		return nil, ctx.Err()
	}
}

// releaseOnce returns a release that is safe to call twice, because a defer plus an
// error path eventually calls it twice somewhere.
func (s *Semaphore) releaseOnce() func() {
	var once sync.Once
	return func() { once.Do(s.release) }
}

func (s *Semaphore) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A shrunk limit is applied here rather than by killing a container: if the
	// holders now exceed the limit, this slot retires instead of being passed on.
	if s.limit > 0 && s.holders > s.limit {
		s.holders--
		return
	}

	// Otherwise hand the slot straight to the next waiter rather than decrementing
	// and letting it race for it.
	if len(s.queue) > 0 {
		ticket := s.queue[0]
		s.queue = s.queue[1:]
		close(ticket)
		return
	}

	s.holders--
	if s.holders < 0 {
		s.holders = 0
	}
}

// abandon removes a ticket a cancelled acquire left in the queue.
func (s *Semaphore) abandon(ticket chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for index, queued := range s.queue {
		if queued == ticket {
			s.queue = append(s.queue[:index], s.queue[index+1:]...)
			return
		}
	}

	// Not in the queue any more, which means a slot was handed over between the
	// context ending and this lock. Release it so it is not lost.
	select {
	case <-ticket:
		s.holders--
		if s.holders < 0 {
			s.holders = 0
		}
	default:
	}
}

// currentLimit reads the setting, with a floor of one: a limit of zero would mean a
// platform that accepts runs and never runs them.
func (s *Semaphore) currentLimit(ctx context.Context) (int, error) {
	limit, err := s.settings.Int(ctx, "runner.max_concurrent_runs", settings.Target{})
	if err != nil {
		return 0, fmt.Errorf("read the concurrency limit: %w", err)
	}
	if limit < 1 {
		limit = 1
	}
	return limit, nil
}

// Held reports how many slots are in use, for the health endpoint.
func (s *Semaphore) Held() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holders
}

// SweepInterval is how often the orphan sweeper runs. Frequent enough that a
// crashed worker's container is gone within minutes, rare enough to be invisible.
const SweepInterval = 2 * time.Minute
