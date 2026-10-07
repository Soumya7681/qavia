package defects

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/defecttracker"
)

// The built-in DefectTracker implementation (BE-5.5.5).
//
// It is a thin adapter over this package's own service, and it exists so the
// capability registry has a member that is always present. Every external tracker
// registers behind it and degrades to it: a Jira adapter that starts failing does
// not take bug tracking down, because the defect was already filed here and the
// external ticket was only ever a mirror (F-11.1).
//
// Its Create is a special case worth naming: an internal defect already exists by
// the time anything asks the capability to file one, so this returns a reference to
// that row rather than creating a second copy of it.

// BuiltIn adapts the internal tracker to the capability interface.
type BuiltIn struct {
	service *Service
}

// NewBuiltIn wires the adapter. Registered first, before any external tracker, so it
// is the fallback rather than a competitor.
func NewBuiltIn(service *Service) *BuiltIn { return &BuiltIn{service: service} }

func (b *BuiltIn) ID() string { return "internal" }

// Available is unconditionally true, and that is the point of this capability having
// a built-in member: the answer to "can this platform track a bug" never depends on
// somebody else's uptime.
func (b *BuiltIn) Available(_ context.Context) bool { return true }

// Create returns a reference to the defect that already exists.
//
// Nothing is inserted. The internal defect is the record; an external adapter's
// Create files a copy elsewhere, and the built-in one has nowhere else to file to.
func (b *BuiltIn) Create(
	_ context.Context,
	ticket defecttracker.Ticket,
) (defecttracker.Reference, error) {
	if err := defecttracker.Validate(ticket); err != nil {
		return defecttracker.Reference{}, err
	}

	return defecttracker.Reference{
		Provider: b.ID(),
		Key:      ticket.ID.String(),
		URL:      fmt.Sprintf("/projects/%s/defects/%s", ticket.ProjectID, ticket.ID),
	}, nil
}

// UpdateStatus moves the internal defect.
func (b *BuiltIn) UpdateStatus(
	ctx context.Context,
	ref defecttracker.Reference,
	status defecttracker.Status,
) error {
	id, err := parseKey(ref.Key)
	if err != nil {
		return err
	}

	mapped := Status(status)
	if !mapped.Valid() {
		return fmt.Errorf("defects: %q is not a status this tracker uses", status)
	}

	if _, err := b.service.Update(ctx, id, UpdateInput{Status: &mapped}); err != nil {
		return err
	}
	return nil
}

// Comment appends to the internal defect's thread, marked as written by the system:
// a comment arriving through the capability came from an integration, not from a
// colleague.
func (b *BuiltIn) Comment(ctx context.Context, ref defecttracker.Reference, body string) error {
	id, err := parseKey(ref.Key)
	if err != nil {
		return err
	}

	if _, err := b.service.Comment(ctx, id, nil, body, true); err != nil {
		return err
	}
	return nil
}

// parseKey reads the internal defect ID back out of a reference.
//
// The built-in tracker's key is the defect's own UUID, so anything else is a
// reference from another provider that reached the wrong adapter.
func parseKey(key string) (uuid.UUID, error) {
	id, err := uuid.Parse(key)
	if err != nil {
		return uuid.Nil, fmt.Errorf("defects: %q is not an internal defect reference", key)
	}
	return id, nil
}
