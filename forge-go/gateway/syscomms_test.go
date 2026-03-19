package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
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

func setupSysTestServer(t *testing.T) (*httptest.Server, *miniredis.Miniredis, *messaging.Client, store.Store, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	client := messaging.NewClient(rdb)
	dbPath := filepath.Join(t.TempDir(), "syscomms-test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             "g1",
		Name:           "test-guild",
		Description:    "syscomms tests",
		OrganizationID: "org-test",
		Status:         store.GuildStatusRunning,
	}))

	gemGen, _ := protocol.NewGemstoneGenerator(1)
	handler := gateway.SysCommsHandler(client, dbStore, gemGen)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/guilds/{id}/syscomms/{user_id}", handler.ServeHTTP)

	ts := httptest.NewServer(mux)

	cleanup := func() {
		ts.Close()
		_ = rdb.Close()
		_ = dbStore.Close()
		mr.Close()
	}

	return ts, mr, client, dbStore, cleanup
}

func connectSysWS(t *testing.T, ts *httptest.Server, guildID, userID string) (*websocket.Conn, *http.Response) {
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + fmt.Sprintf("/ws/guilds/%s/syscomms/%s", guildID, userID)
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(wsURL, nil)
	require.NoError(t, err)
	return conn, resp
}

func TestSysCommsRejectsUnknownGuild(t *testing.T) {
	ts, _, _, _, cleanup := setupSysTestServer(t)
	defer cleanup()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws/guilds/unknown/syscomms/u1"
	dialer := websocket.Dialer{}
	conn, resp, err := dialer.Dial(wsURL, nil)
	require.Error(t, err)
	if conn != nil {
		_ = conn.Close()
	}
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestSysCommsConnectionAnnounce(t *testing.T) {
	ts, mr, _, _, cleanup := setupSysTestServer(t)
	defer cleanup()

	// 1. Subscribe to guild_status_topic using go-redis directly for test assertion
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	pubsub := rdb.Subscribe(ctx, "g1:guild_status_topic")
	defer func() { _ = pubsub.Close() }()
	_, err := pubsub.Receive(ctx)
	require.NoError(t, err)

	// 2. Connect WebSocket Client
	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	// 3. Verify HealthCheckRequest announcement published to redis
	msgCh := pubsub.Channel()
	select {
	case redisMsg := <-msgCh:
		var msg protocol.Message
		err = json.Unmarshal([]byte(redisMsg.Payload), &msg)
		require.NoError(t, err)

		assert.Equal(t, "sys_comms_socket:u1", *msg.Sender.ID)
		assert.Equal(t, "rustic_ai.core.guild.agent_ext.mixins.health.HealthCheckRequest", msg.Format)
		assert.Contains(t, string(msg.Payload), "checktime")

	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for HealthCheckRequest announcement")
	}
}

func TestSysCommsIngressMutation(t *testing.T) {
	ts, mr, _, _, cleanup := setupSysTestServer(t)
	defer cleanup()

	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	// Need to wait slightly to let the connection announce pass so we know pumps are running
	time.Sleep(100 * time.Millisecond)

	// 1. Send JSON payload
	testPayload := map[string]interface{}{
		"format": "test.Format",
		"payload": map[string]string{
			"hello": "sys",
		},
		"traceparent": "00-testsys-0000000000000000-01",
	}

	err := conn.WriteJSON(testPayload)
	require.NoError(t, err)

	// 2. Wait for message persistence
	time.Sleep(100 * time.Millisecond)

	// 3. Inspect Redis for published message on user_system:u1
	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	sysInbox := "g1:user_system:u1"

	// Get latest message JSON
	zrange, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:   sysInbox,
		Start: 0,
		Stop:  0,
		Rev:   true,
	}).Result()
	require.NoError(t, err)
	require.NotEmpty(t, zrange, "Message was not inserted into ZSET")

	// ZRange returns the raw stored JSON in query.go
	storedMsgJSON := zrange[0]

	var storedMsg protocol.Message
	err = json.Unmarshal([]byte(storedMsgJSON), &storedMsg)
	require.NoError(t, err)

	msgID := storedMsg.ID

	// Python parity: server injects traceparent and ignores client-provided value.
	require.NotNil(t, storedMsg.Traceparent)
	assert.NotEmpty(t, *storedMsg.Traceparent)
	assert.NotEqual(t, "00-testsys-0000000000000000-01", *storedMsg.Traceparent)

	// Verify Sender override
	assert.Equal(t, "sys_comms_socket:u1", *storedMsg.Sender.ID)

	// Python parity: syscomms thread is reset to [current_message_id].
	assert.Equal(t, []uint64{msgID}, storedMsg.Thread)
}

func TestSysCommsEgressPassthrough(t *testing.T) {
	ts, _, msgClient, _, cleanup := setupSysTestServer(t)
	defer cleanup()

	conn, _ := connectSysWS(t, ts, "g1", "u1")
	defer func() { _ = conn.Close() }()

	require.NoError(t, conn.SetReadDeadline(time.Time{}))
	ctx := context.Background()

	// Give the Gateway's Redis 'Subscribe' loop a moment to attach before we start firing notifications
	time.Sleep(100 * time.Millisecond)

	// The syscomms connection always emits an initial HealthCheckRequest on guild_status_topic.
	// Drain it so passthrough assertions below are deterministic.
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

	err = msgClient.PublishMessage(ctx, "g1", "user_system_notification:u1", testMsg)
	require.NoError(t, err)

	var p []byte
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, p, err = conn.ReadMessage()
	require.NoError(t, err)

	var readMsg protocol.Message
	err = json.Unmarshal(p, &readMsg)
	require.NoError(t, err)

	assert.Equal(t, id1.ToInt(), readMsg.ID)
	assert.Equal(t, `"direct_system_notification"`, string(readMsg.Payload))

	id2, _ := gen.Generate(protocol.PriorityNormal)
	// Verify guild_status_topic passthrough works just as well
	broadcastMsg := &protocol.Message{
		ID:      id2.ToInt(),
		Payload: json.RawMessage(`"guild_broadcast"`),
	}

	err = msgClient.PublishMessage(ctx, "g1", "guild_status_topic", broadcastMsg)
	require.NoError(t, err)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, p2, err := conn.ReadMessage()
	require.NoError(t, err)

	var readMsg2 protocol.Message
	err = json.Unmarshal(p2, &readMsg2)
	require.NoError(t, err)

	assert.Equal(t, id2.ToInt(), readMsg2.ID)
	assert.Equal(t, `"guild_broadcast"`, string(readMsg2.Payload))
}

func TestSysCommsIngressDropsMessagesMissingFormatOrPayload(t *testing.T) {
	ts, mr, _, _, cleanup := setupSysTestServer(t)
	defer cleanup()

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
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	sysInbox := "g1:user_system:u1"
	zrange, err := rdb.ZRange(ctx, sysInbox, 0, -1).Result()
	require.NoError(t, err)
	assert.Empty(t, zrange, "invalid syscomms messages must not be persisted")
}
