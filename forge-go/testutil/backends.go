package testutil

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/testutil/natstest"
)

// SetupRedisBackend creates an in-process miniredis and returns a RedisBackend
// with cleanup registered on t.
func SetupRedisBackend(t *testing.T) messaging.Backend {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	return messaging.NewRedisBackend(rdb)
}

// SetupNATSBackend creates an in-process NATS server and returns a NATSBackend
// with cleanup registered on t.
func SetupNATSBackend(t *testing.T) messaging.Backend {
	t.Helper()
	return natstest.NewBackend(t)
}

// SetupMessagingBackend dispatches to the appropriate backend setup by name.
// Supported names: "redis", "nats".
func SetupMessagingBackend(t *testing.T, name string) messaging.Backend {
	t.Helper()
	switch name {
	case "nats":
		return SetupNATSBackend(t)
	default:
		return SetupRedisBackend(t)
	}
}
