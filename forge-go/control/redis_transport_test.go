package control

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisControlTransport_PushPop(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)

	payload := []byte(`{"command":"test"}`)
	require.NoError(t, transport.Push(ctx, "test:queue", payload))

	got, err := transport.Pop(ctx, "test:queue", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, payload, got)
}

func TestRedisControlTransport_PopTimeout(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)

	got, err := transport.Pop(ctx, "empty:redis:queue", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, got, "Expected nil on timeout")
}

func TestRedisControlTransport_QueueDepth(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)

	depth, err := transport.QueueDepth(ctx, "depth:redis:queue")
	require.NoError(t, err)
	assert.Equal(t, int64(0), depth)

	require.NoError(t, transport.Push(ctx, "depth:redis:queue", []byte("a")))
	require.NoError(t, transport.Push(ctx, "depth:redis:queue", []byte("b")))

	depth, err = transport.QueueDepth(ctx, "depth:redis:queue")
	require.NoError(t, err)
	assert.Equal(t, int64(2), depth)
}

func TestRedisControlTransport_PushWaitResponse(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)

	payload := []byte(`{"success":true}`)
	require.NoError(t, transport.PushResponse(ctx, "req-redis-1", payload, 30*time.Second))

	got, err := transport.WaitResponse(ctx, "req-redis-1", 2*time.Second)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, payload, got)
}

func TestRedisControlTransport_WaitResponseTimeout(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)

	got, err := transport.WaitResponse(ctx, "no-such-req-redis", 100*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, got, "Expected nil on timeout")
}
