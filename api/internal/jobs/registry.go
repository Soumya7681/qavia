package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
)

// registered is one job type with its generic type parameter erased.
//
// The registry has to hold handlers of different payload types in one map, so the
// decode and the call are captured as closures when the handler is registered.
// The type safety is not lost, it is moved: it is enforced at the Register call,
// which is the only place both types are visible.
type registered struct {
	typeName string
	queue    Queue

	idempotencyKey func(json.RawMessage) (string, error)
	handle         func(ctx context.Context, payload json.RawMessage, jc JobContext) error
}

// Registry maps a job type name to its handler.
//
// The Go registry is the authority on what a valid job type is, which is why the
// type column is text with no check constraint: types grow with every phase, and
// a migration per job type would be noise.
type Registry struct {
	byType map[string]registered
}

func NewRegistry() *Registry { return &Registry{byType: make(map[string]registered)} }

// Register adds a handler.
//
// It panics on a duplicate. Registration happens in main.go before the worker
// starts, so a duplicate is a wiring bug, and a failed boot beats a process where
// which handler runs depends on map ordering.
func Register[T any](r *Registry, handler TypeHandler[T], queue Queue) {
	typeName := handler.Type()
	if typeName == "" {
		panic("jobs: handler has an empty type name")
	}
	if _, duplicate := r.byType[typeName]; duplicate {
		panic(fmt.Sprintf("jobs: handler %q registered twice", typeName))
	}
	if queue == "" {
		queue = QueueDefault
	}

	r.byType[typeName] = registered{
		typeName: typeName,
		queue:    queue,
		idempotencyKey: func(raw json.RawMessage) (string, error) {
			payload, err := decodePayload[T](raw)
			if err != nil {
				return "", err
			}
			return handler.IdempotencyKey(payload), nil
		},
		handle: func(ctx context.Context, raw json.RawMessage, jc JobContext) error {
			payload, err := decodePayload[T](raw)
			if err != nil {
				return err
			}
			return handler.Handle(ctx, payload, jc)
		},
	}
}

// RegisterEnqueueOnly declares a type this process may push but cannot run.
//
// It exists because execution is the one stage that needs a container runtime
// (BE-4.2), and the API process deliberately has none: it checks the target, writes
// the run row, and pushes the task for a worker that does. Without this the enqueue
// would be refused as an unknown type, and with a fake handler registered instead a
// misconfigured worker could silently pick the task up and do nothing.
//
// A process that registers a type this way and then dequeues it fails loudly, which
// is the intended outcome: it means the deployment gave execution to a process that
// cannot execute.
func RegisterEnqueueOnly(r *Registry, typeName string, queue Queue, keyFor func(StagePayload) string) {
	if typeName == "" {
		panic("jobs: enqueue-only registration has an empty type name")
	}
	if _, duplicate := r.byType[typeName]; duplicate {
		panic(fmt.Sprintf("jobs: handler %q registered twice", typeName))
	}
	if queue == "" {
		queue = QueueDefault
	}

	r.byType[typeName] = registered{
		typeName: typeName,
		queue:    queue,
		idempotencyKey: func(raw json.RawMessage) (string, error) {
			payload, err := decodePayload[StagePayload](raw)
			if err != nil {
				return "", err
			}
			return keyFor(payload), nil
		},
		handle: func(_ context.Context, _ json.RawMessage, _ JobContext) error {
			return fmt.Errorf(
				"jobs: %q was dequeued by a process that only enqueues it; "+
					"this deployment routed execution to a process with no container runtime", typeName)
		},
	}
}

// RegisterEnqueueOnlyFor is RegisterEnqueueOnly for a job with its own payload type.
//
// The chain payload covers most work; a standalone job such as a report has its own
// shape, and borrowing the chain's would mean carrying six fields that are always
// empty.
func RegisterEnqueueOnlyFor[T any](
	r *Registry,
	typeName string,
	queue Queue,
	keyFor func(T) string,
) {
	if typeName == "" {
		panic("jobs: enqueue-only registration has an empty type name")
	}
	if _, duplicate := r.byType[typeName]; duplicate {
		panic(fmt.Sprintf("jobs: handler %q registered twice", typeName))
	}
	if queue == "" {
		queue = QueueDefault
	}

	r.byType[typeName] = registered{
		typeName: typeName,
		queue:    queue,
		idempotencyKey: func(raw json.RawMessage) (string, error) {
			payload, err := decodePayload[T](raw)
			if err != nil {
				return "", err
			}
			return keyFor(payload), nil
		},
		handle: func(_ context.Context, _ json.RawMessage, _ JobContext) error {
			return fmt.Errorf(
				"jobs: %q was dequeued by a process that only enqueues it; "+
					"this deployment routed the work to a process that cannot do it", typeName)
		},
	}
}

// lookup returns the handler for a type.
func (r *Registry) lookup(typeName string) (registered, bool) {
	handler, found := r.byType[typeName]
	return handler, found
}

// Types returns every registered job type, sorted. Used by the worker to build its
// task multiplexer and by the readiness detail line.
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.byType))
	for typeName := range r.byType {
		types = append(types, typeName)
	}
	sort.Strings(types)
	return types
}

// Knows reports whether a type has a handler, so an enqueue of a type nothing can
// run is refused at submit rather than becoming a job that fails on every worker.
func (r *Registry) Knows(typeName string) bool {
	_, found := r.byType[typeName]
	return found
}

// QueueFor returns the priority band a type runs in.
func (r *Registry) QueueFor(typeName string) Queue {
	if handler, found := r.byType[typeName]; found {
		return handler.queue
	}
	return QueueDefault
}

// Queues returns the weighted queue map the asynq server takes.
//
// Weights, not strict priority: a strict ordering starves the low queue entirely
// while anything at all is queued above it, and retention that never runs is a
// disk that fills up quietly.
func Queues() map[string]int {
	return map[string]int{
		string(QueueCritical): 6,
		string(QueueDefault):  3,
		string(QueueLow):      1,
	}
}

// QueueNames lists the bands, highest first.
func QueueNames() []string {
	return slices.Clone([]string{
		string(QueueCritical), string(QueueDefault), string(QueueLow),
	})
}

func newBytesReader(raw json.RawMessage) *bytes.Reader { return bytes.NewReader(raw) }
