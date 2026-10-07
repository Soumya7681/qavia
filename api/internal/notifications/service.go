package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/capability"
	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/platform/paging"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// Settings is the slice of the settings service this package needs, declared by
// the consumer (backend-standards.md 3).
type Settings interface {
	StringList(ctx context.Context, key string, target settings.Target) ([]string, error)
}

// Directory supplies recipients this package cannot look up itself, because the
// users table belongs to another package.
type Directory interface {
	// Admins returns every enabled admin. Degradation alerts go to all of them:
	// an integration failing is nobody's personal notification.
	Admins(ctx context.Context) ([]notifier.Recipient, error)
}

// Service owns the notification centre and the fan-out across channels.
type Service struct {
	db        *store.DB
	notifiers *notifier.Registry
	settings  Settings
	directory Directory
}

func NewService(
	db *store.DB,
	notifiers *notifier.Registry,
	settingsService Settings,
	directory Directory,
) *Service {
	return &Service{db: db, notifiers: notifiers, settings: settingsService, directory: directory}
}

// Notify delivers one message to one recipient across every channel they have
// enabled.
//
// Three rules make this safe to call from a job handler:
//
//   - The in-app channel is always delivered, whatever the preferences say. It is
//     the record of what happened, and a user who has switched off email should
//     still find the result in the platform.
//   - An unconfigured channel is skipped, never an error. A fresh install has no
//     SMTP and no Slack, and that is not a failure (requirements.md 5.4).
//   - An external channel that errors degrades to the built-in and raises an admin
//     alert rather than failing the caller's job (FR-10.6).
func (s *Service) Notify(ctx context.Context, to notifier.Recipient, msg notifier.Message) error {
	if err := notifier.Validate(msg); err != nil {
		return err
	}

	builtin, found := s.notifiers.Builtin()
	if !found {
		return fmt.Errorf("notifications: no notifier is registered")
	}
	if err := builtin.Send(ctx, to, msg); err != nil {
		// The in-app write is the one delivery that must not be silently lost: it
		// is the durable record the notification centre reads.
		return fmt.Errorf("notifications: deliver in-app: %w", err)
	}

	for _, channel := range s.extraChannels(ctx, to) {
		for _, impl := range s.notifiers.Active(ctx) {
			if impl.Channel() != channel || impl.ID() == builtin.ID() {
				continue
			}
			if err := impl.Send(ctx, to, msg); err != nil {
				// The message is already in the notification centre, so the work is
				// not lost. What matters is that an admin learns the channel is
				// broken instead of users quietly stopping receiving email.
				slog.WarnContext(ctx, "notification channel failed",
					"channel", string(channel), "implementation", impl.ID(), "error", err)

				s.Degraded(ctx, capability.Degradation{
					Kind:       s.notifiers.Kind(),
					FailedID:   impl.ID(),
					FallbackID: builtin.ID(),
					Operation:  "send",
					Err:        err,
				})
			}
		}
	}
	return nil
}

// NotifyAll delivers the same message to several recipients.
//
// A failure for one recipient does not stop the others: a job that finished has
// finished, and half the team hearing about it beats nobody hearing about it.
func (s *Service) NotifyAll(ctx context.Context, recipients []notifier.Recipient, msg notifier.Message) error {
	var failed int
	for _, recipient := range recipients {
		if err := s.Notify(ctx, recipient, msg); err != nil {
			failed++
			slog.ErrorContext(ctx, "notify recipient",
				"user_id", recipient.UserID, "kind", string(msg.Kind), "error", err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("notifications: %d of %d recipients could not be notified",
			failed, len(recipients))
	}
	return nil
}

// Degraded implements capability.Reporter: an external adapter failed and the
// built-in took over, so every admin is told (FR-10.6).
//
// It never returns an error. It is called from the failure path of something else
// that is already going wrong, and turning "we could not tell you about the
// problem" into a second error helps nobody; the log line carries it instead.
func (s *Service) Degraded(ctx context.Context, event capability.Degradation) {
	if s.directory == nil {
		return
	}

	admins, err := s.directory.Admins(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "load admins for degradation alert", "error", err)
		return
	}

	message := notifier.Message{
		Kind:  notifier.KindIntegrationFailed,
		Title: fmt.Sprintf("%s integration %q failed", event.Kind, event.FailedID),
		Body: fmt.Sprintf(
			"%s fell back to the built-in %q for %s. The work completed. Check the integration's settings.",
			event.FailedID, event.FallbackID, event.Operation),
		Link: "/settings/integrations",
	}

	builtin, found := s.notifiers.Builtin()
	if !found {
		return
	}
	for _, admin := range admins {
		// Deliberately the built-in only. The channel that just failed is not the
		// one to announce its own failure on.
		if err := builtin.Send(ctx, admin, message); err != nil {
			slog.ErrorContext(ctx, "raise degradation alert",
				"user_id", admin.UserID, "error", err)
		}
	}
}

// extraChannels resolves which optional channels a recipient wants.
//
// A failure to read the preference is not a reason to drop the notification: the
// in-app copy is already written, so this degrades to "no extra channels" and logs.
func (s *Service) extraChannels(ctx context.Context, to notifier.Recipient) []notifier.Channel {
	if s.settings == nil {
		return nil
	}

	userID := to.UserID
	values, err := s.settings.StringList(ctx, "preferences.notification_channels",
		settings.Target{UserID: &userID})
	if err != nil {
		slog.WarnContext(ctx, "read notification channel preference",
			"user_id", to.UserID, "error", err)
		return nil
	}

	channels := make([]notifier.Channel, 0, len(values))
	for _, value := range values {
		channel := notifier.Channel(value)
		if channel == notifier.ChannelInApp {
			// Always delivered, so listing it here would send it twice.
			continue
		}
		if !slices.Contains(channels, channel) {
			channels = append(channels, channel)
		}
	}
	return channels
}

// ChannelStatus is one channel and whether it can currently deliver.
type ChannelStatus struct {
	Channel   notifier.Channel
	ID        string
	Available bool
}

// Channels reports what is registered and what is configured.
//
// An unconfigured channel is reported as unavailable rather than omitted, so the
// UI can show "Not configured" with a link to the settings that would fix it,
// which is the difference between a missing feature and an optional one.
func (s *Service) Channels(ctx context.Context) []ChannelStatus {
	implementations := s.notifiers.All()

	out := make([]ChannelStatus, 0, len(implementations))
	for _, impl := range implementations {
		out = append(out, ChannelStatus{
			Channel:   impl.Channel(),
			ID:        impl.ID(),
			Available: impl.Available(ctx),
		})
	}
	return out
}

// ------------------------------------------------------------ notification centre

// List returns a page of a user's notifications, newest first.
func (s *Service) List(ctx context.Context, userID uuid.UUID, limit int, cursor string, unreadOnly bool) (Page, error) {
	limit = paging.ClampLimit(limit)

	params := dbgen.ListNotificationsParams{
		UserID:     userID,
		PageSize:   int32(limit + 1),
		UnreadOnly: &unreadOnly,
	}
	if cursor != "" {
		at, err := paging.DecodeTime(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &at
	}

	rows, err := s.db.Queries().ListNotifications(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list notifications: %w", err)
	}

	unread, err := s.UnreadCount(ctx, userID)
	if err != nil {
		return Page{}, err
	}

	// One row beyond the page tells us whether another page exists without a second
	// count query that could disagree with it.
	page := Page{Items: make([]Notification, 0, limit), Unread: unread}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = paging.EncodeTime(rows[i-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toNotification(row))
	}
	return page, nil
}

// UnreadCount backs the badge.
func (s *Service) UnreadCount(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountUnreadNotifications(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}

// MarkRead marks one notification read.
//
// The user ID is part of the update rather than checked first, so one user cannot
// mark another's notification read even if they guess the ID. Zero rows updated
// means it was already read or is not theirs, and both report success: re-reading
// is not an error, and distinguishing the two would say whether somebody else's
// notification exists.
func (s *Service) MarkRead(ctx context.Context, userID, notificationID uuid.UUID) error {
	if _, err := s.db.Queries().MarkNotificationRead(ctx, dbgen.MarkNotificationReadParams{
		ID: notificationID, UserID: userID,
	}); err != nil {
		return fmt.Errorf("mark notification read: %w", err)
	}
	return nil
}

// MarkAllRead clears the badge and returns how many were marked.
func (s *Service) MarkAllRead(ctx context.Context, userID uuid.UUID) (int64, error) {
	updated, err := s.db.Queries().MarkAllNotificationsRead(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", err)
	}
	return updated, nil
}
