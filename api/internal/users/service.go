// Package users owns user records and roles (F-1.3).
//
// It holds the administration operations. Login, sessions, and passwords belong to
// the auth package, which owns the credential side; this package owns who exists
// and what they may do.
package users

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/capability/notifier"
	"github.com/hyscaler/qavia/api/internal/platform/apierr"
	"github.com/hyscaler/qavia/api/internal/role"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// MaxPageSize caps every list. No unbounded list, even where "it will only ever be
// a few rows" (backend-standards.md 9).
const MaxPageSize = 200

// DefaultPageSize applies when a caller does not ask.
const DefaultPageSize = 50

// Sessions is the slice of session handling this package needs, declared here by the
// consumer rather than taken as a whole session manager (backend-standards.md 3).
type Sessions interface {
	// RevokeUser destroys every session a user holds, and returns how many.
	RevokeUser(ctx context.Context, userID uuid.UUID) (int64, error)
}

// Service is the user administration logic.
type Service struct {
	db       *store.DB
	sessions Sessions
	recorder *audit.Recorder
}

func NewService(db *store.DB, sessions Sessions, recorder *audit.Recorder) *Service {
	return &Service{db: db, sessions: sessions, recorder: recorder}
}

// Page is one page of users plus the cursor for the next.
type Page struct {
	Items      []auth.User
	NextCursor string
}

// List returns a page of users, newest first.
func (s *Service) List(ctx context.Context, limit int, cursor string) (Page, error) {
	limit = clampLimit(limit)

	params := dbgen.ListUsersParams{PageSize: int32(limit + 1)}
	if cursor != "" {
		at, err := decodeCursor(cursor)
		if err != nil {
			return Page{}, err
		}
		params.Cursor = &at
	}

	rows, err := s.db.Queries().ListUsers(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list users: %w", err)
	}

	// One row beyond the page is fetched to learn whether another page exists,
	// which avoids a second count query and cannot disagree with it.
	page := Page{Items: make([]auth.User, 0, limit)}
	for i, row := range rows {
		if i == limit {
			page.NextCursor = encodeCursor(rows[i-1].CreatedAt)
			break
		}
		page.Items = append(page.Items, toUser(row))
	}
	return page, nil
}

// Get loads one user.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (auth.User, error) {
	row, err := s.db.Queries().GetUserByID(ctx, id)
	if err != nil {
		if store.IsNotFound(err) {
			return auth.User{}, apierr.UserNotFound(id)
		}
		return auth.User{}, fmt.Errorf("load user: %w", err)
	}
	return toUser(row), nil
}

// SetRole changes a user's role.
//
// An admin cannot change their own role: locking the last admin out of their own
// instance is a support call nobody wants, and the check costs one comparison.
//
// Every session the user holds is destroyed, so the new role applies on their next
// request instead of whenever their session happens to end.
func (s *Service) SetRole(ctx context.Context, actor auth.User, id uuid.UUID, newRole role.Role) (auth.User, error) {
	if !newRole.Valid() {
		return auth.User{}, apierr.Validation("Choose a valid role.", map[string]any{"field": "role"})
	}
	if actor.ID == id {
		return auth.User{}, apierr.CannotChangeOwnRole()
	}

	existing, err := s.Get(ctx, id)
	if err != nil {
		return auth.User{}, err
	}
	if existing.Role == newRole {
		return existing, nil
	}

	if err := s.db.Queries().SetUserRole(ctx, dbgen.SetUserRoleParams{
		ID: id, Role: dbgen.UserRole(newRole),
	}); err != nil {
		return auth.User{}, fmt.Errorf("set role: %w", err)
	}

	if _, err := s.sessions.RevokeUser(ctx, id); err != nil {
		return auth.User{}, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionRoleChanged, ActorID: &actor.ID, ActorEmail: actor.Email,
		Subject: existing.Email,
		Detail:  map[string]any{"from": string(existing.Role), "to": string(newRole)},
	})

	return s.Get(ctx, id)
}

// SetDisabled enables or disables an account.
//
// Disabling destroys every session, which is the offboarding path: access ends
// here rather than when a token would have expired.
func (s *Service) SetDisabled(ctx context.Context, actor auth.User, id uuid.UUID, disabled bool) (auth.User, error) {
	if actor.ID == id && disabled {
		return auth.User{}, apierr.Forbidden().
			WithMessage("You cannot disable your own account. Ask another admin.")
	}

	existing, err := s.Get(ctx, id)
	if err != nil {
		return auth.User{}, err
	}

	var disabledAt *time.Time
	if disabled {
		now := time.Now()
		disabledAt = &now
	}
	if err := s.db.Queries().SetUserDisabled(ctx, dbgen.SetUserDisabledParams{
		ID: id, DisabledAt: disabledAt,
	}); err != nil {
		return auth.User{}, fmt.Errorf("set disabled: %w", err)
	}

	action := audit.ActionUserEnabled
	if disabled {
		action = audit.ActionUserDisabled
		if _, err := s.sessions.RevokeUser(ctx, id); err != nil {
			return auth.User{}, err
		}
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: action, ActorID: &actor.ID, ActorEmail: actor.Email, Subject: existing.Email,
	})

	return s.Get(ctx, id)
}

// RevokeSessions signs a user out everywhere.
func (s *Service) RevokeSessions(ctx context.Context, actor auth.User, id uuid.UUID) (int64, error) {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return 0, err
	}

	revoked, err := s.sessions.RevokeUser(ctx, id)
	if err != nil {
		return 0, err
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionSessionsRevoked, ActorID: &actor.ID, ActorEmail: actor.Email,
		Subject: existing.Email, Detail: map[string]any{"revoked": revoked},
	})
	return revoked, nil
}

// Unlock clears a login lockout without waiting for it to expire.
func (s *Service) Unlock(ctx context.Context, actor auth.User, id uuid.UUID) error {
	existing, err := s.Get(ctx, id)
	if err != nil {
		return err
	}

	if _, err := s.db.Queries().ClearAccountLock(ctx, existing.Email); err != nil {
		return fmt.Errorf("clear account lock: %w", err)
	}

	s.recorder.Record(ctx, audit.Entry{
		Action: audit.ActionAccountUnlock, ActorID: &actor.ID, ActorEmail: actor.Email,
		Subject: existing.Email,
	})
	return nil
}

// Recipient resolves a user into somebody a notification can be addressed to.
//
// It exists so the jobs and notifications packages never touch the users table:
// they declare the one method they need and this satisfies it
// (backend-standards.md 3).
func (s *Service) Recipient(ctx context.Context, id uuid.UUID) (notifier.Recipient, error) {
	user, err := s.Get(ctx, id)
	if err != nil {
		return notifier.Recipient{}, err
	}
	return notifier.Recipient{UserID: user.ID, Email: user.Email, Name: user.Name}, nil
}

// Admins returns every enabled admin.
//
// Used for alerts that are nobody's personal notification: an integration that
// degraded, storage that stopped answering. A disabled account is left out, because
// it is not somebody who will read it.
func (s *Service) Admins(ctx context.Context) ([]notifier.Recipient, error) {
	rows, err := s.db.Queries().ListUsersByRole(ctx, dbgen.UserRole(role.Admin))
	if err != nil {
		return nil, fmt.Errorf("list admins: %w", err)
	}

	admins := make([]notifier.Recipient, 0, len(rows))
	for _, row := range rows {
		admins = append(admins, notifier.Recipient{
			UserID: row.ID, Email: row.Email, Name: row.Name,
		})
	}
	return admins, nil
}

// Count backs the first-run check: the setup endpoint is available only while no
// user exists.
func (s *Service) Count(ctx context.Context) (int64, error) {
	count, err := s.db.Queries().CountUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}

func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
	}
}

func toUser(row dbgen.User) auth.User {
	return auth.User{
		ID:          row.ID,
		Email:       row.Email,
		Name:        row.Name,
		Role:        role.Role(row.Role),
		Timezone:    row.Timezone,
		Disabled:    row.DisabledAt != nil,
		LastLoginAt: row.LastLoginAt,
		CreatedAt:   row.CreatedAt,
	}
}
