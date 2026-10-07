package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// invalidationChannel is the Postgres NOTIFY channel carrying setting keys.
const invalidationChannel = "qavia_settings_changed"

// Invalidate drops the local cache entry and tells every other process to do the
// same.
//
// The API and the worker are separate binaries, so a local map is not enough: a
// stale read after a write is a bug, not a tolerance (backend-standards.md 6).
//
// The transport is Postgres LISTEN/NOTIFY rather than Redis. backend-standards.md 6
// says Redis and tech-stack.md 5 says LISTEN/NOTIFY; this follows tech-stack, because
// the settings cache was the only thing that would have required Redis here, and
// keeping it on Postgres leaves the queue choice in work.md 8 item 7 genuinely open.
func (s *Service) Invalidate(ctx context.Context, key string) error {
	s.cache.forget(key)

	// pg_notify is used rather than the NOTIFY statement because the payload is a
	// parameter, so a setting key can never be interpolated into SQL.
	if _, err := s.db.Pool().Exec(ctx,
		"SELECT pg_notify($1, $2)", invalidationChannel, key); err != nil {
		return fmt.Errorf("publish settings invalidation for %q: %w", key, err)
	}
	return nil
}

// Listen subscribes to invalidations until the context is cancelled.
//
// Both processes run this. It holds one dedicated connection, not one per watcher,
// and reconnects with backoff: losing the listener silently would mean every process
// serving stale settings for as long as it stayed up, which is the failure this whole
// mechanism exists to prevent.
func (s *Service) Listen(ctx context.Context) error {
	const (
		minBackoff = 500 * time.Millisecond
		maxBackoff = 30 * time.Second
	)

	backoff := minBackoff
	for {
		err := s.listenOnce(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			slog.WarnContext(ctx, "settings invalidation listener dropped; reconnecting",
				"error", err, "retry_in", backoff.String())
		}

		// Any reconnect means messages were missed, so the whole cache is dropped
		// rather than assuming which keys changed while it was down.
		s.cache.clear()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

func (s *Service) listenOnce(ctx context.Context) error {
	conn, err := s.db.Pool().Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listener connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+invalidationChannel); err != nil {
		return fmt.Errorf("listen on %s: %w", invalidationChannel, err)
	}

	slog.InfoContext(ctx, "listening for settings changes", "channel", invalidationChannel)

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("wait for settings notification: %w", err)
		}
		s.applyNotification(ctx, notification)
	}
}

func (s *Service) applyNotification(ctx context.Context, notification *pgconn.Notification) {
	key := notification.Payload
	if key == "" {
		// An empty payload says something changed but not what.
		s.cache.clear()
		return
	}

	s.cache.forget(key)
	slog.DebugContext(ctx, "settings cache invalidated", "key", key)
}
