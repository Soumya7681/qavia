// Package paging holds the cursor encoding every list endpoint shares.
//
// Cursor pagination, not offset. Offset breaks under concurrent writes: a row
// inserted between two requests shifts the window and a caller silently skips an
// entry (backend-standards.md 11).
//
// The cursor is opaque and base64 encoded so nobody builds one by hand and then
// depends on its shape. Two forms exist because the tables sort two ways: by
// timestamp for most lists, and by a bigserial id for the append-only logs, where
// the id is both the order and the resume point.
package paging

import (
	"encoding/base64"
	"strconv"
	"time"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
)

// DefaultLimit applies when a caller does not ask for a page size.
const DefaultLimit = 50

// MaxLimit caps every list. No unbounded list, even where "it will only ever be a
// few rows" (backend-standards.md 9).
const MaxLimit = 200

// ClampLimit turns a requested page size into an allowed one.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	default:
		return limit
	}
}

// EncodeTime builds a cursor from the sort timestamp of the last row on a page.
func EncodeTime(at time.Time) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte(strconv.FormatInt(at.UnixNano(), 10)))
}

// DecodeTime reads a timestamp cursor.
func DecodeTime(cursor string) (time.Time, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, Invalid()
	}
	nanos, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return time.Time{}, Invalid()
	}
	return time.Unix(0, nanos).UTC(), nil
}

// EncodeID builds a cursor from a bigserial id.
func EncodeID(id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(id, 10)))
}

// DecodeID reads an id cursor.
func DecodeID(cursor string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, Invalid()
	}
	id, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, Invalid()
	}
	return id, nil
}

// Invalid is the domain error for a cursor that cannot be read.
//
// It says what to do next rather than describing the encoding, because a stale
// bookmark is the usual cause and the user's only useful move is to start again.
func Invalid() error {
	return apierr.Validation(
		"That page cursor is not valid. Start from the first page.",
		map[string]any{"field": "cursor"})
}
