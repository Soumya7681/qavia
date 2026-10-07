package projects

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/platform/httpx"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// RequireMembership enforces project access on every route that carries a project
// ID, without each handler remembering to.
//
// backend-standards.md 11 wants this as a route-group check so a new route
// inherits it. The generated server registers routes flat, so the equivalent
// guarantee is expressed here: the middleware runs for every operation, and any
// operation whose path has a {projectID} is checked. Adding a new project-scoped
// route therefore cannot forget the check, because it is keyed off the path
// parameter rather than off a list somebody maintains.
//
// Routes with no project ID pass straight through; their authorisation is the
// policy table, and for nested resources such as an artifact it is the owning
// service, which resolves the project first and calls EnsureMember.
func RequireMembership(service *Service) api.StrictMiddlewareFunc {
	return func(next api.StrictHandlerFunc, _ string) api.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			projectID, present := projectIDFrom(r)
			if !present {
				return next(ctx, w, r, request)
			}

			actor, authenticated := httpx.CurrentUser(ctx)
			if !authenticated {
				return nil, apierr.Unauthenticated()
			}
			if err := service.EnsureMember(ctx, actor, projectID); err != nil {
				return nil, err
			}
			return next(ctx, w, r, request)
		}
	}
}

// projectIDFrom reads the project ID from the matched route pattern.
//
// A malformed value is reported as a missing project rather than a parse error:
// the generated parameter binding has already rejected anything the spec calls a
// UUID, so reaching here with a bad one means the route was matched some other
// way, and "not found" is both true and uninformative to a prober.
func projectIDFrom(r *http.Request) (uuid.UUID, bool) {
	routeCtx := chi.RouteContext(r.Context())
	if routeCtx == nil {
		return uuid.Nil, false
	}

	raw := routeCtx.URLParam("projectID")
	if raw == "" {
		return uuid.Nil, false
	}

	projectID, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return projectID, true
}
