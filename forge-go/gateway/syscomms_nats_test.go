package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/gateway"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/testutil/natstest"
)

func setupNATSSysTestServer(t *testing.T) (*httptest.Server, messaging.Backend, store.Store) {
	t.Helper()
	backend := natstest.NewBackend(t)

	dbPath := filepath.Join(t.TempDir(), "syscomms-nats-test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbStore.Close() })

	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             "g1",
		Name:           "test-guild",
		Description:    "syscomms NATS tests",
		OrganizationID: "org-test",
		Status:         store.GuildStatusRunning,
	}))

	gemGen, _ := protocol.NewGemstoneGenerator(1)
	handler := gateway.SysCommsHandler(backend, dbStore, gemGen)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/guilds/{id}/syscomms/{user_id}", handler.ServeHTTP)

	ts := httptest.NewServer(mux)
	t.Cleanup(func() { ts.Close() })

	return ts, backend, dbStore
}

func TestNATSSysCommsConnectionAnnounce(t *testing.T) {
	ts, backend, _ := setupNATSSysTestServer(t)

	ctx := context.Background()

	// Subscribe to guild_status_topic BEFORE connecting WebSocket so we don't miss the announce
	sub, err := backend.Subscribe(ctx, "g1", "guild_status_topic")
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	// Connect WebSocket — this triggers the HealthCheckRequest announce
	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	// Receive the announce from the subscription channel
	select {
	case subMsg := <-sub.Channel():
		msg := subMsg.Message
		assert.Equal(t, "sys_comms_socket:u1", *msg.Sender.ID)
		assert.Equal(t, "rustic_ai.core.guild.agent_ext.mixins.health.HealthCheckRequest", msg.Format)
		assert.Contains(t, string(msg.Payload), "checktime")

	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for HealthCheckRequest announcement")
	}
}

func TestNATSSysCommsIngressMutation(t *testing.T) {
	ts, backend, _ := setupNATSSysTestServer(t)

	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	// Wait for connection announce to complete
	time.Sleep(100 * time.Millisecond)

	testPayload := map[string]interface{}{
		"format": "test.Format",
		"payload": map[string]string{
			"hello": "sys",
		},
		"traceparent": "00-testsys-0000000000000000-01",
	}
	err := conn.WriteJSON(testPayload)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()
	msgs, err := backend.GetMessagesForTopic(ctx, "g1", "user_system:u1")
	require.NoError(t, err)
	require.NotEmpty(t, msgs, "message was not stored into user_system:u1")

	storedMsg := msgs[len(msgs)-1]
	msgID := storedMsg.ID

	// Server injects traceparent and ignores client-provided value
	require.NotNil(t, storedMsg.Traceparent)
	assert.NotEmpty(t, *storedMsg.Traceparent)
	assert.NotEqual(t, "00-testsys-0000000000000000-01", *storedMsg.Traceparent)

	// Sender is overridden
	assert.Equal(t, "sys_comms_socket:u1", *storedMsg.Sender.ID)

	// Thread is reset to [current_message_id]
	assert.Equal(t, []uint64{msgID}, storedMsg.Thread)
}

func TestNATSSysCommsEgressPassthrough(t *testing.T) {
	ts, backend, _ := setupNATSSysTestServer(t)

	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetReadDeadline(time.Time{}))
	ctx := context.Background()

	// Give the subscriptions time to attach
	time.Sleep(100 * time.Millisecond)

	// Drain the initial HealthCheckRequest announce
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, initialPayload, err := conn.ReadMessage()
	require.NoError(t, err)
	var initialMsg protocol.Message
	require.NoError(t, json.Unmarshal(initialPayload, &initialMsg))
	assert.Equal(t, "rustic_ai.core.guild.agent_ext.mixins.health.HealthCheckRequest", initialMsg.Format)

	gen, _ := protocol.NewGemstoneGenerator(0)
	id1, _ := gen.Generate(protocol.PriorityNormal)

	// Publish to user_system_notification:u1
	testMsg := &protocol.Message{
		ID:      id1.ToInt(),
		Payload: json.RawMessage(`"direct_system_notification"`),
	}
	err = backend.PublishMessage(ctx, "g1", "user_system_notification:u1", testMsg)
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, p, err := conn.ReadMessage()
	require.NoError(t, err)

	var readMsg protocol.Message
	require.NoError(t, json.Unmarshal(p, &readMsg))
	assert.Equal(t, id1.ToInt(), readMsg.ID)
	assert.Equal(t, `"direct_system_notification"`, string(readMsg.Payload))

	// Verify guild_status_topic passthrough
	id2, _ := gen.Generate(protocol.PriorityNormal)
	broadcastMsg := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"guild_broadcast"`),
	}
	err = backend.PublishMessage(ctx, "g1", "guild_status_topic", broadcastMsg)
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, p2, err := conn.ReadMessage()
	require.NoError(t, err)

	var readMsg2 protocol.Message
	require.NoError(t, json.Unmarshal(p2, &readMsg2))
	assert.Equal(t, id2.ToInt(), readMsg2.ID)
	assert.Equal(t, `"guild_broadcast"`, string(readMsg2.Payload))
}

func TestNATSSysCommsIngressDrops(t *testing.T) {
	ts, backend, _ := setupNATSSysTestServer(t)

	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()
	time.Sleep(100 * time.Millisecond)

	// Missing format
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"payload": map[string]interface{}{"x": 1},
	}))
	// Missing payload
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"format": "x.y.Format",
	}))

	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()
	msgs, err := backend.GetMessagesForTopic(ctx, "g1", "user_system:u1")
	require.NoError(t, err)
	assert.Empty(t, msgs, "invalid syscomms messages must not be persisted")
}
