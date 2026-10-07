package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// SessionStore is a scs.CtxStore backed by the same pgx pool as everything else.
//
// alexedwards/scs ships a Postgres store, but it takes a database/sql handle. Using
// it would mean a second connection path to the same database and session SQL
// living outside a store, so the four methods are implemented here over the
// generated queries instead (backend-standards.md 9).
type SessionStore struct {
	db *store.DB
}

func NewSessionStore(db *store.DB) *SessionStore {
	return &SessionStore{db: db}
}

// FindCtx returns the session data if the token exists and has not expired.
//
// scs treats found=false as "no session" and expects no error, so an expired or
// missing row is not an error condition.
func (s *SessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	row, err := s.db.Queries().FindSession(ctx, token)
	if err != nil {
		if store.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("find session: %w", err)
	}
	return row.Data, true, nil
}

func (s *SessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	if err := s.db.Queries().UpsertSession(ctx, dbgen.UpsertSessionParams{
		Token:  token,
		Data:   data,
		Expiry: expiry,
	}); err != nil {
		return fmt.Errorf("commit session: %w", err)
	}
	return nil
}

func (s *SessionStore) DeleteCtx(ctx context.Context, token string) error {
	if _, err := s.db.Queries().DeleteSession(ctx, token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// AllCtx satisfies scs.IterableCtxStore. scs uses it for bulk operations; nothing
// in Qavia iterates sessions, but implementing it costs one query and stops a
// future caller from silently getting an unsupported-operation error.
func (s *SessionStore) AllCtx(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.db.Queries().ListAllSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	out := make(map[string][]byte, len(rows))
	for _, row := range rows {
		out[row.Token] = row.Data
	}
	return out, nil
}

// The non-context methods exist because scs.Store still requires them. They are
// unreachable in this codebase: the manager is always used with a request context,
// and CtxStore takes precedence. Panicking rather than silently using
// context.Background makes a regression obvious instead of untraceable.
func (s *SessionStore) Find(string) ([]byte, bool, error) {
	panic("auth: SessionStore.Find called without a context; use FindCtx")
}

func (s *SessionStore) Commit(string, []byte, time.Time) error {
	panic("auth: SessionStore.Commit called without a context; use CommitCtx")
}

func (s *SessionStore) Delete(string) error {
	panic("auth: SessionStore.Delete called without a context; use DeleteCtx")
}

// Owner-aware operations. These are the reason for the extra user_id column.

// BindUser records which user a session belongs to, immediately after
// authentication.
func (s *SessionStore) BindUser(ctx context.Context, token string, userID uuid.UUID) error {
	rows, err := s.db.Queries().SetSessionUser(ctx, dbgen.SetSessionUserParams{
		Token:  token,
		UserID: &userID,
	})
	if err != nil {
		return fmt.Errorf("bind session to user: %w", err)
	}
	if rows == 0 {
		return errors.New("bind session to user: session row is missing")
	}
	return nil
}

// RevokeUser destroys every session a user holds. This is offboarding, disabling an
// account, and the admin "sign out everywhere" action.
func (s *SessionStore) RevokeUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	rows, err := s.db.Queries().DeleteSessionsForUser(ctx, &userID)
	if err != nil {
		return 0, fmt.Errorf("revoke sessions: %w", err)
	}
	return rows, nil
}

// RevokeOthers destroys every session except the current one, which is what a
// password change should do.
func (s *SessionStore) RevokeOthers(ctx context.Context, userID uuid.UUID, keepToken string) (int64, error) {
	rows, err := s.db.Queries().DeleteOtherSessionsForUser(ctx, dbgen.DeleteOtherSessionsForUserParams{
		UserID: &userID,
		Token:  keepToken,
	})
	if err != nil {
		return 0, fmt.Errorf("revoke other sessions: %w", err)
	}
	return rows, nil
}

// CountForUser backs the admin view of how many devices a user is signed in on.
func (s *SessionStore) CountForUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	count, err := s.db.Queries().CountSessionsForUser(ctx, &userID)
	if err != nil {
		return 0, fmt.Errorf("count sessions: %w", err)
	}
	return count, nil
}

// DeleteExpired is called by the internal scheduler. scs does not prune a custom
// store on its own.
func (s *SessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	rows, err := s.db.Queries().DeleteExpiredSessions(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return rows, nil
}
