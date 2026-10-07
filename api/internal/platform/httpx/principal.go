package httpx

import (
	"context"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/role"
)

// Principal is the authenticated caller, as the HTTP layer sees it.
//
// It carries the minimum a request needs and deliberately not a full user
// record: a platform package must not depend on a domain package, and a handler
// that wants more should ask the users service for it.
type Principal struct {
	UserID uuid.UUID
	Email  string
	Role   role.Role
}

type principalKey struct{}

// WithPrincipal attaches the authenticated caller. Only the session middleware
// calls this.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// CurrentUser returns the authenticated caller and whether there is one.
func CurrentUser(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// MustCurrentUser returns the authenticated caller.
//
// It panics when there is none, which is correct: every route that reaches a
// handler has already passed RequireAuth, so a missing principal is a wiring bug
// and not a runtime condition to handle. The recovery middleware turns it into a
// 500 with an incident ID rather than killing the process.
func MustCurrentUser(ctx context.Context) Principal {
	p, ok := CurrentUser(ctx)
	if !ok {
		panic("httpx: no principal on context; route is missing RequireAuth")
	}
	return p
}
