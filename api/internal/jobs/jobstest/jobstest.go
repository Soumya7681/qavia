// Package jobstest proves a job handler is safe to run twice.
//
// Asynq delivers at least once. A worker killed after the handler finished but
// before the task was acknowledged runs the same job again, so every handler has to
// leave the world in the same state whether it ran once or twice (NFR-4,
// backend-standards.md 8). That property is cheap to assert and expensive to
// discover in production, which is why it has a harness rather than a convention.
package jobstest

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/hyscaler/qavia/api/internal/jobs"
)

// Context is a JobContext that records what a handler reported.
type Context struct {
	ID uuid.UUID

	mu       sync.Mutex
	progress []int
	events   []string
}

var _ jobs.JobContext = (*Context)(nil)

// NewContext returns a recording JobContext for one job.
func NewContext(id uuid.UUID) *Context { return &Context{ID: id} }

func (c *Context) JobID() uuid.UUID { return c.ID }

func (c *Context) Progress(percent int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.progress = append(c.progress, percent)
}

func (c *Context) Event(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, fmt.Sprintf(format, args...))
}

// Events returns the lines the handler narrated, in order.
func (c *Context) Events() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.events...)
}

// Progress values reported, in order.
func (c *Context) ProgressValues() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.progress...)
}

// RunTwice runs handler on payload twice, the way a redelivered task would, and
// asserts both runs succeed and leave the same state behind.
//
// state reads whatever the handler is responsible for (rows, stored objects) into a
// comparable value. It is called after each run, and the two snapshots must be
// equal: a second run that inserts a duplicate row, sends a second notification, or
// fails because the first already did the work is the bug this catches.
//
// The payload goes through the same JSON round trip the worker applies to a stored
// payload, and the idempotency key must survive it unchanged, because the key is
// computed at enqueue and recomputed nowhere else.
//
// It returns the state after the second run, for the caller's own assertions.
func RunTwice[T any](t *testing.T, handler jobs.TypeHandler[T], payload T, state func() any) any {
	t.Helper()

	key := handler.IdempotencyKey(payload)
	require.NotEmpty(t, key, "%s: an empty idempotency key makes every enqueue a new job", handler.Type())

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	var stored T
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.Equal(t, key, handler.IdempotencyKey(stored),
		"%s: the idempotency key changed across the payload's JSON round trip", handler.Type())

	ctx := t.Context()
	id := uuid.New()

	require.NoError(t, handler.Handle(ctx, stored, NewContext(id)), "%s: first run", handler.Type())
	first := state()

	require.NoError(t, handler.Handle(ctx, stored, NewContext(id)),
		"%s: second run of the same job must succeed, not fail because the first did the work", handler.Type())
	second := state()

	require.Equal(t, first, second, "%s: running the job twice left a different state than running it once",
		handler.Type())
	return second
}
