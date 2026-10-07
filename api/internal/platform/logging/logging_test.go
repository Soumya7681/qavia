package logging

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	secretValue = "sk-ant-DO-NOT-LOG-ME-0123456789"
	tokenValue  = "ghp_DO-NOT-LOG-ME-abcdefghij"
	passValue   = "correct-horse-battery-staple"
)

type level4 struct {
	APIKey string
	Region string
}

type level3 struct {
	Inner level4
	Label string
}

type level2 struct {
	Items  []level3
	ByName map[string]level3
	Raw    []byte
}

type level1 struct {
	Name        string
	Nested      *level2
	Token       string
	Credentials map[string]string
	CreatedAt   time.Time
}

func logLine(t *testing.T, args ...any) string {
	t.Helper()

	var buf bytes.Buffer
	logger := New(Options{Writer: &buf, Level: slog.LevelDebug})
	logger.Info("test", args...)
	return buf.String()
}

// The done-when for BE-0.2: a struct containing an API key at every nesting
// depth, and no assertion finds the plaintext.
func TestRedactsSecretsAtEveryNestingDepth(t *testing.T) {
	payload := level1{
		Name:  "hyscaler-claude",
		Token: tokenValue,
		Nested: &level2{
			Items: []level3{{
				Label: "first",
				Inner: level4{APIKey: secretValue, Region: "ap-south-1"},
			}},
			ByName: map[string]level3{
				"primary": {Label: "second", Inner: level4{APIKey: secretValue}},
			},
			Raw: []byte(secretValue),
		},
		Credentials: map[string]string{"api_key": secretValue, "endpoint": "https://example.test"},
		CreatedAt:   time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}

	out := logLine(t, "provider", payload)

	require.NotContains(t, out, secretValue)
	require.NotContains(t, out, tokenValue)
	require.Contains(t, out, Redacted)

	// Over-redaction would make logs useless, so confirm the harmless fields
	// survive.
	require.Contains(t, out, "hyscaler-claude")
	require.Contains(t, out, "ap-south-1")
	require.Contains(t, out, "first")
	require.Contains(t, out, "second")
}

func TestRedactsTopLevelAttributeKeys(t *testing.T) {
	out := logLine(t,
		"api_key", secretValue,
		"password", passValue,
		"bearer_token", tokenValue,
		"AUTHORIZATION", "Basic abc",
		"project", "quickdesk",
	)

	require.NotContains(t, out, secretValue)
	require.NotContains(t, out, passValue)
	require.NotContains(t, out, tokenValue)
	require.NotContains(t, out, "Basic abc")
	require.Contains(t, out, "quickdesk")
}

// A sensitive group name redacts everything beneath it, however innocent the
// child key looks.
func TestRedactsWholeSensitiveGroup(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Writer: &buf})
	logger.Info("test",
		slog.Group("credentials",
			slog.String("value", secretValue),
			slog.String("region", "ap-south-1"),
		),
		slog.Group("provider",
			slog.String("name", "local-qwen"),
		),
	)

	out := buf.String()
	require.NotContains(t, out, secretValue)
	require.NotContains(t, out, "ap-south-1", "a sensitive group redacts all of its children")
	require.Contains(t, out, "local-qwen")
}

// Ordinary payloads must be logged exactly as a plain JSON handler would, or the
// redaction pass becomes a formatting surprise.
func TestLeavesOrdinaryStructsUntouched(t *testing.T) {
	type project struct {
		ID       string
		Name     string
		Archived bool
	}

	out := logLine(t, "project", project{ID: "p-1", Name: "quickdesk"})

	require.Contains(t, out, `"ID":"p-1"`)
	require.Contains(t, out, `"Name":"quickdesk"`)
	require.NotContains(t, out, Redacted)
}

func TestRedactsByJSONTagName(t *testing.T) {
	type row struct {
		Blob string `json:"secret_material"`
	}

	out := logLine(t, "row", row{Blob: secretValue})
	require.NotContains(t, out, secretValue)
}

func TestNilAndEmptyValuesDoNotPanic(t *testing.T) {
	require.NotPanics(t, func() {
		var nilPtr *level2
		logLine(t, "nested", nilPtr, "nothing", nil, "empty", level1{})
	})
}

func TestCorrelationAndJobIDComeFromContext(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Writer: &buf})

	ctx := WithJobID(WithCorrelationID(context.Background(), "corr-123"), "job-456")
	logger.InfoContext(ctx, "stage complete")

	out := buf.String()
	require.Contains(t, out, `"correlation_id":"corr-123"`)
	require.Contains(t, out, `"job_id":"job-456"`)
}

func TestContextHelpersIgnoreEmptyValues(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "")
	require.Empty(t, CorrelationID(ctx))
	require.Empty(t, JobID(ctx))
}

// WithAttrs and WithGroup must keep the context handler in the chain, otherwise
// a logger built with slog.With silently stops attaching correlation IDs.
func TestDerivedLoggersKeepContextEnrichment(t *testing.T) {
	var buf bytes.Buffer
	logger := New(Options{Writer: &buf}).With("component", "worker").WithGroup("stage")

	logger.InfoContext(WithCorrelationID(context.Background(), "corr-789"), "running")

	out := buf.String()
	require.Contains(t, out, `"correlation_id":"corr-789"`)
	require.Contains(t, out, `"component":"worker"`)
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	level := new(slog.LevelVar)
	level.Set(slog.LevelWarn)
	logger := New(Options{Writer: &buf, Level: level})

	logger.Info("suppressed")
	require.Empty(t, buf.String())

	// A LevelVar means the log level can become a settings-driven value without
	// rebuilding the logger.
	level.Set(slog.LevelInfo)
	logger.Info("emitted")
	require.Contains(t, buf.String(), "emitted")
}

func TestParseLevel(t *testing.T) {
	require.Equal(t, slog.LevelDebug, ParseLevel("debug"))
	require.Equal(t, slog.LevelWarn, ParseLevel("warn"))
	require.Equal(t, slog.LevelError, ParseLevel("ERROR"))
	require.Equal(t, slog.LevelInfo, ParseLevel("nonsense"))
}

func TestIsSensitiveKey(t *testing.T) {
	for _, name := range []string{
		"api_key", "APIKey", "access_token", "clientSecret",
		"password", "passwd", "credentials", "Authorization", "secret_key",
	} {
		require.True(t, IsSensitiveKey(name), name)
	}
	for _, name := range []string{"name", "region", "endpoint", "project_id", "status"} {
		require.False(t, IsSensitiveKey(name), name)
	}
}
