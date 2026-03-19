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

func TestSubscriptionDelivery(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer func() { _ = rdb.Close() }()

	client := messaging.NewClient(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := "test_ns"
	topic := "live_alerts"
	nsTopic := ns + ":" + topic

	// 1. Subscribe
	sub, err := client.Subscribe(ctx, ns, topic)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	// 2. Publish a message
	gen, _ := protocol.NewGemstoneGenerator(1)
	id, _ := gen.Generate(protocol.PriorityNormal)

	serverName := "server"
	sentMsg := &protocol.Message{
		ID:      id.ToInt(),
		Payload: json.RawMessage(`"broadcast data"`),
		Sender:  protocol.AgentTag{Name: &serverName},
	}

	bytes, _ := json.Marshal(sentMsg)
	err = rdb.Publish(ctx, nsTopic, string(bytes)).Err()
	require.NoError(t, err)

	// 3. Receive the message from the channel
	select {
	case receivedSubMsg := <-sub.Channel():
		receivedMsg := receivedSubMsg.Message
		assert.Equal(t, nsTopic, receivedSubMsg.Topic)
		assert.Equal(t, sentMsg.ID, receivedMsg.ID)
		assert.Equal(t, sentMsg.Payload, receivedMsg.Payload)
		assert.Equal(t, sentMsg.Sender, receivedMsg.Sender)
	case err := <-sub.ErrChannel():
		t.Fatalf("Unexpected error: %v", err)
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for pub/sub message")
	}
}

func TestSubscriptionGracefulShutdown(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer func() { _ = rdb.Close() }()

	client := messaging.NewClient(rdb)
	ctx := context.Background()

	sub, err := client.Subscribe(ctx, "ns", "temp_topic")
	require.NoError(t, err)

	// Close the subscription
	err = sub.Close()
	require.NoError(t, err)

	// Ensure the goroutine finishes and channels shouldn't block
	select {
	case msg := <-sub.Channel():
		t.Fatalf("Expected no messages but got: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected to drain out
	}
}
