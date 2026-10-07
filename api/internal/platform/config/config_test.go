package config

import (
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":       "postgres://qavia:qavia@localhost:55432/qavia?sslmode=disable",
		"REDIS_URL":          "redis://localhost:56379/0",
		"APP_ENCRYPTION_KEY": validKey,
		"APP_URL":            "http://localhost:3000",
		"PORT":               "8080",
		"APP_ENV":            "development",
	}
}

func lookupFrom(m map[string]string) lookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := load(lookupFrom(validEnv()))
	require.NoError(t, err)
	require.Len(t, cfg.EncryptionKey, EncryptionKeyBytes)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, EnvDevelopment, cfg.Env)
	require.Equal(t, "localhost:3000", cfg.AppURL.Host)
	require.False(t, cfg.Env.IsProduction())
}

func TestLoadDefaults(t *testing.T) {
	env := validEnv()
	delete(env, "PORT")
	delete(env, "APP_ENV")

	cfg, err := load(lookupFrom(env))
	require.NoError(t, err)
	require.Equal(t, 8080, cfg.Port)
	require.Equal(t, EnvDevelopment, cfg.Env)
}

func TestLoadRejectsBadValues(t *testing.T) {
	tests := map[string]struct {
		mutate    func(map[string]string)
		wantInErr string
	}{
		"missing database url": {
			mutate:    func(m map[string]string) { delete(m, "DATABASE_URL") },
			wantInErr: "DATABASE_URL is required",
		},
		"missing redis url": {
			mutate:    func(m map[string]string) { delete(m, "REDIS_URL") },
			wantInErr: "REDIS_URL is required",
		},
		"missing key": {
			mutate:    func(m map[string]string) { delete(m, "APP_ENCRYPTION_KEY") },
			wantInErr: "openssl rand -base64 32",
		},
		"key not base64": {
			mutate:    func(m map[string]string) { m["APP_ENCRYPTION_KEY"] = "not!base64!" },
			wantInErr: "must be base64",
		},
		"key wrong length": {
			mutate: func(m map[string]string) {
				m["APP_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString([]byte("too short"))
			},
			wantInErr: "must decode to 32 bytes, got 9",
		},
		"missing app url": {
			mutate:    func(m map[string]string) { delete(m, "APP_URL") },
			wantInErr: "APP_URL is required",
		},
		"app url wrong scheme": {
			mutate:    func(m map[string]string) { m["APP_URL"] = "ftp://example.test" },
			wantInErr: "must be an http or https URL",
		},
		"app url no host": {
			mutate:    func(m map[string]string) { m["APP_URL"] = "http://" },
			wantInErr: "must include a host",
		},
		"port not a number": {
			mutate:    func(m map[string]string) { m["PORT"] = "eighty" },
			wantInErr: "PORT must be a number",
		},
		"port out of range": {
			mutate:    func(m map[string]string) { m["PORT"] = "99999" },
			wantInErr: "PORT must be a number",
		},
		"unknown env": {
			mutate:    func(m map[string]string) { m["APP_ENV"] = "prod" },
			wantInErr: "APP_ENV must be one of",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env := validEnv()
			tc.mutate(env)

			_, err := load(lookupFrom(env))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}

// Every problem is reported at once, not one per restart.
func TestLoadAggregatesEveryProblem(t *testing.T) {
	_, err := load(lookupFrom(map[string]string{}))
	require.Error(t, err)

	for _, want := range []string{"DATABASE_URL", "REDIS_URL", "APP_ENCRYPTION_KEY", "APP_URL"} {
		require.Contains(t, err.Error(), want)
	}
}

// Config is exactly the kind of struct that ends up in a startup log line.
func TestLogValueOmitsEncryptionKey(t *testing.T) {
	cfg, err := load(lookupFrom(validEnv()))
	require.NoError(t, err)

	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("boot", "config", cfg)

	out := buf.String()
	require.Contains(t, out, `"encryption_key_set":true`)
	require.NotContains(t, out, validKey)
	require.NotContains(t, out, "0123456789abcdef")
}
