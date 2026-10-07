package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
)

type rawBodyKey struct{}

// CaptureRawBody keeps the exact bytes of a request body for the handlers that
// need them.
//
// A signed webhook is authenticated over the bytes as sent, and the generated
// server hands a handler a decoded struct: re-encoding that struct produces
// different bytes, and the signature would never verify. So the raw body is kept
// here, once, for the paths that need it, and the request is given a fresh reader
// so the validator and the decoder still see a complete body.
//
// It applies to listed prefixes rather than to every request, because holding
// every upload in memory would undo the streaming that BE-0.26 exists to
// guarantee.
func CaptureRawBody(maxBytes int64, prefixes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body == nil || !matchesPrefix(r.URL.Path, prefixes) {
				next.ServeHTTP(w, r)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes))
			if err != nil {
				WriteError(r.Context(), w, err)
				return
			}
			// The original body is replaced rather than closed and forgotten: what
			// follows still has to read it.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))

			next.ServeHTTP(w, r.WithContext(
				context.WithValue(r.Context(), rawBodyKey{}, body)))
		})
	}
}

// RawBody returns the captured bytes, or nil where the path was not captured.
func RawBody(ctx context.Context) []byte {
	body, ok := ctx.Value(rawBodyKey{}).([]byte)
	if !ok {
		return nil
	}
	return body
}

func matchesPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
