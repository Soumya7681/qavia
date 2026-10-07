package notifier

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
)

// Channel is where a message is delivered. The in-app channel is built in and
// always present; everything else is an optional integration that may be absent
// on a working install (requirements.md 5.4).
type Channel string

const (
	ChannelInApp Channel = "in_app"
	ChannelEmail Channel = "email"
	ChannelSlack Channel = "slack"
)

// Kind is what a notification is about. The values match the check constraint on
// the notifications table, so an unknown kind fails at the boundary rather than
// at the insert.
type Kind string

const (
	KindJobCompleted      Kind = "job_completed"
	KindJobFailed         Kind = "job_failed"
	KindJobNeedsInput     Kind = "job_needs_input"
	KindRunCompleted      Kind = "run_completed"
	KindDriftDetected     Kind = "drift_detected"
	KindIntegrationFailed Kind = "integration_failed"
	KindAdminAlert        Kind = "admin_alert"
)

// Valid reports whether the kind is one the database will accept.
func (k Kind) Valid() bool {
	switch k {
	case KindJobCompleted, KindJobFailed, KindJobNeedsInput, KindRunCompleted,
		KindDriftDetected, KindIntegrationFailed, KindAdminAlert:
		return true
	default:
		return false
	}
}

// Message is one notification, independent of channel.
//
// Link is a path rather than an absolute URL. Each channel builds the absolute
// form from APP_URL where it needs one, so a deployment that changes hostname does
// not leave stale links in stored rows (FR-8.5).
type Message struct {
	Kind  Kind
	Title string
	Body  string
	Link  string
}

// Recipient is who a message is for.
//
// It carries the fields every channel needs and nothing else. A platform-level
// capability must not depend on the users package, so the caller maps a user into
// this rather than passing a domain type down.
type Recipient struct {
	UserID uuid.UUID
	Email  string
	Name   string
}

// Notifier delivers a message on one channel.
type Notifier interface {
	ID() string
	Available(ctx context.Context) bool

	// Channel is what a user's preferences switch on. Two adapters may share a
	// channel: SMTP and a transactional email provider are both email.
	Channel() Channel

	Send(ctx context.Context, to Recipient, msg Message) error
}

// Registry holds every notifier. The in-app one registers first and is therefore
// the fallback for every external channel.
type Registry = capability.Registry[Notifier]

// NewRegistry builds the notifier registry.
func NewRegistry() *Registry { return capability.NewRegistry[Notifier]("notifier") }

// Validate rejects a message that no channel could sensibly deliver.
//
// Called once, before fan-out, so an invalid message fails the same way on every
// channel rather than partly delivering.
func Validate(msg Message) error {
	switch {
	case !msg.Kind.Valid():
		return fmt.Errorf("notifier: unknown notification kind %q", msg.Kind)
	case msg.Title == "":
		return fmt.Errorf("notifier: notification of kind %q has no title", msg.Kind)
	}
	return nil
}
