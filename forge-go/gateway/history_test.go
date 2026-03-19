package gateway_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/testutil/natstest"
)

func runRetrieveHistoryTest(t *testing.T, msgClient messaging.Backend) {
	t.Helper()
	ctx := context.Background()
	gen, _ := protocol.NewGemstoneGenerator(0)

	// Publish a notification to user_notifications:u1
	id1, _ := gen.Generate(protocol.PriorityNormal)
	notifMsg := &protocol.Message{
		ID:      id1.ToInt(),
		Payload: json.RawMessage(`"notif"`),
		Format:  "Test",
	}
	require.NoError(t, msgClient.PublishMessage(ctx, "g1", "user_notifications:u1", notifMsg))

	// Publish a broadcast to user_message_broadcast
	id2, _ := gen.Generate(protocol.PriorityNormal)
	broadcastMsg := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"broadcast"`),
		Format:  "Test",
	}
	require.NoError(t, msgClient.PublishMessage(ctx, "g1", "user_message_broadcast", broadcastMsg))

	// Publish notifMsg to broadcast too — same ID should be deduplicated
	require.NoError(t, msgClient.PublishMessage(ctx, "g1", "user_message_broadcast", notifMsg))

	result, err := gateway.RetrieveHistory(ctx, msgClient, "g1", "u1", 0)
	require.NoError(t, err)

	// 3 publishes but only 2 unique IDs; dedup should yield exactly 2
	assert.Len(t, result, 2)

	var m1, m2 protocol.Message
	require.NoError(t, json.Unmarshal(result[0], &m1))
	require.NoError(t, json.Unmarshal(result[1], &m2))

	// Both IDs present
	ids := map[uint64]bool{m1.ID: true, m2.ID: true}
	assert.True(t, ids[id1.ToInt()], "notif message should be in result")
	assert.True(t, ids[id2.ToInt()], "broadcast message should be in result")

	// Sorted ascending by Gemstone ID (id1 < id2 since generated in order)
	assert.Less(t, m1.ID, m2.ID, "messages should be sorted by Gemstone ID ascending")
}

func TestRetrieveHistory_Redis(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	msgClient := messaging.NewClient(rdb)
	runRetrieveHistoryTest(t, msgClient)
}

func TestRetrieveHistory_NATS(t *testing.T) {
	backend := natstest.NewBackend(t)

	runRetrieveHistoryTest(t, backend)
}
