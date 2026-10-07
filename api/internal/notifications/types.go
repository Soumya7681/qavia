package notifications

import (
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Notification is one in-app notification as the rest of the platform sees it.
type Notification struct {
	ID     uuid.UUID
	UserID uuid.UUID
	Kind   notifier.Kind

	Title string
	Body  string

	// Link is a path into the web app, so a deployment that changes hostname does
	// not leave stored rows pointing at the old one (FR-8.5).
	Link string

	ReadAt    *time.Time
	CreatedAt time.Time
}

// Read reports whether the notification has been seen.
func (n Notification) Read() bool { return n.ReadAt != nil }

// Page is one page of notifications plus the cursor for the next.
type Page struct {
	Items      []Notification
	Unread     int64
	NextCursor string
}

// toNotification maps the row. No sqlc row type crosses this package's boundary
// (backend-standards.md 4).
func toNotification(row dbgen.Notification) Notification {
	return Notification{
		ID:        row.ID,
		UserID:    row.UserID,
		Kind:      notifier.Kind(row.Kind),
		Title:     row.Title,
		Body:      row.Body,
		Link:      row.Link,
		ReadAt:    row.ReadAt,
		CreatedAt: row.CreatedAt,
	}
}

// ToAPI maps a notification to its response shape.
func ToAPI(n Notification) api.Notification {
	out := api.Notification{
		Id:        n.ID,
		Kind:      api.NotificationKind(n.Kind),
		Title:     n.Title,
		Body:      n.Body,
		Link:      n.Link,
		Read:      n.Read(),
		CreatedAt: n.CreatedAt,
	}
	if n.ReadAt != nil {
		out.ReadAt.Set(*n.ReadAt)
	}
	return out
}
