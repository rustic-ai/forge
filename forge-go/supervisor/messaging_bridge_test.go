package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/require"
)

func TestSplitBridgeTopic(t *testing.T) {
	t.Run("derive namespace from namespaced topic", func(t *testing.T) {
		namespace, bareTopic, canonical, err := splitBridgeTopic("", "guild-1:default_topic")
		require.NoError(t, err)
		require.Equal(t, "guild-1", namespace)
		require.Equal(t, "default_topic", bareTopic)
		require.Equal(t, "guild-1:default_topic", canonical)
	})

	t.Run("strip namespace prefix once", func(t *testing.T) {
		namespace, bareTopic, canonical, err := splitBridgeTopic("guild-1", "guild-1:default_topic")
		require.NoError(t, err)
		require.Equal(t, "guild-1", namespace)
		require.Equal(t, "default_topic", bareTopic)
		require.Equal(t, "guild-1:default_topic", canonical)
	})

	t.Run("preserve bare topic when namespace provided", func(t *testing.T) {
		namespace, bareTopic, canonical, err := splitBridgeTopic("guild-1", "default_topic")
		require.NoError(t, err)
		require.Equal(t, "guild-1", namespace)
		require.Equal(t, "default_topic", bareTopic)
		require.Equal(t, "guild-1:default_topic", canonical)
	})
}

func TestBridgeEnvelopeUnmarshalMessageIDs(t *testing.T) {
	var envelope bridgeEnvelope

	err := json.Unmarshal([]byte(`{"msg_ids":[1,2,3]}`), &envelope)
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 2, 3}, envelope.requestedMessageIDs())

	var legacy bridgeEnvelope
	err = json.Unmarshal([]byte(`{"message_ids":[4,5]}`), &legacy)
	require.NoError(t, err)
	require.Equal(t, []uint64{4, 5}, legacy.requestedMessageIDs())
}

func TestBridgeSocketPathIsShortAndStable(t *testing.T) {
	socketPath := bridgeSocketPath(
		"/tmp/forge-zmq",
		"guild-with-a-very-long-identifier",
		"agent-with-an-even-longer-identifier",
		"/tmp/some/really/deep/agent/workdir/path",
	)

	require.Contains(t, socketPath, "/tmp/forge-zmq/")
	require.Less(t, len(socketPath), 100)
	require.Equal(
		t,
		socketPath,
		bridgeSocketPath(
			"/tmp/forge-zmq",
			"guild-with-a-very-long-identifier",
			"agent-with-an-even-longer-identifier",
			"/tmp/some/really/deep/agent/workdir/path",
		),
	)
}

func TestDecodeBridgeMessageAcceptsObjectAndJSONString(t *testing.T) {
	objectRaw := json.RawMessage(`{"id":123,"topics":"default_topic","sender":{"id":"agent-1","name":"Agent 1"},"payload":{"message":"hello"}}`)
	message, err := decodeBridgeMessage(objectRaw)
	require.NoError(t, err)
	require.Equal(t, uint64(123), message.ID)

	encodedRaw := json.RawMessage(`"{\"id\":456,\"topics\":\"default_topic\",\"sender\":{\"id\":\"agent-1\",\"name\":\"Agent 1\"},\"payload\":{\"message\":\"hello\"}}"`)
	message, err = decodeBridgeMessage(encodedRaw)
	require.NoError(t, err)
	require.Equal(t, uint64(456), message.ID)
}

func TestResolvedTransportFromEnv(t *testing.T) {
	require.Equal(
		t,
		protocol.AgentTransportSupervisorZMQ,
		resolvedTransportFromEnv(
			[]string{protocol.EnvForgeAgentTransport + "=" + string(protocol.AgentTransportSupervisorZMQ)},
			"direct",
		),
	)

	require.Equal(
		t,
		protocol.AgentTransportDirect,
		resolvedTransportFromEnv(nil, "direct"),
	)
}

func TestNormalizeBridgeTransportMode(t *testing.T) {
	require.Equal(t, BridgeTransportIPC, NormalizeBridgeTransportMode("ipc"))
	require.Equal(t, BridgeTransportIPC, NormalizeBridgeTransportMode("IPC"))
	require.Equal(t, BridgeTransportIPC, NormalizeBridgeTransportMode(""))
	require.Equal(t, BridgeTransportIPC, NormalizeBridgeTransportMode("unknown"))
	require.Equal(t, BridgeTransportTCP, NormalizeBridgeTransportMode("tcp"))
	require.Equal(t, BridgeTransportTCP, NormalizeBridgeTransportMode("TCP"))
	require.Equal(t, BridgeTransportTCP, NormalizeBridgeTransportMode("  tcp  "))
}

func TestNewAgentMessagingBridgeWithMode_TCP(t *testing.T) {
	// TCP mode should produce a tcp:// endpoint and empty SocketPath.
	// We need a messaging backend stub for bridge creation.
	bridge, err := NewAgentMessagingBridgeWithMode(
		t.Context(),
		"guild-1",
		"agent-1",
		"/tmp/test-workdir",
		&stubMessagingBackend{},
		BridgeTransportTCP,
	)
	require.NoError(t, err)
	defer bridge.Close()

	require.Equal(t, BridgeTransportTCP, bridge.Mode())
	require.Empty(t, bridge.SocketPath())
	require.True(t, len(bridge.Endpoint()) > 0)
	require.Contains(t, bridge.Endpoint(), "tcp://127.0.0.1:")
}

func TestNewAgentMessagingBridgeWithMode_IPC(t *testing.T) {
	bridge, err := NewAgentMessagingBridgeWithMode(
		t.Context(),
		"guild-1",
		"agent-1",
		"/tmp/test-workdir",
		&stubMessagingBackend{},
		BridgeTransportIPC,
	)
	require.NoError(t, err)
	defer bridge.Close()

	require.Equal(t, BridgeTransportIPC, bridge.Mode())
	require.NotEmpty(t, bridge.SocketPath())
	require.Contains(t, bridge.Endpoint(), "ipc://")
}

// stubMessagingBackend is a minimal stub for bridge creation tests.
type stubMessagingBackend struct{}

func (s *stubMessagingBackend) PublishMessage(_ context.Context, _, _ string, _ *protocol.Message) error {
	return nil
}
func (s *stubMessagingBackend) GetMessagesForTopic(_ context.Context, _, _ string) ([]protocol.Message, error) {
	return nil, nil
}
func (s *stubMessagingBackend) GetMessagesSince(_ context.Context, _, _ string, _ uint64) ([]protocol.Message, error) {
	return nil, nil
}
func (s *stubMessagingBackend) GetMessagesByID(_ context.Context, _ string, _ []uint64) ([]protocol.Message, error) {
	return nil, nil
}
func (s *stubMessagingBackend) Subscribe(_ context.Context, _ string, _ ...string) (messaging.Subscription, error) {
	return nil, nil
}
func (s *stubMessagingBackend) Close() error { return nil }
