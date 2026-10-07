// Package logging builds the slog handler, including secret redaction.
//
// NFR-10 ("no secret is ever written to application logs") is enforced here, by
// the handler, rather than by discipline at every call site. A call site that
// forgets is the normal case, not the exception, so the guarantee has to live
// somewhere a forgetful caller cannot bypass.
//
// Two things the handler does that a plain JSON handler does not:
//
//   - Redacts by key pattern at any nesting depth, including inside structs and
//     maps passed as a single attribute value. See redact.go.
//   - Attaches the correlation ID carried on the context, so tracing one user
//     action across five job stages does not depend on threading a logger.
package logging

import (
	"context"
	"io"
	"log/slog"
)

// Options configures the handler. Writer and Level both have working defaults so
// a test can call New(Options{}) and get something usable.
type Options struct {
	// Writer receives the JSON lines. Defaults to os.Stderr via the caller.
	Writer io.Writer

	// Level may be a *slog.LevelVar so the level becomes a settings-driven value
	// without rebuilding the logger. Defaults to Info.
	Level slog.Leveler

	// AddSource includes file and line. Useful in development, noisy in
	// production.
	AddSource bool
}

// New builds the logger. It is the only constructor.
//
// main calls slog.SetDefault with the result, so ad-hoc logging anywhere in the
// process goes through this handler and cannot bypass redaction. Long-lived
// services still take a *slog.Logger as a constructor argument, because a
// service that reaches for a global is a service that cannot be tested with its
// own buffer.
func New(opts Options) *slog.Logger {
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}

	inner := slog.NewJSONHandler(opts.Writer, &slog.HandlerOptions{
		Level:       opts.Level,
		AddSource:   opts.AddSource,
		ReplaceAttr: redactAttr,
	})

	return slog.New(&contextHandler{inner: inner})
}

// ParseLevel maps a settings value to a level. An unrecognised value is Info
// rather than an error: a typo in a log-level setting must not stop the process.
func ParseLevel(s string) slog.Level {
	switch s {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN", "warning":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// contextHandler copies context-carried values onto every record.
//
// slog already threads context.Context into Handle, and the correlation ID is
// already on the context in every Go call path, so nothing else has to be
// plumbed for correlation to work.
type contextHandler struct {
	inner slog.Handler
}

func (h *contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String(CorrelationIDField, id))
	}
	if id := JobID(ctx); id != "" {
		r.AddAttrs(slog.String(JobIDField, id))
	}
	return h.inner.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{inner: h.inner.WithGroup(name)}
}
