// Command rotatekey re-encrypts every stored secret under a new encryption key.
//
// Run it offline, between stopping the old processes and starting the new ones. It
// opens each secret with the current APP_ENCRYPTION_KEY and reseals it with the new
// one, then prints what to put in the environment.
//
// Losing APP_ENCRYPTION_KEY makes every stored secret unrecoverable and they have to
// be re-entered, so rotation is the supported alternative to that: it works while the
// old key still exists.
//
//	APP_ENCRYPTION_KEY=<current> go run ./cmd/rotatekey -new-key "$(openssl rand -base64 32)"
package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hyscaler/qavia/api/internal/platform/config"
	"github.com/hyscaler/qavia/api/internal/settings"
	"github.com/hyscaler/qavia/api/internal/store"
	"github.com/hyscaler/qavia/api/internal/store/dbgen"
)

func main() {
	newKeyFlag := flag.String("new-key", "",
		"the new key, base64 encoded 32 bytes: openssl rand -base64 32")
	dryRun := flag.Bool("dry-run", false,
		"report what would be rotated without writing anything")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *newKeyFlag, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "rotatekey: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, newKeyEncoded string, dryRun bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	oldCipher, err := settings.NewCipher(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	var newCipher *settings.Cipher
	if !dryRun {
		if newKeyEncoded == "" {
			return fmt.Errorf("-new-key is required: generate one with `openssl rand -base64 32`")
		}
		newKey, decodeErr := base64.StdEncoding.DecodeString(newKeyEncoded)
		if decodeErr != nil {
			return fmt.Errorf("-new-key must be base64: %w", decodeErr)
		}
		if len(newKey) != config.EncryptionKeyBytes {
			return fmt.Errorf("-new-key must decode to %d bytes, got %d",
				config.EncryptionKeyBytes, len(newKey))
		}
		if string(newKey) == string(cfg.EncryptionKey) {
			return fmt.Errorf("-new-key is the same as the current key; nothing would change")
		}
		if newCipher, err = settings.NewCipher(newKey); err != nil {
			return err
		}
	}

	db, err := store.Open(ctx, store.Options{DatabaseURL: cfg.DatabaseURL, MaxConns: 2})
	if err != nil {
		return err
	}
	defer db.Close()

	registry := settings.Default()
	rotated, skipped := 0, 0

	// Every secret is read, opened, and resealed in one transaction. A partial
	// rotation would leave some rows readable only by the old key and some only by
	// the new, and no single key could then start the application.
	err = db.InTx(ctx, func(q *dbgen.Queries) error {
		for _, scope := range []dbgen.SettingsScope{
			dbgen.SettingsScopeGlobal, dbgen.SettingsScopeProject, dbgen.SettingsScopeUser,
		} {
			rows, listErr := q.ListSettingsByScope(ctx, dbgen.ListSettingsByScopeParams{Scope: scope})
			if listErr != nil {
				return fmt.Errorf("list %s settings: %w", scope, listErr)
			}

			for _, row := range rows {
				if !row.IsSecret {
					continue
				}
				if _, declared := registry.Lookup(row.Key); !declared {
					// A stored value for a key nobody declares any more cannot be
					// rotated meaningfully, and dropping it silently would destroy
					// data. Report it and leave it.
					fmt.Printf("skipped %s (%s): no longer declared\n", row.Key, scope)
					skipped++
					continue
				}

				secret, decodeErr := settings.DecodeSecret(row.Value)
				if decodeErr != nil {
					return fmt.Errorf("secret %q at %s scope is unreadable: %w", row.Key, scope, decodeErr)
				}

				plaintext, decryptErr := oldCipher.Decrypt(row.Key, secret)
				if decryptErr != nil {
					return fmt.Errorf("open %q at %s scope: %w", row.Key, scope, decryptErr)
				}

				if dryRun {
					fmt.Printf("would rotate %s (%s), stored %s\n",
						row.Key, scope, secret.UpdatedAt.Format(time.RFC3339))
					rotated++
					continue
				}

				resealed, sealErr := newCipher.Reseal(row.Key, plaintext)
				if sealErr != nil {
					return sealErr
				}
				encoded, encodeErr := settings.EncodeSecret(resealed)
				if encodeErr != nil {
					return encodeErr
				}

				if writeErr := writeSecret(ctx, q, row, encoded); writeErr != nil {
					return writeErr
				}

				fmt.Printf("rotated %s (%s)\n", row.Key, scope)
				rotated++
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	switch {
	case dryRun:
		fmt.Printf("\n%d secret(s) would be rotated, %d skipped. Nothing was written.\n", rotated, skipped)
	default:
		fmt.Printf("\n%d secret(s) rotated, %d skipped.\n", rotated, skipped)
		fmt.Printf("Set APP_ENCRYPTION_KEY to the new value before starting the API or the worker.\n")
		fmt.Printf("Starting with the old key will fail to read every secret above.\n")
	}
	return nil
}

// writeSecret replaces one row's value, using the upsert that matches its scope.
func writeSecret(ctx context.Context, q *dbgen.Queries, row dbgen.Setting, value []byte) error {
	if row.Scope == dbgen.SettingsScopeGlobal {
		if _, err := q.UpsertGlobalSetting(ctx, dbgen.UpsertGlobalSettingParams{
			Key: row.Key, Value: value, IsSecret: true, UpdatedBy: row.UpdatedBy,
		}); err != nil {
			return fmt.Errorf("reseal %q: %w", row.Key, err)
		}
		return nil
	}

	if _, err := q.UpsertSetting(ctx, dbgen.UpsertSettingParams{
		Scope: row.Scope, ScopeID: row.ScopeID, Key: row.Key,
		Value: value, IsSecret: true, UpdatedBy: row.UpdatedBy,
	}); err != nil {
		return fmt.Errorf("reseal %q: %w", row.Key, err)
	}
	return nil
}
