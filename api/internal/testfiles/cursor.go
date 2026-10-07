package testfiles

import (
	"encoding/base64"

	"github.com/hyscaler/qavia/api/internal/platform/paging"
)

// The file tree pages by path rather than by time.
//
// A browser is ordered by path, and paging it by created_at would make files
// appear in one order and page in another. The cursor is still opaque and base64
// encoded, so nobody builds one by hand and then depends on its shape.

func encodePathCursor(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func decodePathCursor(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", paging.Invalid()
	}
	return string(raw), nil
}
