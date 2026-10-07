package users

import (
	"encoding/base64"
	"strconv"
	"time"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// Cursor pagination, not offset. Offset breaks under concurrent writes: a row
// inserted between two requests shifts the window and a caller silently skips an
// entry (backend-standards.md 11).
//
// The cursor is opaque and base64 encoded so nobody builds one by hand and then
// depends on its shape. It carries the created_at of the last row on the page.

func encodeCursor(at time.Time) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(at.UnixNano(), 10)))
}

func decodeCursor(cursor string) (time.Time, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, invalidCursor()
	}
	nanos, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return time.Time{}, invalidCursor()
	}
	return time.Unix(0, nanos).UTC(), nil
}

func invalidCursor() error {
	return apierr.Validation(
		"That page cursor is not valid. Start from the first page.",
		map[string]any{"field": "cursor"})
}
