package auth

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

// ThrottlePolicy bounds login attempts (F-1.2).
//
// Two independent limits, because they defend against different things. The
// per-account limit stops one account being guessed at. The per-IP limit stops one
// source spraying many accounts, which the per-account limit would never notice.
type ThrottlePolicy struct {
	Window time.Duration

	// MaxAccountFailures triggers a durable lock on the account.
	MaxAccountFailures int64

	// MaxIPFailures rejects further attempts from the address without locking any
	// account, so an attacker cannot lock a colleague out on purpose.
	MaxIPFailures int64

	LockDuration time.Duration
}

// DefaultThrottlePolicy is what the settings registry (BE-0.13) declares as the
// code default.
var DefaultThrottlePolicy = ThrottlePolicy{
	Window:             15 * time.Minute,
	MaxAccountFailures: 5,
	MaxIPFailures:      25,
	LockDuration:       15 * time.Minute,
}

// throttle evaluates the limits and records attempts.
//
// Counters live in Postgres rather than Redis. The plan called for Redis, and this
// is a deliberate deviation: login_attempts is already the durable record an admin
// inspects, two sources of truth for one counter is worse than one, and login
// volume on an internal tool for ten concurrent users does not need a second
// datastore. It also keeps the Asynq-versus-River question open, since that turns
// on how much Redis is load-bearing.
type throttle struct {
	db     *store.DB
	policy ThrottlePolicy
}

// lockState describes why a login is being refused before any password is checked.
type lockState struct {
	locked           bool
	retryAfterSecond int
}

// check evaluates both limits. It runs before the password is verified, so a locked
// account costs no Argon2 work.
func (t *throttle) check(ctx context.Context, email string, ip *netip.Addr) (lockState, error) {
	lock, err := t.db.Queries().GetAccountLock(ctx, email)
	switch {
	case err == nil:
		return lockState{
			locked:           true,
			retryAfterSecond: secondsUntil(lock.LockedUntil),
		}, nil
	case !store.IsNotFound(err):
		return lockState{}, fmt.Errorf("read account lock: %w", err)
	}

	if ip == nil {
		return lockState{}, nil
	}

	failures, err := t.db.Queries().CountRecentFailedAttemptsByIP(ctx,
		dbgen.CountRecentFailedAttemptsByIPParams{
			Ip:    *ip,
			Since: time.Now().Add(-t.policy.Window),
		})
	if err != nil {
		return lockState{}, fmt.Errorf("count failures by ip: %w", err)
	}

	if failures >= t.policy.MaxIPFailures {
		// No account is locked here on purpose. Locking on an IP limit would let
		// anyone lock out a colleague by guessing at their email from a shared
		// office address.
		return lockState{
			locked:           true,
			retryAfterSecond: int(t.policy.Window.Seconds()),
		}, nil
	}
	return lockState{}, nil
}

// recordFailure logs the attempt and locks the account once it crosses the limit.
// It returns the resulting lock state so the caller can tell the user how long to
// wait.
func (t *throttle) recordFailure(ctx context.Context, email string, ip *netip.Addr, userAgent string) (lockState, error) {
	if err := t.db.Queries().RecordLoginAttempt(ctx, dbgen.RecordLoginAttemptParams{
		Email:     email,
		Ip:        ip,
		Succeeded: false,
		UserAgent: userAgent,
	}); err != nil {
		return lockState{}, fmt.Errorf("record failed attempt: %w", err)
	}

	failures, err := t.db.Queries().CountRecentFailedAttempts(ctx,
		dbgen.CountRecentFailedAttemptsParams{
			Email: email,
			Since: time.Now().Add(-t.policy.Window),
		})
	if err != nil {
		return lockState{}, fmt.Errorf("count failures: %w", err)
	}

	if failures < t.policy.MaxAccountFailures {
		return lockState{}, nil
	}

	lockedUntil := time.Now().Add(t.policy.LockDuration)
	if err := t.db.Queries().LockAccount(ctx, dbgen.LockAccountParams{
		Email:       email,
		LockedUntil: lockedUntil,
		Reason:      "too_many_failed_attempts",
	}); err != nil {
		return lockState{}, fmt.Errorf("lock account: %w", err)
	}

	return lockState{locked: true, retryAfterSecond: secondsUntil(lockedUntil)}, nil
}

// recordSuccess logs the attempt and clears any lock.
func (t *throttle) recordSuccess(ctx context.Context, email string, ip *netip.Addr, userAgent string) error {
	if err := t.db.Queries().RecordLoginAttempt(ctx, dbgen.RecordLoginAttemptParams{
		Email:     email,
		Ip:        ip,
		Succeeded: true,
		UserAgent: userAgent,
	}); err != nil {
		return fmt.Errorf("record successful attempt: %w", err)
	}
	if _, err := t.db.Queries().ClearAccountLock(ctx, email); err != nil {
		return fmt.Errorf("clear account lock: %w", err)
	}
	return nil
}

func secondsUntil(t time.Time) int {
	remaining := int(time.Until(t).Seconds())
	if remaining < 1 {
		return 1
	}
	return remaining
}
