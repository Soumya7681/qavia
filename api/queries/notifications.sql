-- In-app notifications. The built-in Notifier, always available, zero
-- configuration.

-- name: CreateNotification :one
INSERT INTO notifications (user_id, kind, title, body, link)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, kind, title, body, link, read_at, created_at;

-- name: ListNotifications :many
SELECT id, user_id, kind, title, body, link, read_at, created_at
FROM notifications
WHERE user_id = $1
  AND (NOT sqlc.narg('unread_only')::boolean OR read_at IS NULL)
  AND (sqlc.narg('cursor')::timestamptz IS NULL OR created_at < sqlc.narg('cursor')::timestamptz)
ORDER BY created_at DESC
LIMIT sqlc.arg('page_size');

-- name: CountUnreadNotifications :one
SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL;

-- name: MarkNotificationRead :execrows
UPDATE notifications
SET read_at = now()
WHERE id = $1 AND user_id = $2 AND read_at IS NULL;

-- name: MarkAllNotificationsRead :execrows
UPDATE notifications
SET read_at = now()
WHERE user_id = $1 AND read_at IS NULL;

-- name: DeleteNotificationsOlderThan :execrows
DELETE FROM notifications WHERE created_at < $1;
