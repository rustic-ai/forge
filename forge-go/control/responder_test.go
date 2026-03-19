package control

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponder_SendResponse(t *testing.T) {
	// 1. Setup Miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	reqID := "req-999"
	transport := NewRedisControlTransport(rdb)
	responder := NewControlQueueResponder(transport)

	resp := &protocol.SpawnResponse{
		RequestID: reqID,
		Success:   true,
		NodeID:    "node-alpha",
	}

	// 3. Send Response
	err = responder.SendResponse(ctx, reqID, resp)
	require.NoError(t, err)

	// 4. Verify it was placed in the correct Redis list
	// Expected key pattern based on implementation plan
	expectedKey := fmt.Sprintf("forge:control:response:%s", reqID)

	b, err := rdb.BRPop(ctx, 2*time.Second, expectedKey).Result()
	require.NoError(t, err)
	require.Len(t, b, 2)

	// 5. Verify the payload
	var out protocol.SpawnResponse
	err = json.Unmarshal([]byte(b[1]), &out)
	require.NoError(t, err)

	assert.True(t, out.Success)
	assert.Equal(t, "node-alpha", out.NodeID)
}

func TestResponder_SendError(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	transport := NewRedisControlTransport(rdb)
	responder := NewControlQueueResponder(transport)
	err = responder.SendError(ctx, "err-req", "test failure")
	require.NoError(t, err)

	expectedKey := fmt.Sprintf("forge:control:response:%s", "err-req")
	b, err := rdb.LPop(ctx, expectedKey).Result()
	require.NoError(t, err)

	var out protocol.ErrorResponse
	err = json.Unmarshal([]byte(b), &out)
	require.NoError(t, err)

	assert.False(t, out.Success)
	assert.Equal(t, "test failure", out.Error)
}
