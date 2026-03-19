package messaging_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestPublishAndRetrieveMessages(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer func() { _ = rdb.Close() }()

	client := messaging.NewClient(rdb)
	ctx := context.Background()

	gen, err := protocol.NewGemstoneGenerator(1)
	require.NoError(t, err)

	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	id2, err := gen.Generate(protocol.PriorityUrgent) // Urgent has higher logical priority (-1 numerically compared to Normal)
	require.NoError(t, err)

	user1Name := "user_1"
	systemName := "system"
	msg1 := &protocol.Message{
		ID:      id1.ToInt(),
		Payload: json.RawMessage(`"hello world"`),
		Sender:  protocol.AgentTag{Name: &user1Name},
	}

	msg2 := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"urgent alert"`),
		Sender:  protocol.AgentTag{Name: &systemName},
	}

	topic := "my_topic"
	ns := "test_ns"

	err = client.PublishMessage(ctx, ns, topic, msg1)
	require.NoError(t, err)

	err = client.PublishMessage(ctx, ns, topic, msg2)
	require.NoError(t, err)

	// Since they both went into the ZSET, they should be sorted properly
	// Remember id2 is PriorityUrgent (0) and id1 is PriorityNormal (4).
	// Because of Gemstone sort order, id2 < id1 numerically when generated at approx same time.
	// `client.GetMessagesForTopic` reads them and calls parseAndSortMessages which uses Compare.
	messages, err := client.GetMessagesForTopic(ctx, ns, topic)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	// In Gemstone priority, Urgent (0) encodes lower int64 value than Normal (4). So Urgent comes first in Ascending sort.
	assert.Equal(t, msg2.ID, messages[0].ID)
	assert.Equal(t, msg1.ID, messages[1].ID)

	// Test GetMessagesByID
	intID1 := id1.ToInt()
	intID2 := id2.ToInt()
	byID, err := client.GetMessagesByID(ctx, ns, []uint64{intID1, intID2})
	require.NoError(t, err)
	require.Len(t, byID, 2)

	// Fast lookup doesn't strictly guarantee preserved order based on sorting, it preserves input slice order
	assert.Equal(t, msg1.ID, byID[0].ID)
	assert.Equal(t, msg2.ID, byID[1].ID)
}

func TestGetMessagesSince(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer func() { _ = rdb.Close() }()

	client := messaging.NewClient(rdb)
	ctx := context.Background()

	gen, err := protocol.NewGemstoneGenerator(1)
	require.NoError(t, err)

	topic := "paginated_topic"

	// Create 3 messages spanning some time
	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond) // artificially increment timestamp to ensure boundary separation
	id2, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	id3, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)

	for _, id := range []protocol.GemstoneID{id1, id2, id3} {
		require.NoError(t, client.PublishMessage(ctx, "ns", topic, &protocol.Message{
			ID: id.ToInt(),
		}))
	}

	// Fetch since id1
	msgs, err := client.GetMessagesSince(ctx, "ns", topic, id1.ToInt())
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, id2.ToInt(), msgs[0].ID)
	assert.Equal(t, id3.ToInt(), msgs[1].ID)

	// Fetch since id2
	msgs2, err := client.GetMessagesSince(ctx, "ns", topic, id2.ToInt())
	require.NoError(t, err)
	require.Len(t, msgs2, 1)
	assert.Equal(t, id3.ToInt(), msgs2[0].ID)
}
