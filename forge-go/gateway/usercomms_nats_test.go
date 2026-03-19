package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/testutil/natstest"
)

func setupNATSTestServer(t *testing.T) (messaging.Backend, store.Store, *httptest.Server) {
	t.Helper()
	backend := natstest.NewBackend(t)

	dbPath := filepath.Join(t.TempDir(), "usercomms-nats-test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbStore.Close() })

	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             "g1",
		Name:           "test-guild",
		Description:    "usercomms NATS tests",
		OrganizationID: "org-test",
		Status:         store.GuildStatusRunning,
	}))

	gemGen, _ := protocol.NewGemstoneGenerator(1)
	handler := gateway.UserCommsHandler(backend, dbStore, gemGen)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/guilds/{id}/usercomms/{user_id}/{user_name}", handler.ServeHTTP)

	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close() })

	return backend, dbStore, server
}

func TestNATSUserCommsIngressMutation(t *testing.T) {
	backend, _, server := setupNATSTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/g1/usercomms/u1/Alice"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	time.Sleep(50 * time.Millisecond)

	ctx := context.Background()

	// Verify announce was published to system_topic
	systemMsgs, err := backend.GetMessagesForTopic(ctx, "g1", "system_topic")
	require.NoError(t, err)
	require.Len(t, systemMsgs, 1)
	announce := systemMsgs[0]
	assert.Equal(t, "user_socket:u1", *announce.Sender.ID)
	assert.Equal(t, "Alice", *announce.Sender.Name)
	assert.Equal(t, "rustic_ai.core.agents.system.models.UserAgentCreationRequest", announce.Format)

	// Send browser-shaped payload (camelCase + data + valid history)
	customGen, _ := protocol.NewGemstoneGenerator(7)
	clientID, _ := customGen.Generate(protocol.PriorityNormal)
	rawMsg := map[string]interface{}{
		"id":             clientID.ToString(),
		"format":         "chatCompletionRequest",
		"conversationId": "789",
		"inReplyTo":      "42",
		"topic":          "echo_topic",
		"data": map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "text", "text": "hello"}}},
			},
		},
		"messageHistory": []interface{}{
			map[string]interface{}{
				"agent":     map[string]interface{}{"id": "upa-u1", "name": "Alice"},
				"origin":    "123",
				"result":    "456",
				"processor": "forward_message_to_user",
			},
		},
	}
	err = conn.WriteJSON(rawMsg)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	// Verify message was mutated and stored into user:u1
	inbox, err := backend.GetMessagesForTopic(ctx, "g1", "user:u1")
	require.NoError(t, err)
	require.Len(t, inbox, 1)
	mutated := inbox[0]

	assert.Equal(t, clientID.ToInt(), mutated.ID)
	assert.Equal(t, "user_socket:u1", *mutated.Sender.ID)
	assert.Equal(t, "Alice", *mutated.Sender.Name)

	require.NotNil(t, mutated.Traceparent)
	assert.Equal(t, "no_tracing", *mutated.Traceparent)

	var wrappedPayload map[string]interface{}
	err = json.Unmarshal(mutated.Payload, &wrappedPayload)
	require.NoError(t, err)

	assert.Equal(t, "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest", wrappedPayload["format"])
	assert.Equal(t, "echo_topic", wrappedPayload["topics"])
	assert.NotNil(t, wrappedPayload["payload"])
	assert.NotContains(t, wrappedPayload, "conversationId")
	assert.NotContains(t, wrappedPayload, "inReplyTo")
	assert.NotContains(t, wrappedPayload, "data")
	assert.EqualValues(t, 789, wrappedPayload["conversation_id"])
	assert.EqualValues(t, 42, wrappedPayload["in_response_to"])

	history, ok := wrappedPayload["message_history"].([]interface{})
	require.True(t, ok)
	assert.Len(t, history, 1)

	entry, ok := history[0].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 123, entry["origin"])
	assert.EqualValues(t, 456, entry["result"])
}

func TestNATSUserCommsEgressOnlyUserNotifications(t *testing.T) {
	backend, _, server := setupNATSTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/g1/usercomms/u1/Alice"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait for subscription to settle
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// Send an inbound message so readPump goroutine is confirmed running
	rawMsg := map[string]interface{}{"format": "x.y.Msg", "data": map[string]interface{}{"hello": "world"}}
	require.NoError(t, conn.WriteJSON(rawMsg))
	time.Sleep(100 * time.Millisecond)

	gen, _ := protocol.NewGemstoneGenerator(0)
	id1, _ := gen.Generate(protocol.PriorityNormal)
	socketAgent := "user_socket:u1"

	// Publish a direct notification to the user
	directMsg := &protocol.Message{
		ID:      id1.ToInt(),
		Payload: json.RawMessage(`"direct message"`),
		Format:  "Text",
		ForwardHeader: &protocol.ForwardHeader{
			OriginMessageID: 151515,
			OnBehalfOf: protocol.AgentTag{
				ID: &socketAgent,
			},
		},
	}
	err = backend.PublishMessage(ctx, "g1", "user_notifications:u1", directMsg)
	require.NoError(t, err)

	// Receive until we see the direct notification
	var receivedDirect protocol.Message
	for {
		err = conn.ReadJSON(&receivedDirect)
		require.NoError(t, err)
		if string(receivedDirect.Payload) == `"direct message"` {
			break
		}
	}

	// Publish a broadcast message; usercomms must NOT push it live to websocket
	id2, _ := gen.Generate(protocol.PriorityNormal)
	broadcastMsg := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"broadcast message"`),
		Format:  "Text",
	}
	err = backend.PublishMessage(ctx, "g1", "user_message_broadcast", broadcastMsg)
	require.NoError(t, err)

	// Assert no broadcast arrives on the live user websocket
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(300*time.Millisecond)))
	var shouldNotArrive protocol.Message
	err = conn.ReadJSON(&shouldNotArrive)
	require.Error(t, err)
}

func TestNATSUserCommsDropsInvalidMessageHistory(t *testing.T) {
	backend, _, server := setupNATSTestServer(t)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/g1/usercomms/u1/Alice"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	rawMsg := map[string]interface{}{
		"format": "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest",
		"data":   map[string]interface{}{"messages": []interface{}{}},
		"message_history": []interface{}{
			map[string]interface{}{
				"agent":  map[string]interface{}{"id": "upa-u1", "name": "Alice"},
				"origin": 123,
				"result": 456,
				// missing processor -> invalid ProcessEntry
			},
		},
	}
	require.NoError(t, conn.WriteJSON(rawMsg))
	time.Sleep(75 * time.Millisecond)

	ctx := context.Background()
	inbox, err := backend.GetMessagesForTopic(ctx, "g1", "user:u1")
	require.NoError(t, err)
	assert.Empty(t, inbox)
}
