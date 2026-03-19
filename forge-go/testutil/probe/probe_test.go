package probe

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeAgent(t *testing.T) {
	// 1. Start miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	// 2. Set up Redis client
	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()

	// 3. Create ProbeAgent
	probe := NewProbeAgent(rdb)

	topic := "test_topic"
	namespace := "test_ns"

	// Create a test message
	msg := DefaultMessage(1, "TestSender", map[string]interface{}{
		"text": "hello the world",
	})
	msg.Timestamp = float64(time.Now().UnixNano()) / 1e9

	// 4. Test Subscribe/WaitForMessage and Publish concurrently

	// We start a goroutine to wait for message, to ensure subscriber is active
	// before we publish. Wait for 2 seconds.
	waitCh := make(chan *Message)
	errCh := make(chan error)

	go func() {
		receivedMsg, err := probe.WaitForMessage(ctx, topic, 2*time.Second)
		if err != nil {
			errCh <- err
			return
		}
		waitCh <- receivedMsg
	}()

	// Small delay to let subscriber connect
	time.Sleep(100 * time.Millisecond)

	// Publish message
	err = probe.Publish(ctx, namespace, topic, msg)
	require.NoError(t, err)

	// Verify we got it
	select {
	case receivedMsg := <-waitCh:
		assert.Equal(t, msg.ID, receivedMsg.ID)
		assert.Equal(t, *msg.Sender.Name, *receivedMsg.Sender.Name)
		assert.Equal(t, msg.Format, receivedMsg.Format)

		// Map structures might convert to float64, check generic map elements
		payloadMap, ok := receivedMsg.Payload["text"].(string)
		require.True(t, ok)
		assert.Equal(t, "hello the world", payloadMap)

	case err := <-errCh:
		t.Fatalf("WaitForMessage failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Timeout waiting for message from wait channel")
	}

	// 5. Verify ZSET side effects manually
	zsetCount := rdb.ZCard(ctx, topic).Val()
	assert.Equal(t, int64(1), zsetCount)

	// 6. Verify Key side effects
	keyExist := rdb.Exists(ctx, "msg:test_ns:1").Val()
	assert.Equal(t, int64(1), keyExist)
}
