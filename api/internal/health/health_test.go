package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadinessAllOK(t *testing.T) {
	service := New("1.2.3",
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
		Probe{Name: "object_store", Detail: "local-disk", Check: func(context.Context) error { return nil }},
	)

	ready, checks := service.Readiness(context.Background())
	require.True(t, ready)
	require.Len(t, checks, 2)
	require.Equal(t, StatusOK, checks[0].Status)
	require.Equal(t, "local-disk", checks[1].Detail)
	require.Equal(t, "1.2.3", service.Version())
}

// A required dependency failing takes readiness down, and the response names which
// one, so a failed deploy is diagnosable without reading logs.
func TestReadinessFailsWhenARequiredProbeFails(t *testing.T) {
	service := New("test",
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
		Probe{Name: "queue", Check: func(context.Context) error {
			return errors.New("dial tcp 127.0.0.1:56379: connect: connection refused")
		}},
	)

	ready, checks := service.Readiness(context.Background())
	require.False(t, ready)
	require.Equal(t, StatusOK, checks[0].Status)
	require.Equal(t, StatusFailing, checks[1].Status)
	require.Contains(t, checks[1].Detail, "connection refused")
}

// An unconfigured optional integration is never a failure. This is what keeps a
// zero-integration install green (requirements.md 5.4).
func TestReadinessStaysGreenWhenAnOptionalProbeFails(t *testing.T) {
	service := New("test",
		Probe{Name: "database", Check: func(context.Context) error { return nil }},
		Probe{Name: "jira", Optional: true, Check: func(context.Context) error {
			return errors.New("no credentials")
		}},
	)

	ready, checks := service.Readiness(context.Background())
	require.True(t, ready)
	require.Equal(t, StatusNotConfigured, checks[1].Status)
	require.Equal(t, "not configured", checks[1].Detail)
	require.NotContains(t, checks[1].Detail, "credentials")
}

func TestProbeWithoutACheckReportsNotConfigured(t *testing.T) {
	service := New("test", Probe{Name: "slack"})

	ready, checks := service.Readiness(context.Background())
	require.True(t, ready)
	require.Equal(t, StatusNotConfigured, checks[0].Status)
}

// A probe that hangs must fail rather than hold the response open: an
// orchestrator waiting is worse than one told to route away.
func TestReadinessTimesOutAHangingProbe(t *testing.T) {
	service := New("test", Probe{Name: "database", Check: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})
	service.timeout = 50 * time.Millisecond

	started := time.Now()
	ready, checks := service.Readiness(context.Background())

	require.False(t, ready)
	require.Equal(t, StatusFailing, checks[0].Status)
	require.Less(t, time.Since(started), time.Second)
}

// Probes run concurrently, so the timeout is the slowest probe rather than the sum.
func TestProbesRunConcurrently(t *testing.T) {
	slow := func(context.Context) error {
		time.Sleep(80 * time.Millisecond)
		return nil
	}
	service := New("test",
		Probe{Name: "a", Check: slow},
		Probe{Name: "b", Check: slow},
		Probe{Name: "c", Check: slow},
	)

	started := time.Now()
	ready, _ := service.Readiness(context.Background())

	require.True(t, ready)
	require.Less(t, time.Since(started), 200*time.Millisecond)
}

// Order matters for the response body: it should match the order probes were
// registered in main, not the order they happened to finish in.
func TestChecksKeepRegistrationOrder(t *testing.T) {
	fast := func(context.Context) error { return nil }
	slow := func(context.Context) error {
		time.Sleep(40 * time.Millisecond)
		return nil
	}

	service := New("test",
		Probe{Name: "slow", Check: slow},
		Probe{Name: "fast", Check: fast},
	)

	_, checks := service.Readiness(context.Background())
	require.Equal(t, "slow", checks[0].Name)
	require.Equal(t, "fast", checks[1].Name)
}
