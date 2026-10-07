// Package setup answers "what does this installation still need" and creates the
// first admin (F-1.10).
//
// It is not in the package list in backend-standards.md 1 because first-run setup
// was not a domain when that list was written. It is its own package rather than a
// corner of auth because it reads across three of them, storage, users, and AI, and
// none of those should learn about the others to answer a wizard's question.
package setup

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/capability/objectstore"
	"github.com/hyscaler/qavia/api/internal/settings"
)

// Users is the slice of the users service this package needs.
type Users interface {
	Count(ctx context.Context) (int64, error)
}

// Accounts is the slice of the auth service this package needs.
type Accounts interface {
	CreateFirstAdmin(ctx context.Context, email, name, password, timezone string, rc auth.RequestContext) (auth.User, error)
}

// Settings is the slice of the settings service this package needs.
type Settings interface {
	String(ctx context.Context, key string, target settings.Target) (string, error)
}

// AIProviders reports whether any AI provider is usable.
//
// Nil until BE-1 lands, which is why every call site tolerates a nil: a missing AI
// provider is reported, not enforced, and it never blocks setup.
type AIProviders interface {
	AnyConfigured(ctx context.Context) (bool, error)
}

// probeTimeout bounds the storage check. Setup is a foreground request, and an
// unreachable endpoint that hangs is worse than one that fails.
const probeTimeout = 10 * time.Second

// Status is what the wizard renders.
type Status struct {
	Complete bool

	AdminExists bool

	StorageReachable bool
	StorageDetail    string
	StorageProvider  string

	AIProviderConfigured bool
}

// Service answers the setup questions.
type Service struct {
	users    Users
	accounts Accounts
	settings Settings
	store    objectstore.Store
	ai       AIProviders
}

func NewService(
	users Users,
	accounts Accounts,
	settingsService Settings,
	objects objectstore.Store,
	ai AIProviders,
) *Service {
	return &Service{
		users:    users,
		accounts: accounts,
		settings: settingsService,
		store:    objects,
		ai:       ai,
	}
}

// Status reports what is still missing.
//
// Only two things block completion: an admin, and storage that can be written to
// and read back from. A missing AI provider is reported so the wizard can offer to
// configure one, and enforced at enqueue time instead, because generation is not
// the only thing the platform does and refusing to finish setup over it would be
// wrong (BE-0.28).
func (s *Service) Status(ctx context.Context) (Status, error) {
	count, err := s.users.Count(ctx)
	if err != nil {
		return Status{}, err
	}

	provider, err := s.settings.String(ctx, "storage.provider", settings.Target{})
	if err != nil {
		return Status{}, err
	}

	status := Status{
		AdminExists:     count > 0,
		StorageProvider: provider,
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// A write and a read back, not a reachability ping. "The credentials are
	// accepted" is a different claim from "a file written here can be read back",
	// and only the second one means the platform will work.
	if err := objectstore.Probe(probeCtx, s.store); err != nil {
		status.StorageReachable = false
		status.StorageDetail = storageDetail(err)
		slog.WarnContext(ctx, "storage probe failed",
			"provider", provider, "error", err)
	} else {
		status.StorageReachable = true
		status.StorageDetail = s.store.ID()
	}

	if s.ai != nil {
		configured, err := s.ai.AnyConfigured(ctx)
		if err != nil {
			return Status{}, err
		}
		status.AIProviderConfigured = configured
	}

	status.Complete = status.AdminExists && status.StorageReachable
	return status, nil
}

// CreateAdminInput is the first account.
type CreateAdminInput struct {
	Email    string
	Name     string
	Password string
	Timezone string
}

// CreateAdmin creates the first admin and signs them in.
func (s *Service) CreateAdmin(
	ctx context.Context,
	input CreateAdminInput,
	rc auth.RequestContext,
) (auth.User, error) {
	timezone := input.Timezone
	if timezone == "" {
		// The platform default rather than a hardcoded zone, so an installation
		// that changed it gets its own answer.
		resolved, err := s.settings.String(ctx, "preferences.timezone", settings.Target{})
		if err != nil {
			return auth.User{}, err
		}
		timezone = resolved
	}

	return s.accounts.CreateFirstAdmin(ctx,
		input.Email, input.Name, input.Password, timezone, rc)
}

// storageDetail turns a probe failure into something safe to show.
//
// The driver's error can name an endpoint and, in the worst case, echo part of a
// credential in a signing failure. Only the shape of the problem is reported, and
// the full chain stays in the log.
func storageDetail(err error) string {
	switch {
	case err == nil:
		return ""
	case isTimeout(err):
		return "The storage endpoint did not respond."
	default:
		return "Storage rejected a test write. Check the bucket, the region, and the keys."
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
