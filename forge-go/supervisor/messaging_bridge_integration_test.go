package supervisor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-zeromq/zmq4"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// zmqRequest sends a bridge envelope over ZMQ and waits for a response.
func zmqRequest(t *testing.T, sock zmq4.Socket, env bridgeEnvelope) bridgeEnvelope {
	t.Helper()
	data, err := json.Marshal(env)
	require.NoError(t, err, "marshal request envelope")

	err = sock.Send(zmq4.NewMsg(data))
	require.NoError(t, err, "send ZMQ request")

	msg, err := sock.Recv()
	require.NoError(t, err, "recv ZMQ response")

	var out bridgeEnvelope
	require.NoError(t, json.Unmarshal(msg.Bytes(), &out), "unmarshal response envelope")
	return out
}

// zmqRecvTimeout reads from the ZMQ socket with a deadline.
func zmqRecvTimeout(t *testing.T, sock zmq4.Socket, timeout time.Duration) bridgeEnvelope {
	t.Helper()
	type result struct {
		env bridgeEnvelope
		err error
	}
	ch := make(chan result, 1)
	go func() {
		msg, err := sock.Recv()
		if err != nil {
			ch <- result{err: err}
			return
		}
		var env bridgeEnvelope
		err = json.Unmarshal(msg.Bytes(), &env)
		ch <- result{env: env, err: err}
	}()
	select {
	case r := <-ch:
		require.NoError(t, r.err, "recv ZMQ event")
		return r.env
	case <-time.After(timeout):
		t.Fatal("timed out waiting for ZMQ message")
		return bridgeEnvelope{}
	}
}

// makeTestMessage builds a protocol.Message with the given GemstoneID and payload string.
func makeTestMessage(t *testing.T, id protocol.GemstoneID, payloadStr string) protocol.Message {
	t.Helper()
	msg := protocol.NewMessageFromGemstoneID(id)
	msg.Topics = protocol.TopicsFromString("guild-test:alpha")
	senderID := "test-sender"
	senderName := "test"
	msg.Sender = protocol.AgentTag{ID: &senderID, Name: &senderName}
	msg.Format = "application/json"
	msg.Payload = json.RawMessage(`{"data":"` + payloadStr + `"}`)
	return msg
}

// bridgeRoundTrip exercises the full bridge protocol against a real messaging.Backend.
func bridgeRoundTrip(t *testing.T, backend messaging.Backend) {
	t.Helper()
	ctx := context.Background()

	// 1. Setup bridge
	bridge, err := NewAgentMessagingBridge(ctx, "guild-test", "agent-test", t.TempDir(), backend)
	require.NoError(t, err, "create bridge")
	defer bridge.Close()

	// 2. Connect ZMQ client
	sock := zmq4.NewPair(ctx, zmq4.WithAutomaticReconnect(false))
	err = sock.Dial(bridge.Endpoint())
	require.NoError(t, err, "dial bridge endpoint")
	defer func() { _ = sock.Close() }()

	// Small delay for ZMQ handshake
	time.Sleep(50 * time.Millisecond)

	// 3. Ping
	resp := zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "ping",
		RequestID: "req-ping",
	})
	assert.True(t, resp.OK, "ping should succeed")
	assert.Equal(t, "response", resp.Kind)
	assert.Equal(t, "req-ping", resp.RequestID)

	// 4. Publish 2 messages via bridge
	gen, err := protocol.NewGemstoneGenerator(1)
	require.NoError(t, err)

	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	id2, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)

	msg1 := makeTestMessage(t, id1, "hello-1")
	msg2 := makeTestMessage(t, id2, "hello-2")

	msg1Raw, err := json.Marshal(msg1)
	require.NoError(t, err)
	msg2Raw, err := json.Marshal(msg2)
	require.NoError(t, err)

	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "publish",
		RequestID: "req-pub-1",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
		Message:   json.RawMessage(msg1Raw),
	})
	require.True(t, resp.OK, "publish msg1: %s", resp.Error)

	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "publish",
		RequestID: "req-pub-2",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
		Message:   json.RawMessage(msg2Raw),
	})
	require.True(t, resp.OK, "publish msg2: %s", resp.Error)

	// 5. get_messages — both messages returned
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "get_messages",
		RequestID: "req-get-all",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
	})
	require.True(t, resp.OK, "get_messages: %s", resp.Error)
	require.Len(t, resp.Messages, 2, "get_messages should return 2 messages")

	// Decode the returned messages to verify IDs
	var retMsg1, retMsg2 protocol.Message
	require.NoError(t, json.Unmarshal(resp.Messages[0], &retMsg1))
	require.NoError(t, json.Unmarshal(resp.Messages[1], &retMsg2))
	assert.Equal(t, id1.ToInt(), retMsg1.ID)
	assert.Equal(t, id2.ToInt(), retMsg2.ID)

	// 6. get_since — only msg2 returned (since is exclusive of sinceID)
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "get_since",
		RequestID: "req-get-since",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
		SinceID:   id1.ToInt(),
	})
	require.True(t, resp.OK, "get_since: %s", resp.Error)
	require.Len(t, resp.Messages, 1, "get_since should return 1 message")
	var sincedMsg protocol.Message
	require.NoError(t, json.Unmarshal(resp.Messages[0], &sincedMsg))
	assert.Equal(t, id2.ToInt(), sincedMsg.ID)

	// 7. get_next — single message after msg1, which is msg2
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "get_next",
		RequestID: "req-get-next",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
		SinceID:   id1.ToInt(),
	})
	require.True(t, resp.OK, "get_next: %s", resp.Error)
	require.NotNil(t, resp.Message, "get_next should return a message")
	var nextMsg protocol.Message
	require.NoError(t, json.Unmarshal(resp.Message, &nextMsg))
	assert.Equal(t, id2.ToInt(), nextMsg.ID)

	// 8. get_by_id — request both IDs (reversed order), assert both returned
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:       "request",
		Op:         "get_by_id",
		RequestID:  "req-get-by-id",
		Namespace:  "guild-test",
		MessageIDs: []uint64{id2.ToInt(), id1.ToInt()},
	})
	require.True(t, resp.OK, "get_by_id: %s", resp.Error)
	require.Len(t, resp.Messages, 2, "get_by_id should return 2 messages")

	// 9. Subscribe + deliver
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "subscribe",
		RequestID: "req-sub",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
	})
	require.True(t, resp.OK, "subscribe: %s", resp.Error)

	// Give subscription time to establish
	time.Sleep(100 * time.Millisecond)

	// Publish a 3rd message directly via the backend
	time.Sleep(10 * time.Millisecond)
	id3, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	msg3 := makeTestMessage(t, id3, "hello-3")
	err = backend.PublishMessage(ctx, "guild-test", "alpha", &msg3)
	require.NoError(t, err, "publish msg3 via backend")

	// Wait for deliver event on ZMQ socket
	event := zmqRecvTimeout(t, sock, 5*time.Second)
	assert.Equal(t, "event", event.Kind)
	assert.Equal(t, "deliver", event.Op)
	assert.Equal(t, "guild-test:alpha", event.Topic)
	require.NotNil(t, event.Message, "deliver event should contain a message")
	var deliveredMsg protocol.Message
	require.NoError(t, json.Unmarshal(event.Message, &deliveredMsg))
	assert.Equal(t, id3.ToInt(), deliveredMsg.ID)

	// 10. Unsubscribe
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "unsubscribe",
		RequestID: "req-unsub",
		Namespace: "guild-test",
		Topic:     "guild-test:alpha",
	})
	require.True(t, resp.OK, "unsubscribe: %s", resp.Error)

	// 11. Cleanup
	resp = zmqRequest(t, sock, bridgeEnvelope{
		Kind:      "request",
		Op:        "cleanup",
		RequestID: "req-cleanup",
	})
	require.True(t, resp.OK, "cleanup: %s", resp.Error)
}

func TestBridgeRoundTrip_Redis(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	backend := messaging.NewRedisBackend(rdb)
	defer func() { _ = backend.Close() }()

	bridgeRoundTrip(t, backend)
}

func startInProcessNATSServer(t *testing.T) *server.Server {
	t.Helper()
	opts := &server.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := server.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("in-process NATS server did not become ready within 5s")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s
}

func TestBridgeRoundTrip_NATS(t *testing.T) {
	s := startInProcessNATSServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	bridgeRoundTrip(t, backend)
}
