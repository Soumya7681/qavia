package setup

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	api "github.com/hyscaler/qavia/api/openapi/gen"
)

// Rate limit for the admin endpoint.
//
// It is unauthenticated and it creates an account, so it is limited even though it
// stops existing the moment a user does: the window between a database being
// created and an admin claiming it is exactly when somebody else must not be able
// to hammer it.
const (
	adminAttempts = 5
	adminWindow   = 15 * time.Minute
)

// Handler implements the setup slice of the generated server interface.
type Handler struct {
	service *Service
	limiter *limiter
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service, limiter: newLimiter(adminAttempts, adminWindow)}
}

func (h *Handler) GetSetupStatus(
	ctx context.Context,
	_ api.GetSetupStatusRequestObject,
) (api.GetSetupStatusResponseObject, error) {
	status, err := h.service.Status(ctx)
	if err != nil {
		return nil, err
	}

	body := api.SetupStatus{
		Complete:             status.Complete,
		AdminExists:          status.AdminExists,
		StorageReachable:     status.StorageReachable,
		StorageProvider:      status.StorageProvider,
		AiProviderConfigured: status.AIProviderConfigured,
	}
	if status.StorageDetail != "" {
		detail := status.StorageDetail
		body.StorageDetail = &detail
	}
	return api.GetSetupStatus200JSONResponse(body), nil
}

func (h *Handler) CreateFirstAdmin(
	ctx context.Context,
	request api.CreateFirstAdminRequestObject,
) (api.CreateFirstAdminResponseObject, error) {
	rc := auth.RequestFrom(ctx)

	if retryAfter, allowed := h.limiter.allow(rc.IP); !allowed {
		return nil, apierr.RateLimited(int(retryAfter.Seconds()))
	}

	input := CreateAdminInput{
		Email:    string(request.Body.Email),
		Name:     request.Body.Name,
		Password: request.Body.Password,
	}
	if request.Body.Timezone != nil {
		input.Timezone = *request.Body.Timezone
	}

	user, err := h.service.CreateAdmin(ctx, input, rc)
	if err != nil {
		return nil, err
	}
	return api.CreateFirstAdmin201JSONResponse(auth.ToAPIUser(user)), nil
}

// limiter is a fixed-window counter per address.
//
// In-process on purpose. The login throttle is in Postgres because it defends a
// long-lived surface across every replica; this defends a surface that exists for
// minutes on a brand new installation, and a table plus a migration to cover it
// would be more machinery than the risk deserves.
type limiter struct {
	attempts int
	window   time.Duration

	mu    sync.Mutex
	seen  map[string]*bucket
	swept time.Time
}

type bucket struct {
	count   int
	resetAt time.Time
}

func newLimiter(attempts int, window time.Duration) *limiter {
	return &limiter{
		attempts: attempts,
		window:   window,
		seen:     make(map[string]*bucket),
		swept:    time.Now(),
	}
}

// allow records an attempt and reports whether it is permitted.
func (l *limiter) allow(ip *netip.Addr) (time.Duration, bool) {
	key := "unknown"
	if ip != nil {
		key = ip.String()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.sweep(now)

	entry, seen := l.seen[key]
	if !seen || now.After(entry.resetAt) {
		l.seen[key] = &bucket{count: 1, resetAt: now.Add(l.window)}
		return 0, true
	}

	entry.count++
	if entry.count > l.attempts {
		return time.Until(entry.resetAt), false
	}
	return 0, true
}

// sweep drops expired buckets, so a stream of addresses cannot grow the map
// without bound.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < l.window {
		return
	}
	for key, entry := range l.seen {
		if now.After(entry.resetAt) {
			delete(l.seen, key)
		}
	}
	l.swept = now
}
