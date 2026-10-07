// Package config is the ONLY place os.Getenv is called (backend-standards.md 6).
//
// Only the bootstrap variables from requirements.md 5.1 exist. Everything else
// comes from the settings service, so it can be edited from the UI without a
// deploy. Adding a new environment variable is a design error until proven
// otherwise: the question to answer is "why can this not be a UI setting?", and
// the only acceptable answers are that it is needed before the database is
// reachable, or that it decrypts the settings themselves.
//
// policy_test.go enforces the rule mechanically.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// EncryptionKeyBytes is the required length of APP_ENCRYPTION_KEY once decoded.
// AES-256-GCM takes a 32-byte key.
const EncryptionKeyBytes = 32

// Env is the deployment environment. It gates development-only affordances such
// as the unsafe runc container runtime, never business behaviour.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

// IsProduction reports whether hardened defaults must apply.
func (e Env) IsProduction() bool { return e == EnvProduction }

// Config holds the six bootstrap values. It is immutable once loaded: there is
// no global mutable state and no reload path, because a change to any of these
// is a restart by definition.
type Config struct {
	// DatabaseURL is required because settings live in the database and cannot
	// be read without it.
	DatabaseURL string

	// RedisURL is required because the queue must connect before any job,
	// including settings-dependent ones, can run.
	RedisURL string

	// EncryptionKey decrypts the secrets in the settings table, so it cannot be
	// stored in the thing it decrypts. Losing it makes every stored secret
	// unrecoverable and they must be re-entered.
	EncryptionKey []byte

	// AppURL builds absolute links in emails sent before an admin has visited
	// settings.
	AppURL *url.URL

	Port int
	Env  Env
}

// LogValue omits EncryptionKey. Config is exactly the kind of struct that ends
// up in a startup log line, and NFR-10 does not tolerate one exception.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("app_env", string(c.Env)),
		slog.Int("port", c.Port),
		slog.String("app_url", c.AppURL.String()),
		slog.Bool("database_url_set", c.DatabaseURL != ""),
		slog.Bool("redis_url_set", c.RedisURL != ""),
		slog.Bool("encryption_key_set", len(c.EncryptionKey) == EncryptionKeyBytes),
	)
}

// Load reads and validates the bootstrap environment.
//
// Every problem is reported at once rather than one per restart, and every
// message names the fix. A boot failure is the cheapest place to learn about a
// misconfiguration.
func Load() (Config, error) {
	return load(os.LookupEnv)
}

// lookup matches the signature of os.LookupEnv so tests can supply an
// environment without mutating the process.
type lookup func(string) (string, bool)

func load(env lookup) (Config, error) {
	var (
		cfg  Config
		errs []error
	)

	cfg.DatabaseURL = strings.TrimSpace(get(env, "DATABASE_URL"))
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New(
			"DATABASE_URL is required: settings live in the database and cannot be read without it"))
	}

	cfg.RedisURL = strings.TrimSpace(get(env, "REDIS_URL"))
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New(
			"REDIS_URL is required: the queue must connect before any job can run"))
	}

	if key, err := decodeEncryptionKey(get(env, "APP_ENCRYPTION_KEY")); err != nil {
		errs = append(errs, err)
	} else {
		cfg.EncryptionKey = key
	}

	if appURL, err := parseAppURL(get(env, "APP_URL")); err != nil {
		errs = append(errs, err)
	} else {
		cfg.AppURL = appURL
	}

	rawPort := strings.TrimSpace(get(env, "PORT"))
	if rawPort == "" {
		rawPort = "8080"
	}
	port, err := strconv.Atoi(rawPort)
	switch {
	case err != nil || port < 1 || port > 65535:
		errs = append(errs, fmt.Errorf("PORT must be a number between 1 and 65535, got %q", rawPort))
	default:
		cfg.Port = port
	}

	rawEnv := Env(strings.TrimSpace(get(env, "APP_ENV")))
	if rawEnv == "" {
		rawEnv = EnvDevelopment
	}
	switch rawEnv {
	case EnvDevelopment, EnvStaging, EnvProduction:
		cfg.Env = rawEnv
	default:
		errs = append(errs, fmt.Errorf(
			"APP_ENV must be one of development, staging, production, got %q", rawEnv))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid bootstrap environment (see .env.example):\n%w",
			errors.Join(errs...))
	}
	return cfg, nil
}

func get(env lookup, key string) string {
	v, found := env(key)
	if !found {
		return ""
	}
	return v
}

// decodeEncryptionKey validates APP_ENCRYPTION_KEY. Every failure message carries
// the command that produces a correct value, because the alternative is a
// developer guessing at an encoding.
func decodeEncryptionKey(raw string) ([]byte, error) {
	const generate = "generate one with `openssl rand -base64 32`"

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY is required: %s", generate)
	}

	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("APP_ENCRYPTION_KEY must be base64: %s", generate)
	}
	if len(key) != EncryptionKeyBytes {
		return nil, fmt.Errorf(
			"APP_ENCRYPTION_KEY must decode to %d bytes, got %d: %s",
			EncryptionKeyBytes, len(key), generate)
	}
	return key, nil
}

// parseAppURL validates APP_URL. It has to be absolute because it builds links in
// notifications sent before an admin has visited settings.
func parseAppURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New(
			"APP_URL is required: it builds absolute links in notifications, for example http://localhost:3000")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("APP_URL is not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("APP_URL must be an http or https URL, got scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("APP_URL must include a host, for example http://localhost:3000")
	}
	return parsed, nil
}
