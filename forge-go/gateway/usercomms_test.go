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

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func setupTestServer(t *testing.T) (*miniredis.Miniredis, *redis.Client, *messaging.Client, store.Store, *httptest.Server) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	msgClient := messaging.NewClient(rdb)
	dbPath := filepath.Join(t.TempDir(), "usercomms-test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             "g1",
		Name:           "test-guild",
		Description:    "usercomms tests",
		OrganizationID: "org-test",
		Status:         store.GuildStatusRunning,
	}))
	gemGen, _ := protocol.NewGemstoneGenerator(1)
	handler := gateway.UserCommsHandler(msgClient, dbStore, gemGen)

	// We use standard ServeMux to map PathValues
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/guilds/{id}/usercomms/{user_id}/{user_name}", handler.ServeHTTP)

	server := httptest.NewServer(mux)
	return mr, rdb, msgClient, dbStore, server
}

func TestUserCommsIngressMutation(t *testing.T) {
	mr, rdb, _, dbStore, server := setupTestServer(t)
	defer mr.Close()
	defer func() { _ = rdb.Close() }()
	defer func() { _ = dbStore.Close() }()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/g1/usercomms/u1/Alice"

	// 1. Connect Client
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait brief moment for the System Announce message
	time.Sleep(50 * time.Millisecond)

	// 2. Consume system topic to verify Announce
	ctx := context.Background()
	systemMsgs, err := rdb.ZRange(ctx, "g1:system_topic", 0, -1).Result()
	// Wait, the announce logic uses `PublishMessage` to `system` but sets topic to `system`.
	require.NoError(t, err)
	require.Len(t, systemMsgs, 1)

	var announce protocol.Message
	err = json.Unmarshal([]byte(systemMsgs[0]), &announce)
	require.NoError(t, err)
	assert.Equal(t, "user_socket:u1", *announce.Sender.ID)
	assert.Equal(t, "Alice", *announce.Sender.Name)
	assert.Equal(t, "rustic_ai.core.agents.system.models.UserAgentCreationRequest", announce.Format)

	// 3. Send browser-shaped payload (camelCase + data + malformed history)
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

	// 4. Verify it was mutated and stored into user_inbox
	inbox, err := rdb.ZRange(ctx, "g1:user:u1", 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, inbox, 1)

	var mutated protocol.Message
	err = json.Unmarshal([]byte(inbox[0]), &mutated)
	require.NoError(t, err)

	// Mutated assertions
	assert.Equal(t, clientID.ToInt(), mutated.ID) // Valid client ID is preserved by parity logic.

	assert.Equal(t, "user_socket:u1", *mutated.Sender.ID)
	assert.Equal(t, "Alice", *mutated.Sender.Name)

	require.NotNil(t, mutated.Traceparent)
	assert.Equal(t, "no_tracing", *mutated.Traceparent)

	// Ensure valid client history is preserved (Python validates and forwards it).
	var wrappedPayload map[string]interface{}
	err = json.Unmarshal(mutated.Payload, &wrappedPayload)
	require.NoError(t, err)

	// Browser envelope is normalized to Rustic Message payload shape.
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

func TestUserCommsEgressOnlyUserNotifications(t *testing.T) {
	mr, rdb, msgClient, dbStore, server := setupTestServer(t)
	defer mr.Close()
	defer func() { _ = rdb.Close() }()
	defer func() { _ = dbStore.Close() }()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/g1/usercomms/u1/Alice"

	// 1. Connect Client
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait for subscription confirmation
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// Send an initial payload so readPump has an inbound message to process.
	rawMsg := map[string]interface{}{"format": "x.y.Msg", "data": map[string]interface{}{"hello": "world"}}
	require.NoError(t, conn.WriteJSON(rawMsg))
	time.Sleep(100 * time.Millisecond) // Let the readPump generate and store it

	// 2. Publish an explicit direct notification to the user
	gen, _ := protocol.NewGemstoneGenerator(0)
	id1, _ := gen.Generate(protocol.PriorityNormal)

	socketAgent := "user_socket:u1"

	directMsg := &protocol.Message{
		ID:      id1.ToInt(),
		Payload: json.RawMessage(`"direct message"`),
		Format:  "Text",
		ForwardHeader: &protocol.ForwardHeader{
			OriginMessageID: 151515,
			OnBehalfOf: protocol.AgentTag{
				ID: &socketAgent, // Forwarded specifically on behalf of user
			},
		},
	}

	// This triggers writePump
	err = msgClient.PublishMessage(ctx, "g1", "user_notifications:u1", directMsg)
	require.NoError(t, err)

	// 3. Receive direct notification.
	var receivedDirect protocol.Message
	for {
		err = conn.ReadJSON(&receivedDirect)
		require.NoError(t, err)
		if string(receivedDirect.Payload) == `"direct message"` {
			break
		}
	}

	// 4. Publish a broadcast message; usercomms must not push it live to websocket.
	id2, _ := gen.Generate(protocol.PriorityNormal)
	broadcastMsg := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"broadcast message"`),
		Format:  "Text",
	}
	err = msgClient.PublishMessage(ctx, "g1", "user_message_broadcast", broadcastMsg)
	require.NoError(t, err)

	// 5. Ensure no broadcast is delivered on the live user websocket.
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(300*time.Millisecond)))
	var shouldNotArrive protocol.Message
	err = conn.ReadJSON(&shouldNotArrive)
	require.Error(t, err)
}

func TestUserCommsRejectsUnknownGuild(t *testing.T) {
	mr, rdb, _, dbStore, server := setupTestServer(t)
	defer mr.Close()
	defer func() { _ = rdb.Close() }()
	defer func() { _ = dbStore.Close() }()
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/guilds/unknown/usercomms/u1/Alice"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.Error(t, err)
	if conn != nil {
		_ = conn.Close()
	}
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestUserCommsDropsInvalidMessageHistory(t *testing.T) {
	mr, rdb, _, dbStore, server := setupTestServer(t)
	defer mr.Close()
	defer func() { _ = rdb.Close() }()
	defer func() { _ = dbStore.Close() }()
	defer server.Close()

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
	inbox, err := rdb.ZRange(ctx, "g1:user:u1", 0, -1).Result()
	require.NoError(t, err)
	assert.Empty(t, inbox)
}
