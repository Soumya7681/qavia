// Package auth owns login, sessions, invites, and password handling (F-1.1).
//
// Three rules shape this package:
//
//   - Sessions are server-side, so revocation is immediate. An offboarded
//     employee's access ends when the row is deleted, with no token TTL to wait out
//     (tech-stack.md 10).
//   - A wrong password and an unknown account are indistinguishable, in the error
//     code, the message, and the work done. Anything else leaks which accounts
//     exist.
//   - There is no self-registration. An admin invites, and the invitee sets their
//     own password from a single-use link.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// sessionUserKey is the key the user ID is stored under inside the session blob.
const sessionUserKey = "user_id"

// InviteTTL bounds how long an invitation link is usable.
const InviteTTL = 7 * 24 * time.Hour

// User is the domain type. It has no password field: a hash cannot leak through a
// type that cannot hold one.
type User struct {
	ID          uuid.UUID
	Email       string
	Name        string
	Role        role.Role
	Timezone    string
	Disabled    bool
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

// Invitation is returned once, when an invite is created.
type Invitation struct {
	User      User
	Token     string
	ExpiresAt time.Time
}

// RequestContext carries what the HTTP layer knows and a service must not reach for
// itself. A service has to be callable from a job worker, where there is no request
// (backend-standards.md 2).
type RequestContext struct {
	IP        *netip.Addr
	UserAgent string
}

// Service is the auth business logic.
type Service struct {
	db       *store.DB
	sessions *scs.SessionManager
	store    *SessionStore
	recorder *audit.Recorder

	hashParams HashParams
	throttle   throttle
}

func NewService(
	db *store.DB,
	sessions *scs.SessionManager,
	sessionStore *SessionStore,
	recorder *audit.Recorder,
) *Service {
	return &Service{
		db:         db,
		sessions:   sessions,
		store:      sessionStore,
		recorder:   recorder,
		hashParams: DefaultHashParams,
		throttle:   throttle{db: db, policy: DefaultThrottlePolicy},
	}
}

// Login authenticates and starts a session.
//
// The order matters. Throttling is checked before the password so a locked account
// costs no Argon2 work, and a missing user still pays the hash cost so the response
// time does not reveal whether the account exists.
func (s *Service) Login(ctx context.Context, email, password string, rc RequestContext) (User, error) {
	email = normalizeEmail(email)

	lock, err := s.throttle.check(ctx, email, rc.IP)
	if err != nil {
		return User{}, err
	}
	if lock.locked {
		return User{}, apierr.AccountLocked(lock.retryAfterSecond)
	}

	row, err := s.db.Queries().GetUserByEmail(ctx, email)
	if err != nil && !store.IsNotFound(err) {
		return User{}, fmt.Errorf("load user by email: %w", err)
	}

	// A nil hash is the unknown-account and never-accepted-invite case. Verifying
	// against a dummy hash keeps the timing of both indistinguishable from a wrong
	// password.
	stored := ""
	if err == nil && row.PasswordHash != nil {
		stored = *row.PasswordHash
	}
	matches := s.verifyOrDecoy(password, stored)

	if !matches {
		newLock, failErr := s.throttle.recordFailure(ctx, email, rc.IP, rc.UserAgent)
		if failErr != nil {
			return User{}, failErr
		}
		s.recorder.Record(ctx, audit.Entry{
			Action: audit.ActionLoginFailed, ActorEmail: email, Subject: email, IP: rc.IP,
		})
		if newLock.locked {
			s.recorder.Record(ctx, audit.Entry{
				Action: audit.ActionAccountLocked, ActorEmail: email, Subject: email, IP: rc.IP,
			})
			return User{}, apierr.AccountLocked(newLock.retryAfterSecond)
		}
		return User{}, apierr.InvalidCredentials()
	}

	// Disabled is checked after the password. Reporting "disabled" to someone who
	// guessed wrongly would confirm the account exists.
	if row.DisabledAt != nil {
		return User{}, apierr.AccountDisabled()
	}

	if err := s.throttle.recordSuccess(ctx, email, rc.IP, rc.UserAgent); err != nil {
		return User{}, err
	}

	// Cost parameters can be raised in settings; an old hash is upgraded on the
	// next successful login rather than by a migration nobody can run.
	if NeedsRehash(stored, s.hashParams) {
		if upgraded, hashErr := HashPassword(password, s.hashParams); hashErr == nil {
			if setErr := s.db.Queries().SetUserPassword(ctx, dbgen.SetUserPasswordParams{
				ID: row.ID, PasswordHash: &upgraded,
			}); setErr != nil {
				return User{}, fmt.Errorf("upgrade password hash: %w", setErr)
			}
		}
	}

	if err := s.startSession(ctx, row.ID); err != nil {
		return User{}, err
	}
	if err := s.db.Queries().TouchUserLogin(ctx, row.ID); err != nil {
		return User{}, fmt.Errorf("record last login: %w", err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionLogin, ActorID: &row.ID, ActorEmail: row.Email,
		Subject: row.Email, IP: rc.IP,
	})

	return toUser(row), nil
}

// Logout destroys the session row, so the cookie is useless immediately.
func (s *Service) Logout(ctx context.Context, actor User) error {
	if err := s.sessions.Destroy(ctx); err != nil {
		return fmt.Errorf("destroy session: %w", err)
	}
	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionLogout, ActorID: &actor.ID, ActorEmail: actor.Email, Subject: actor.Email,
	})
	return nil
}

// Invite creates a user with no password and returns a single-use link.
//
// The token is returned once and stored only as a hash. Email is an optional
// integration, so on a zero-integration install the admin is the delivery
// mechanism and needs to see the link.
func (s *Service) Invite(ctx context.Context, actor User, email, name string, newRole role.Role) (Invitation, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return Invitation{}, apierr.Validation("Enter a valid email address.",
			map[string]any{"field": "email"})
	}
	if !newRole.Valid() {
		return Invitation{}, apierr.Validation("Choose a valid role.",
			map[string]any{"field": "role"})
	}

	token, hashed, err := newInviteToken()
	if err != nil {
		return Invitation{}, err
	}
	expiresAt := time.Now().Add(InviteTTL)

	var created dbgen.User
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		user, txErr := q.CreateUser(ctx, dbgen.CreateUserParams{
			Email:    email,
			Name:     name,
			Role:     dbgen.UserRole(newRole),
			Timezone: "UTC",
		})
		if txErr != nil {
			if store.IsUniqueViolation(txErr) {
				return apierr.EmailAlreadyTaken()
			}
			return fmt.Errorf("create invited user: %w", txErr)
		}
		created = user

		if _, txErr = q.CreateInvitation(ctx, dbgen.CreateInvitationParams{
			UserID:    user.ID,
			TokenHash: hashed,
			ExpiresAt: expiresAt,
			InvitedBy: &actor.ID,
		}); txErr != nil {
			return fmt.Errorf("create invitation: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return Invitation{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionUserInvited, ActorID: &actor.ID, ActorEmail: actor.Email,
		Subject: email, Detail: map[string]any{"role": string(newRole)},
	})

	return Invitation{User: toUser(created), Token: token, ExpiresAt: expiresAt}, nil
}

// AcceptInvite sets the password and signs the user in.
//
// Every failure returns the same invite_invalid error: an unknown token, an expired
// one, and one already used are indistinguishable, so the endpoint cannot be used
// to probe for valid tokens.
func (s *Service) AcceptInvite(ctx context.Context, token, name, password string, rc RequestContext) (User, error) {
	if reason, ok := ValidatePassword(password); !ok {
		return User{}, apierr.PasswordTooWeak(reason)
	}

	invite, err := s.db.Queries().GetInvitationByTokenHash(ctx, hashInviteToken(token))
	if err != nil {
		if store.IsNotFound(err) {
			return User{}, apierr.InviteInvalid()
		}
		return User{}, fmt.Errorf("load invitation: %w", err)
	}
	if invite.AcceptedAt != nil || invite.ExpiresAt.Before(time.Now()) {
		return User{}, apierr.InviteInvalid()
	}

	hashed, err := HashPassword(password, s.hashParams)
	if err != nil {
		return User{}, err
	}

	var updated dbgen.User
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		// The UPDATE carries the accepted_at IS NULL condition, so two concurrent
		// accepts cannot both win.
		rows, txErr := q.AcceptInvitation(ctx, invite.ID)
		if txErr != nil {
			return fmt.Errorf("accept invitation: %w", txErr)
		}
		if rows == 0 {
			return apierr.InviteInvalid()
		}

		if txErr = q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{
			ID: invite.UserID, PasswordHash: &hashed,
		}); txErr != nil {
			return fmt.Errorf("set password: %w", txErr)
		}

		user, txErr := q.GetUserByID(ctx, invite.UserID)
		if txErr != nil {
			return fmt.Errorf("reload user: %w", txErr)
		}
		if name != "" {
			user, txErr = q.UpdateUserProfile(ctx, dbgen.UpdateUserProfileParams{
				ID: user.ID, Name: name, Timezone: user.Timezone,
			})
			if txErr != nil {
				return fmt.Errorf("set display name: %w", txErr)
			}
		}
		updated = user
		return nil
	})
	if err != nil {
		return User{}, err
	}

	if updated.DisabledAt != nil {
		return User{}, apierr.AccountDisabled()
	}

	if err := s.startSession(ctx, updated.ID); err != nil {
		return User{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionInviteAccepted, ActorID: &updated.ID, ActorEmail: updated.Email,
		Subject: updated.Email, IP: rc.IP,
	})

	return toUser(updated), nil
}

// CreateFirstAdmin makes the account that everything else is administered from.
//
// It is available only while the users table is empty, and the emptiness check
// shares the transaction with the insert: two people racing through the setup
// wizard must not produce two admins, and a check-then-write across two statements
// is exactly how that happens.
//
// This is what makes the six bootstrap variables enough to reach a working
// logged-in admin, with no seed script and no manual SQL (BE-0.28).
func (s *Service) CreateFirstAdmin(
	ctx context.Context,
	email, name, password, timezone string,
	rc RequestContext,
) (User, error) {
	email = normalizeEmail(email)
	if email == "" || !strings.Contains(email, "@") {
		return User{}, apierr.Validation("Enter a valid email address.",
			map[string]any{"field": "email"})
	}
	if strings.TrimSpace(name) == "" {
		return User{}, apierr.Validation("Enter your name.", map[string]any{"field": "name"})
	}
	if reason, ok := ValidatePassword(password); !ok {
		return User{}, apierr.PasswordTooWeak(reason)
	}

	hashed, err := HashPassword(password, s.hashParams)
	if err != nil {
		return User{}, err
	}

	var created dbgen.User
	err = s.db.InTx(ctx, func(q *dbgen.Queries) error {
		count, txErr := q.CountUsers(ctx)
		if txErr != nil {
			return fmt.Errorf("count users: %w", txErr)
		}
		if count > 0 {
			// Setup is over. The endpoint reports not found from here on, so it
			// cannot become a back door on a platform already in use.
			return apierr.SetupAlreadyComplete()
		}

		user, txErr := q.CreateUser(ctx, dbgen.CreateUserParams{
			Email:    email,
			Name:     strings.TrimSpace(name),
			Role:     dbgen.UserRole(role.Admin),
			Timezone: timezone,
		})
		if txErr != nil {
			return fmt.Errorf("create first admin: %w", txErr)
		}

		if txErr = q.SetUserPassword(ctx, dbgen.SetUserPasswordParams{
			ID: user.ID, PasswordHash: &hashed,
		}); txErr != nil {
			return fmt.Errorf("set first admin password: %w", txErr)
		}

		created = user
		return nil
	})
	if err != nil {
		return User{}, err
	}

	if err := s.startSession(ctx, created.ID); err != nil {
		return User{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionSetupCompleted, ActorID: &created.ID, ActorEmail: created.Email,
		Subject: created.Email, IP: rc.IP,
	})

	return toUser(created), nil
}

// ChangePassword replaces the caller's own password and signs out their other
// sessions, which is what a user expects a password change to do.
func (s *Service) ChangePassword(ctx context.Context, actor User, current, next string) error {
	row, err := s.db.Queries().GetUserByID(ctx, actor.ID)
	if err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	if row.PasswordHash == nil {
		return apierr.InvalidCredentials()
	}

	matches, err := VerifyPassword(current, *row.PasswordHash)
	if err != nil || !matches {
		return apierr.InvalidCredentials()
	}
	if reason, ok := ValidatePassword(next); !ok {
		return apierr.PasswordTooWeak(reason)
	}
	if current == next {
		return apierr.PasswordTooWeak("Choose a password you have not used here before.")
	}

	hashed, err := HashPassword(next, s.hashParams)
	if err != nil {
		return err
	}
	if err := s.db.Queries().SetUserPassword(ctx, dbgen.SetUserPasswordParams{
		ID: actor.ID, PasswordHash: &hashed,
	}); err != nil {
		return fmt.Errorf("set password: %w", err)
	}

	if _, err := s.store.RevokeOthers(ctx, actor.ID, s.sessions.Token(ctx)); err != nil {
		return err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionPasswordChanged, ActorID: &actor.ID, ActorEmail: actor.Email,
		Subject: actor.Email,
	})
	return nil
}

// CurrentUser loads the user behind the active session.
//
// It reads the database on every request rather than trusting the session blob, so
// a disabled account or a changed role takes effect on the next request rather than
// whenever the session happens to expire.
func (s *Service) CurrentUser(ctx context.Context) (User, bool, error) {
	raw := s.sessions.GetString(ctx, sessionUserKey)
	if raw == "" {
		return User{}, false, nil
	}

	userID, err := uuid.Parse(raw)
	if err != nil {
		// The blob holds something that is not a user ID, so the session is
		// corrupt. Destroying it beats ignoring it: otherwise the parse fails again
		// on every subsequent request and the caller can never recover without
		// clearing their cookies by hand.
		if destroyErr := s.sessions.Destroy(ctx); destroyErr != nil {
			return User{}, false, fmt.Errorf("destroy corrupt session: %w", destroyErr)
		}
		slog.WarnContext(ctx, "destroyed session with an unparseable user id", "error", err)
		return User{}, false, nil
	}

	row, err := s.db.Queries().GetUserByID(ctx, userID)
	if err != nil {
		if !store.IsNotFound(err) {
			return User{}, false, fmt.Errorf("load session user: %w", err)
		}
		// The user was deleted while the session lived. That is not a failure: it
		// means there is no authenticated caller. Destroying the session here stops
		// every later request repeating the lookup.
		if destroyErr := s.sessions.Destroy(ctx); destroyErr != nil {
			return User{}, false, fmt.Errorf("destroy orphaned session: %w", destroyErr)
		}
		return User{}, false, nil //nolint:nilerr // a missing user means no session, not a failure
	}
	if row.DisabledAt != nil {
		return User{}, false, nil
	}
	return toUser(row), true, nil
}

// startSession renews the session token and binds it to the user.
//
// RenewToken is what prevents session fixation: the token the caller arrived with
// is discarded, so a value an attacker planted before login is not the value that
// ends up authenticated.
func (s *Service) startSession(ctx context.Context, userID uuid.UUID) error {
	if err := s.sessions.RenewToken(ctx); err != nil {
		return fmt.Errorf("renew session token: %w", err)
	}
	s.sessions.Put(ctx, sessionUserKey, userID.String())

	// scs writes the row when the response is committed, so the owner column has to
	// be set after that. Commit here, then bind.
	token, expiry, err := s.sessions.Commit(ctx)
	if err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	_ = expiry

	return s.store.BindUser(ctx, token, userID)
}

// verifyOrDecoy keeps the timing of an unknown account indistinguishable from a
// wrong password by hashing against a fixed decoy when there is no stored hash.
func (s *Service) verifyOrDecoy(password, stored string) bool {
	if stored == "" {
		spendDecoyTime(password)
		return false
	}
	matches, err := VerifyPassword(password, stored)
	if err != nil {
		// A malformed stored hash is a data problem, not a wrong password. It must
		// not authenticate anybody.
		return false
	}
	return matches
}

// spendDecoyTime hashes against a fixed decoy so an unknown account costs the same
// as a wrong password. Without it, a missing user returns noticeably faster and the
// timing alone reveals which accounts exist.
func spendDecoyTime(password string) {
	matches, err := VerifyPassword(password, decoyHash)
	if err != nil {
		// The decoy stopped parsing, so the timing defence is gone. There is a unit
		// test for this; the warning is the runtime backstop.
		slog.Warn("decoy password hash is unusable; unknown accounts now respond faster than real ones",
			"error", err)
		return
	}
	if matches {
		slog.Warn("decoy password hash was guessed; replace it")
	}
}

// decoyHash is a valid Argon2id hash of a value nobody knows, used only to spend
// the same time an unknown account would otherwise skip.
const decoyHash = "$argon2id$v=19$m=19456,t=2,p=1$" +
	"c29tZXNhbHR2YWx1ZTEyMw$" +
	"J9Fo1qOAeTuBFEBBBUBrxCVUFq2P0RCPvHRi5jZlHVs"

func newInviteToken() (token string, hashed []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate invitation token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashInviteToken(token), nil
}

// hashInviteToken stores and looks up invitations by hash. A leaked database must
// not yield working invitation links.
func hashInviteToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func toUser(row dbgen.User) User {
	user := User{
		ID:        row.ID,
		Email:     row.Email,
		Name:      row.Name,
		Role:      role.Role(row.Role),
		Timezone:  row.Timezone,
		Disabled:  row.DisabledAt != nil,
		CreatedAt: row.CreatedAt,
	}
	user.LastLoginAt = row.LastLoginAt
	return user
}

// ErrNoSession is returned by helpers that require a session and found none.
var ErrNoSession = errors.New("no active session")
