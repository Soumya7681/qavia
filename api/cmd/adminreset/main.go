// Command adminreset sets a user's password and clears their lockout.
//
// It exists for the state every self-hosted platform eventually reaches: nobody can
// log in. An admin forgot their password on an install with no SMTP configured, or a
// lockout is holding after a brute-force attempt, and there is no second admin to
// fix it from the UI.
//
// It grants nothing that server access does not already grant. It needs
// DATABASE_URL and QAVIA_SECRET_KEY, which is to say it needs the database and the
// key that decrypts everything in it; anybody who can run this could already read
// the whole installation. What it avoids is the worse alternative: a permanent
// unauthenticated reset endpoint in the API.
//
//	adminreset -email admin@example.com                  # prompts for a password
//	adminreset -email admin@example.com -password '…'    # for a provisioning script
//	adminreset -email admin@example.com -unlock-only     # clears a lockout only
//
// Every reset is written to the audit log with no actor, the same as any other thing
// the system does to itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/hyscaler/qavia/api/internal/audit"
	"github.com/hyscaler/qavia/api/internal/auth"
	"github.com/hyscaler/qavia/api/internal/platform/config"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

func main() {
	var (
		email      = flag.String("email", "", "the account to reset")
		password   = flag.String("password", "", "the new password; prompted for when omitted")
		unlockOnly = flag.Bool("unlock-only", false, "clear the lockout and leave the password alone")
	)
	flag.Parse()

	if strings.TrimSpace(*email) == "" {
		fail("-email is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, strings.ToLower(strings.TrimSpace(*email)), *password, *unlockOnly); err != nil {
		fail("%v", err)
	}
}

func run(ctx context.Context, email, password string, unlockOnly bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, store.Options{DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		return err
	}
	defer db.Close()

	user, err := db.Queries().GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Named plainly: this is an operator at a terminal on their own server,
			// and the "do not confirm whether an account exists" rule protects a
			// remote caller, not this one.
			return fmt.Errorf("no account with the email %q", email)
		}
		return fmt.Errorf("read the account: %w", err)
	}

	recorder := audit.NewRecorder(db)

	if _, err := db.Queries().ClearAccountLock(ctx, email); err != nil {
		return fmt.Errorf("clear the lockout: %w", err)
	}
	fmt.Printf("cleared any lockout on %s\n", email)

	if unlockOnly {
		recorder.Record(ctx, audit.Entry{
			Action:  audit.ActionAccountUnlock,
			Subject: email,
			Detail:  map[string]any{"via": "adminreset"},
		})
		return nil
	}

	if password == "" {
		password, err = prompt()
		if err != nil {
			return err
		}
	}
	if reason, ok := auth.ValidatePassword(password); !ok {
		return errors.New(reason)
	}

	hash, err := auth.HashPassword(password, auth.DefaultHashParams)
	if err != nil {
		return fmt.Errorf("hash the password: %w", err)
	}

	if err := db.Queries().SetUserPassword(ctx, dbgen.SetUserPasswordParams{
		ID: user.ID, PasswordHash: &hash,
	}); err != nil {
		return fmt.Errorf("write the password: %w", err)
	}

	// Every existing session is revoked, because a password reset that leaves old
	// sessions alive has not actually locked anybody out.
	if _, err := db.Queries().DeleteSessionsForUser(ctx, &user.ID); err != nil {
		return fmt.Errorf("revoke existing sessions: %w", err)
	}

	recorder.Record(ctx, audit.Entry{
		Action:  audit.ActionPasswordChanged,
		Subject: email,
		Detail:  map[string]any{"via": "adminreset", "sessionsRevoked": true},
	})

	fmt.Printf("reset the password for %s and revoked their sessions\n", email)
	return nil
}

// prompt reads a password without echoing it, twice, so a typo does not become the
// new password on an installation nobody can log into.
func prompt() (string, error) {
	if !term.IsTerminal(syscall.Stdin) {
		return "", fmt.Errorf("no terminal to prompt on: pass -password")
	}

	fmt.Print("new password: ")
	first, err := term.ReadPassword(syscall.Stdin)
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read the password: %w", err)
	}

	fmt.Print("again: ")
	second, err := term.ReadPassword(syscall.Stdin)
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read the password: %w", err)
	}

	if string(first) != string(second) {
		return "", fmt.Errorf("the two passwords did not match")
	}
	return string(first), nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "adminreset: "+format+"\n", args...)
	os.Exit(2)
}
