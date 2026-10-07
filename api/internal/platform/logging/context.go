package logging

import "context"

// Field names used on every record. The frontend and the log search both depend
// on these being stable.
const (
	CorrelationIDField = "correlation_id"
	JobIDField         = "job_id"
)

type correlationIDKey struct{}

type jobIDKey struct{}

// WithCorrelationID puts the request or job correlation ID on the context.
//
// It lives here rather than in httpx so that the logging handler can read it
// without importing HTTP middleware, and so a worker with no HTTP request can
// set it the same way.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, correlationIDKey{}, id)
}

// CorrelationID returns the correlation ID, or the empty string.
func CorrelationID(ctx context.Context) string {
	id, ok := ctx.Value(correlationIDKey{}).(string)
	if !ok {
		return ""
	}
	return id
}

// WithJobID puts the job ID on the context. Every job log line carries it, which
// is what makes one user action traceable across five stages
// (backend-standards.md 8).
func WithJobID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, jobIDKey{}, id)
}

// JobID returns the job ID, or the empty string.
func JobID(ctx context.Context) string {
	id, ok := ctx.Value(jobIDKey{}).(string)
	if !ok {
		return ""
	}
	return id
}
