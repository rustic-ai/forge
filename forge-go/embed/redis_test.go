package embed

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmbeddedRedis(t *testing.T) {
	er, err := StartEmbeddedRedis()
	require.NoError(t, err)
	require.NotNil(t, er)
	defer er.Close()

	// Ensure Host and Port are populated
	require.NotEmpty(t, er.Host())
	require.NotEmpty(t, er.Port())
	require.NotEmpty(t, er.Addr())

	// Test the bound redis client works
	client := er.Client()
	require.NotNil(t, client)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pong, err := client.Ping(ctx).Result()
	require.NoError(t, err)
	require.Equal(t, "PONG", pong)

	// Close safely
	er.Close()

	// Subsequent pings should fail since the underlying miniredis is closed
	_, err = client.Ping(ctx).Result()
	require.Error(t, err)
}

func TestEmbeddedRedisAt_ExplicitAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	er, err := StartEmbeddedRedisAt(addr)
	require.NoError(t, err)
	require.NotNil(t, er)
	defer er.Close()

	require.Equal(t, addr, er.Addr())
}

func TestEmbeddedRedisAt_FailsWhenAddressOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	_, err = StartEmbeddedRedisAt(ln.Addr().String())
	require.Error(t, err)
}
