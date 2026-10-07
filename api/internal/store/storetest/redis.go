package storetest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	redisOnce      sync.Once
	redisContainer *shared
)

// Redis returns the URL of a real Redis, flushed empty.
//
// Like Postgres, one container serves the whole test binary and is emptied per
// test. The queue is not mocked for the same reason the database is not: Asynq's
// uniqueness, retry, and scheduling behaviour lives in Redis, and a fake would
// only agree with the code under test.
func Redis(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping container-backed integration test in -short mode")
	}

	redisOnce.Do(func() { redisContainer = startRedis() })
	require.NoError(t, redisContainer.err, "start redis container")

	opts, err := redis.ParseURL(redisContainer.url)
	require.NoError(t, err)
	client := redis.NewClient(opts)
	defer func() { require.NoError(t, client.Close()) }()
	require.NoError(t, client.FlushAll(context.Background()).Err())

	return redisContainer.url
}

func startRedis() *shared {
	ctx := context.Background()

	// Tag-pinned for the same reason as Postgres: it never faces untrusted code.
	container, err := testcontainers.Run(ctx, "redis:7-alpine",
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("6379/tcp").WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		return &shared{err: err}
	}

	endpoint, err := container.PortEndpoint(ctx, "6379/tcp", "")
	if err != nil {
		return &shared{err: err}
	}
	return &shared{url: fmt.Sprintf("redis://%s/0", endpoint)}
}
