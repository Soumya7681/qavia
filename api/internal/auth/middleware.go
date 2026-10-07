package auth

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/hyscaler/qavia/api/internal/platform/httpx"
)

// SessionMiddleware loads the session and puts the principal on the context.
//
// It is the only place a principal is created. Everything downstream reads it from
// the context, and the authorisation policy decides what that principal may do.
//
// It never rejects a request: an absent or expired session simply means no
// principal. Refusing is the policy layer's job, so a public endpoint and a
// protected one go through exactly the same code here.
func SessionMiddleware(manager *scs.SessionManager, service *Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// scs's own middleware handles loading the cookie, committing the session,
		// and writing the Set-Cookie header. Wrapping it rather than reimplementing
		// it keeps the cookie lifecycle in one well-tested place.
		return manager.LoadAndSave(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			user, found, err := service.CurrentUser(ctx)
			if err != nil {
				// A database failure here is not "unauthenticated": returning 401
				// would tell a signed-in user their session had ended when it had
				// not.
				httpx.WriteError(ctx, w, err)
				return
			}

			if found {
				ctx = httpx.WithPrincipal(ctx, httpx.Principal{
					UserID: user.ID,
					Email:  user.Email,
					Role:   user.Role,
				})
			}

			// A strict handler receives a context, never the *http.Request, so what
			// a service needs from the request has to travel on the context. This is
			// the alternative to letting a handler reach for http.Request, which the
			// layering rules forbid (backend-standards.md 2).
			ctx = WithRequestContext(ctx, RequestContextFrom(r))

			next.ServeHTTP(w, r.WithContext(ctx))
		}))
	}
}

// NewSessionManager configures the cookie and lifetime rules.
//
// The values that matter and why:
//
//   - HttpOnly, so no script can read the session token.
//   - SameSite=Lax, which blocks cross-site POSTs while keeping ordinary top-level
//     navigation working.
//   - Persist=false, so the cookie is a session cookie rather than one that
//     survives a browser restart on a shared machine.
//   - Secure in production only, because local development is plain HTTP and a
//     Secure cookie would silently never be sent.
func NewSessionManager(store scs.Store, secure bool, lifetime, idleTimeout time.Duration) *scs.SessionManager {
	manager := scs.New()
	manager.Store = store
	manager.Lifetime = lifetime
	manager.IdleTimeout = idleTimeout

	manager.Cookie.Name = SessionCookieName
	manager.Cookie.Path = "/"
	manager.Cookie.HttpOnly = true
	manager.Cookie.SameSite = http.SameSiteLaxMode
	manager.Cookie.Secure = secure
	manager.Cookie.Persist = false

	return manager
}

// SessionCookieName matches the securitySchemes entry in qavia.yaml. Changing one
// without the other silently breaks the generated client's declared contract.
const SessionCookieName = "qavia_session"

type requestContextKey struct{}

// WithRequestContext attaches the caller's IP and user agent. Only the session
// middleware calls it.
func WithRequestContext(ctx context.Context, rc RequestContext) context.Context {
	return context.WithValue(ctx, requestContextKey{}, rc)
}

// RequestFrom recovers what the session middleware attached.
//
// A zero value is a valid answer: a job worker has no request, and the throttle
// treats a missing IP as "cannot rate limit by address" rather than an error.
func RequestFrom(ctx context.Context) RequestContext {
	rc, ok := ctx.Value(requestContextKey{}).(RequestContext)
	if !ok {
		return RequestContext{}
	}
	return rc
}

// RequestContextFrom extracts what the service needs from the request.
//
// The client IP comes from RemoteAddr, deliberately not from X-Forwarded-For.
// Trusting that header without a vetted proxy in front lets any caller forge their
// own address and walk straight past the per-IP limit. When a reverse proxy is
// introduced, this is the one function to change.
func RequestContextFrom(r *http.Request) RequestContext {
	rc := RequestContext{UserAgent: r.UserAgent()}

	if addrPort, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		addr := addrPort.Addr().Unmap()
		rc.IP = &addr
	} else if addr, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		addr = addr.Unmap()
		rc.IP = &addr
	}
	return rc
}
