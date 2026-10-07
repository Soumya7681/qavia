package notifications

import (
	"context"
	"fmt"

	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// InAppID is the built-in notifier's ID.
const InAppID = "in-app"

// InApp writes notifications to the notifications table.
//
// It is the built-in: always available, zero configuration, and the fallback for
// every external channel. A platform where SMTP and Slack are both unconfigured
// still notifies people, which is what makes those integrations genuinely optional
// (FR-8.2).
//
// It lives in this package rather than in capability/notifier because it owns the
// table, and a package may only touch its own tables (backend-standards.md 3).
type InApp struct {
	db *store.DB
}

func NewInApp(db *store.DB) *InApp { return &InApp{db: db} }

func (i *InApp) ID() string { return InAppID }

func (i *InApp) Channel() notifier.Channel { return notifier.ChannelInApp }

// Available is unconditionally true. The in-app channel has no configuration to
// be missing: if the database is down nothing else works either.
func (i *InApp) Available(_ context.Context) bool { return true }

// Send stores one notification.
func (i *InApp) Send(ctx context.Context, to notifier.Recipient, msg notifier.Message) error {
	if err := notifier.Validate(msg); err != nil {
		return err
	}

	if _, err := i.db.Queries().CreateNotification(ctx, dbgen.CreateNotificationParams{
		UserID: to.UserID,
		Kind:   string(msg.Kind),
		Title:  msg.Title,
		Body:   msg.Body,
		Link:   msg.Link,
	}); err != nil {
		return fmt.Errorf("create notification for %s: %w", to.UserID, err)
	}
	return nil
}
