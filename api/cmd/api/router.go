package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/go-chi/chi/v5"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/hyscaler/qavia/api/internal/jobs"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// webhookBodyLimit bounds the raw body kept for signature verification. A trigger
// payload is a few fields; anything larger is not a webhook this platform sent
// somebody the token for.
const webhookBodyLimit = 1 << 20

// newRouter assembles the HTTP handler.
//
// Middleware order is load-bearing and is the reason this lives in one readable
// function:
//
//  1. Recoverer, so a panic anywhere below becomes a 500 with an incident ID
//     rather than taking down a process that also runs the queue.
//  2. CorrelationID, so every log line and every downstream call carries it.
//  3. otelhttp, so the span wraps everything measurable.
//  4. RequestLogger, inside tracing so the logged duration matches the span.
//  5. Session, which populates the principal. It precedes the validator because
//     the validator's authentication hook reads the principal.
//  6. The request validator, which rejects a malformed body before a handler runs.
//  7. The authorisation policy, applied as strict middleware so it sees the
//     operation ID.
//  8. Any additional strict middleware, currently project membership, which runs
//     after authorisation because it is a second, narrower question: the policy
//     table says whether the role may call the operation at all, and membership
//     says whether this caller may touch this project.
func newRouter(
	server api.StrictServerInterface,
	policies httpx.PolicySet,
	session func(http.Handler) http.Handler,
	strictMiddleware ...api.StrictMiddlewareFunc,
) (http.Handler, error) {
	spec, err := api.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded openapi spec: %w", err)
	}

	// The spec declares a relative server URL for the browser client. Leaving it
	// in makes the validator try to match request paths against it and reject
	// everything.
	spec.Servers = nil

	validator := nethttpmiddleware.OapiRequestValidatorWithOptions(spec, &nethttpmiddleware.Options{
		Options: openapi3filter.Options{
			// The scheme is declared in the spec so the generated client and the
			// docs describe it. Enforcement is this hook plus the policy table:
			// kin-openapi only checks that a credential is present, and "present"
			// is not "valid".
			AuthenticationFunc: func(ctx context.Context, _ *openapi3filter.AuthenticationInput) error {
				if _, ok := httpx.CurrentUser(ctx); !ok {
					return apierr.Unauthenticated()
				}
				return nil
			},
		},
		ErrorHandlerWithOpts: func(ctx context.Context, err error, w http.ResponseWriter, _ *http.Request, _ nethttpmiddleware.ErrorHandlerOpts) {
			// Without this, a schema violation returns kin-openapi's plain-text
			// error and the client has no code to branch on.
			httpx.WriteError(ctx, w, validationError(err))
		},
	})

	router := chi.NewRouter()
	router.Use(
		httpx.Recoverer,
		httpx.CorrelationID,
		otelHandler,
		httpx.RequestLogger,
		// The inbound webhook is authenticated by an HMAC over the bytes as sent,
		// so those bytes are kept before anything decodes them. Only that prefix is
		// captured: holding every upload in memory would undo the streaming that
		// BE-0.26 exists to guarantee.
		httpx.CaptureRawBody(webhookBodyLimit, jobs.WebhookPathPrefix),
		session,
		validator,
	)

	// Strict middleware, applied to every operation. Authorisation first, so a
	// caller with no business calling the operation at all never reaches a check
	// that would tell them whether a particular project exists.
	//
	// Both error hooks are replaced, and they are deliberately different. The
	// generated defaults call http.Error, which writes plain text and leaves the
	// client with no code to branch on.
	strict := api.NewStrictHandlerWithOptions(server,
		append([]api.StrictMiddlewareFunc{authorize(policies)}, strictMiddleware...),
		api.StrictHTTPServerOptions{
			// Fires only when the body cannot be decoded into the generated type.
			// That is the caller's fault, so it must be a 400: routing it through
			// the generic writer would report a 500 for a malformed email.
			RequestErrorHandlerFunc: writeDecodeError,

			// Fires for an error a handler returned, and for a failure writing the
			// response. Domain errors keep their own status here.
			ResponseErrorHandlerFunc: writeError,
		})

	return api.HandlerWithOptions(strict, api.ChiServerOptions{
		BaseRouter: router,
		// Fires when a path or query parameter cannot be parsed, before any
		// handler runs.
		ErrorHandlerFunc: writeError,
	}), nil
}

// authorize turns the policy table into strict middleware. The generated wrapper
// hands us the operation ID, which is what makes a single table possible.
func authorize(policies httpx.PolicySet) api.StrictMiddlewareFunc {
	return func(next api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			if err := policies.Authorize(ctx, operationID); err != nil {
				return nil, err
			}
			return next(ctx, w, r, request)
		}
	}
}

// otelHandler wraps every request in a span. The route pattern is used as the span
// name rather than the raw path, so /projects/{id} does not produce one span name
// per project.
func otelHandler(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "api",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
				if pattern := routeCtx.RoutePattern(); pattern != "" {
					return r.Method + " " + pattern
				}
			}
			return r.Method + " " + r.URL.Path
		}),
	)
}

// validationError maps a validator failure to a domain error.
//
// Two cases matter beyond the generic one. An authentication failure surfaced by
// the security hook keeps its 401 rather than becoming a 400. A request for a path
// the spec does not describe is a 404: the validator sees it first, because it is
// router-level middleware, and reporting "bad request" for a wrong URL is
// misleading.
func validationError(err error) error {
	if domain, ok := apierr.As(err); ok {
		return domain
	}
	if errors.Is(err, routers.ErrPathNotFound) || errors.Is(err, routers.ErrMethodNotAllowed) {
		return apierr.NotFound("Endpoint")
	}

	// kin-openapi's Error() is a multi-line dump that includes the schema and the
	// offending value. It is useful in a log and unreadable in a UI, so the first
	// line becomes the message and the rest goes in details for a developer.
	message, detail := splitValidationMessage(err.Error())

	var details map[string]any
	if detail != "" {
		details = map[string]any{"validation": detail}
	}
	return apierr.Validation(message, details)
}

// splitValidationMessage takes the first line as the human-facing message and keeps
// the remainder as developer detail.
func splitValidationMessage(text string) (message, detail string) {
	message, detail, _ = strings.Cut(text, "\n")

	// The prefix names the plumbing rather than the problem.
	message = strings.TrimPrefix(message, "request body has an error: ")
	message = strings.TrimPrefix(message,
		"doesn't match schema: ")

	// "validation failed due to: at '/email': ..." reads better without the lead-in.
	if _, after, found := strings.Cut(message, "validation failed due to: "); found {
		message = after
	}

	return strings.TrimSpace(message), strings.TrimSpace(detail)
}

// writeError converts any handler or parameter error into the standard envelope,
// so a domain error raised deep in a service reaches the client with its code
// intact and never as a bare 500 or a plain-text body.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(r.Context(), w, err)
}

// writeDecodeError reports a body the generated types could not accept.
//
// The generated decoder enforces the formats declared in the spec, so a value like
// an invalid email address fails here rather than in the schema validator. It is a
// client error either way, and the caller needs a code to branch on.
func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	message, detail := splitValidationMessage(
		strings.TrimPrefix(err.Error(), "can't decode JSON body: "))

	var details map[string]any
	if detail != "" {
		details = map[string]any{"validation": detail}
	}
	httpx.WriteError(r.Context(), w, apierr.Validation(message, details))
}
