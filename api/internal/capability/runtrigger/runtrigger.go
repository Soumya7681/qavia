package runtrigger

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
)

// Kind is how a run was started. It is recorded on the job, so "who started this"
// is answerable months later without guessing from timestamps.
type Kind string

const (
	KindManual   Kind = "manual"
	KindSchedule Kind = "schedule"
	KindWebhook  Kind = "webhook"
)

// Request is a run somebody or something asked for.
type Request struct {
	ProjectID  uuid.UUID
	Chain      string
	ArtifactID *uuid.UUID

	// Reference is free text from the trigger, such as a branch name or a
	// schedule's name, recorded on the job.
	Reference string

	Source Kind

	// ActorID is the user the work is attributed to. Nil for a schedule, which
	// nobody is waiting on.
	ActorID *uuid.UUID

	// RunID is set for the execute chain, whose run row is written before the chain
	// is submitted: the target check and the concurrency cap happen on the request
	// that asked for the run, not inside a worker (BE-4.9).
	RunID *uuid.UUID
}

// Submitter is the slice of the jobs service a trigger needs, declared by the
// consumer (backend-standards.md 3).
type Submitter interface {
	SubmitTriggered(ctx context.Context, req Request) (uuid.UUID, error)
}

// Trigger is one way a run can start.
//
// All three built-ins satisfy it: the manual API call, the internal scheduler, and
// the generic inbound webhook. None of them needs an external platform configured,
// which is the point: a CI system can start work on a zero-integration install
// (F-2.8, F-2.9).
type Trigger interface {
	ID() string
	Available(ctx context.Context) bool

	Kind() Kind
}

// Registry holds every trigger.
type Registry = capability.Registry[Trigger]

// NewRegistry builds the run trigger registry.
func NewRegistry() *Registry { return capability.NewRegistry[Trigger]("runtrigger") }

// ManualID is the built-in manual trigger's ID.
const ManualID = "manual"

// Manual is the default: a person pressed submit.
//
// It holds no state, because the API endpoint is the mechanism. It is registered
// so that "how can a run start here" is answerable from the registry rather than
// from reading the router.
type Manual struct{}

func NewManual() *Manual { return &Manual{} }

func (m *Manual) ID() string { return ManualID }

func (m *Manual) Kind() Kind { return KindManual }

// Available is unconditionally true. A manual run needs nothing configured.
func (m *Manual) Available(_ context.Context) bool { return true }
