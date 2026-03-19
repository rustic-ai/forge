package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListener_HandleSpawnRequest(t *testing.T) {
	// 1. Setup Miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)
	listener := NewControlQueueListener(transport)

	handleCh := make(chan *protocol.SpawnRequest, 1)
	listener.OnSpawn = func(ctx context.Context, req *protocol.SpawnRequest) {
		handleCh <- req
	}

	go listener.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Give the BRPOP loop time to start

	// 3. Inject a protocol.SpawnRequest into the control queue
	req := &protocol.SpawnRequest{
		RequestID: "spawn-123",
		GuildID:   "guild-abc",
		AgentSpec: protocol.AgentSpec{
			ID: "test-agent",
		},
	}

	// In the python implementation, requests are LPUSHed to "forge:control:requests"
	// However, we need to embed a command type header. The standard message format:
	// {"command": "spawn", "payload": {...}}
	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}
	wb, _ := json.Marshal(wrapper)

	rdb.LPush(ctx, ControlQueueRequestKey, wb)

	// 4. Verify the listener cleanly picked it up, deserialized it, and called the handler
	select {
	case result := <-handleCh:
		assert.Equal(t, "spawn-123", result.RequestID)
		assert.Equal(t, "guild-abc", result.GuildID)
		assert.Equal(t, "test-agent", result.AgentSpec.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("Listener timed out waiting for protocol.SpawnRequest")
	}

	listener.Stop()
}

func TestListener_CustomQueueKey(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	const nodeQueue = "forge:control:node:test-node-1"
	transport := NewRedisControlTransport(rdb)
	listener := NewControlQueueListenerWithQueue(transport, nodeQueue)

	handleCh := make(chan *protocol.SpawnRequest, 1)
	listener.OnSpawn = func(ctx context.Context, req *protocol.SpawnRequest) {
		handleCh <- req
	}

	go listener.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	req := &protocol.SpawnRequest{
		RequestID: "spawn-custom-queue",
		GuildID:   "guild-custom",
		AgentSpec: protocol.AgentSpec{ID: "agent-custom"},
	}
	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}
	wb, _ := json.Marshal(wrapper)
	rdb.LPush(ctx, nodeQueue, wb)

	select {
	case result := <-handleCh:
		assert.Equal(t, "spawn-custom-queue", result.RequestID)
		assert.Equal(t, "agent-custom", result.AgentSpec.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("Listener timed out waiting for custom-queue SpawnRequest")
	}

	listener.Stop()
}
