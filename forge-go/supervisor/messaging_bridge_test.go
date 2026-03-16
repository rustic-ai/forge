package supervisor

import (
	"encoding/json"
	"testing"

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
