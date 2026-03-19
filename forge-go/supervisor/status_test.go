package supervisor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusKeyLifecycle(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	guildID := "guild-abc"
	agentID := "agent-xyz"
	nodeID := "node-1"
	pid := 1024

	store := NewRedisAgentStatusStore(rdb)

	// Write running status
	err = store.WriteStatus(ctx, guildID, agentID, &AgentStatusJSON{
		State:     "running",
		NodeID:    nodeID,
		PID:       pid,
		Timestamp: time.Now(),
	}, 30*time.Second)
	require.NoError(t, err)

	// Verify via GetStatus
	status, err := store.GetStatus(ctx, guildID, agentID)
	require.NoError(t, err)
	require.NotNil(t, status)
	assert.Equal(t, "running", status.State)
	assert.Equal(t, nodeID, status.NodeID)
	assert.Equal(t, pid, status.PID)
	assert.WithinDuration(t, time.Now(), status.Timestamp, 2*time.Second)

	// Verify TTL via redis directly
	key := fmt.Sprintf("forge:agent:status:%s:%s", guildID, agentID)
	ttl, err := rdb.TTL(ctx, key).Result()
	require.NoError(t, err)
	assert.True(t, ttl > 28*time.Second && ttl <= 30*time.Second, "Expected ~30s TTL, got %v", ttl)

	// Fast-forward and refresh
	mr.FastForward(15 * time.Second)
	err = store.RefreshStatus(ctx, guildID, agentID, 30*time.Second)
	require.NoError(t, err)

	ttl, _ = rdb.TTL(ctx, key).Result()
	assert.True(t, ttl > 28*time.Second && ttl <= 30*time.Second, "Expected TTL to reset to 30s, got %v", ttl)

	// Restarting
	err = store.WriteStatus(ctx, guildID, agentID, &AgentStatusJSON{State: "restarting", Timestamp: time.Now()}, 30*time.Second)
	require.NoError(t, err)
	status, err = store.GetStatus(ctx, guildID, agentID)
	require.NoError(t, err)
	assert.Equal(t, "restarting", status.State)

	// Failed (300s TTL)
	err = store.WriteStatus(ctx, guildID, agentID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
	require.NoError(t, err)
	status, err = store.GetStatus(ctx, guildID, agentID)
	require.NoError(t, err)
	assert.Equal(t, "failed", status.State)

	ttl, _ = rdb.TTL(ctx, key).Result()
	assert.True(t, ttl > 298*time.Second && ttl <= 300*time.Second, "Expected Failed TTL to be 300s, got %v", ttl)

	// Delete
	err = store.DeleteStatus(ctx, guildID, agentID)
	require.NoError(t, err)

	status, err = store.GetStatus(ctx, guildID, agentID)
	require.NoError(t, err)
	assert.Nil(t, status, "Expected nil status after deletion")
}
