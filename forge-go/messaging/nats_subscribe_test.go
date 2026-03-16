package messaging_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestNATSSubscriptionDelivery(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer backend.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := "test_ns"
	topic := "live_alerts"
	nsTopic := ns + ":" + topic

	// 1. Subscribe.
	sub, err := backend.Subscribe(ctx, ns, topic)
	require.NoError(t, err)
	defer sub.Close()

	// 2. Publish a message directly via NATS core pub/sub to trigger the subscription callback.
	gen, _ := protocol.NewGemstoneGenerator(1)
	id, _ := gen.Generate(protocol.PriorityNormal)

	serverName := "server"
	sentMsg := &protocol.Message{
		ID:     id.ToInt(),
		Sender: protocol.AgentTag{Name: &serverName},
	}
	msgBytes, _ := json.Marshal(sentMsg)
	require.NoError(t, nc.Publish(nsTopic, msgBytes))

	// 3. Receive the message from the subscription channel.
	select {
	case received := <-sub.Channel():
		assert.Equal(t, nsTopic, received.Topic)
		assert.Equal(t, sentMsg.ID, received.Message.ID)
		assert.Equal(t, *sentMsg.Sender.Name, *received.Message.Sender.Name)
	case err := <-sub.ErrChannel():
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for NATS pub/sub message")
	}
}

func TestNATSSubscriptionGracefulShutdown(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer backend.Close()

	ctx := context.Background()

	sub, err := backend.Subscribe(ctx, "ns", "temp_topic")
	require.NoError(t, err)

	// Close the subscription.
	require.NoError(t, sub.Close())

	// Channel should drain without producing messages.
	select {
	case msg := <-sub.Channel():
		t.Fatalf("expected no messages after close, got: %v", msg)
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}
