package defecttracker

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
)

// The DefectTracker capability (F-11.1, BE-5.5.5).
//
// The built-in tracker registers first and is what every other implementation
// degrades to. That ordering is the whole design: a Jira adapter that stops working
// must not take bug tracking with it, so the internal defect is always the record of
// truth and an external ticket is a mirror of it (requirements.md 5.4).
//
// Which means the interface is deliberately narrow. It covers what a mirror needs —
// create, update status, comment, and say where it landed — and nothing that would
// make the internal tracker depend on a feature only one vendor has.

// Severity and Status are the platform's vocabulary, not any vendor's. An adapter
// maps them onto whatever its tracker calls the same things, and a tracker with no
// equivalent maps to the closest and says so in its own field.
type (
	Severity string
	Status   string
)

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

const (
	StatusOpen         Status = "open"
	StatusAcknowledged Status = "acknowledged"
	StatusInProgress   Status = "in_progress"
	StatusFixed        Status = "fixed"
	StatusWontFix      Status = "wont_fix"
	StatusDuplicate    Status = "duplicate"
)

// Ticket is a defect as a tracker needs to see it.
//
// Links are references, never content: an adapter that wanted to upload a 40 MB
// video to a ticket would be making a decision about a client's data that this
// platform does not get to make on their behalf.
type Ticket struct {
	// ID is the internal defect. An external ticket always carries it, so a ticket
	// found in Jira can be traced back here.
	ID uuid.UUID

	ProjectID uuid.UUID

	Title       string
	Description string

	Severity Severity
	Status   Status

	// Links are absolute URLs into this platform: the failure, the analysis, the run.
	// Built by the caller from APP_URL, because a stored absolute URL goes stale the
	// first time a deployment changes hostname.
	Links map[string]string
}

// Reference is where a ticket ended up in an external tracker.
//
// Stored on the defect's external_ref column. Empty for the built-in tracker, which
// is its own destination.
type Reference struct {
	// Provider is the adapter's ID, so a defect mirrored twice can say which entry
	// belongs to which system.
	Provider string

	// Key is what a person would quote: "QA-1184".
	Key string

	// URL is where a person would click.
	URL string
}

// Tracker files and updates defects.
type Tracker interface {
	ID() string
	Available(ctx context.Context) bool

	// Create files a ticket and returns where it landed.
	Create(ctx context.Context, ticket Ticket) (Reference, error)

	// UpdateStatus moves an existing ticket. It takes the reference rather than the
	// ticket, because the external system's key is the only thing that identifies it
	// there.
	UpdateStatus(ctx context.Context, ref Reference, status Status) error

	// Comment appends to a ticket's thread, so a discussion started in one system is
	// visible in the other.
	Comment(ctx context.Context, ref Reference, body string) error
}

// Registry holds every tracker. The built-in one registers first and is therefore
// the fallback for every external adapter.
type Registry = capability.Registry[Tracker]

// NewRegistry builds the tracker registry.
func NewRegistry() *Registry { return capability.NewRegistry[Tracker]("defecttracker") }

// Validate rejects a ticket no tracker could sensibly file.
//
// Called once, before fan-out, so an invalid ticket fails the same way everywhere
// rather than being filed internally and rejected externally.
func Validate(ticket Ticket) error {
	switch {
	case ticket.Title == "":
		return fmt.Errorf("defecttracker: a ticket needs a title")
	case ticket.ProjectID == uuid.Nil:
		return fmt.Errorf("defecttracker: a ticket needs the project it belongs to")
	case !ticket.Severity.Valid():
		return fmt.Errorf("defecttracker: %q is not a severity this platform uses", ticket.Severity)
	}
	return nil
}

func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	default:
		return false
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusAcknowledged, StatusInProgress,
		StatusFixed, StatusWontFix, StatusDuplicate:
		return true
	default:
		return false
	}
}
