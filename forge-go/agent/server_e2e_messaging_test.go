package agent

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// startNATSServerForMessagingTest starts an in-process JetStream-enabled NATS server
// and registers cleanup.
func startNATSServerForMessagingTest(t *testing.T) *natsserver.Server {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		t.Fatal("NATS server did not become ready within 5s")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s
}

// awaitServerReady polls /readyz until it returns HTTP 200 or the deadline passes.
func awaitServerReady(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, timeout, 100*time.Millisecond, "server at %s did not become ready within %s", baseURL, timeout)
}

// TestStartServer_RedisBackend_MessagingPublishAndRetrieve starts a single-process Forge server
// with the default Redis messaging backend, publishes a message via a second Redis-backed
// messaging.Backend (sharing the same miniredis), and verifies round-trip retrieval.
func TestStartServer_RedisBackend_MessagingPublishAndRetrieve(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	listenAddr := reserveLocalAddr(t)
	cfg := &ServerConfig{
		DatabaseURL:        "file:testmsg_redis_e2e?mode=memory&cache=shared",
		RedisURL:           mr.Addr(),
		ListenAddress:      listenAddr,
		LeaderElectionMode: "redis",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = StartServer(ctx, cfg) }()
	awaitServerReady(t, "http://"+listenAddr, 8*time.Second)

	// Create a parallel Redis backend pointing at the same miniredis to verify
	// the server wired the Redis backend correctly and streams are writable.
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	backend := messaging.NewRedisBackend(rdb)

	gen, err := protocol.NewGemstoneGenerator(10)
	require.NoError(t, err)
	id, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)

	ctx2 := context.Background()
	msg := &protocol.Message{ID: id.ToInt()}
	require.NoError(t, backend.PublishMessage(ctx2, "e2e-guild", "e2e-topic", msg))

	msgs, err := backend.GetMessagesForTopic(ctx2, "e2e-guild", "e2e-topic")
	require.NoError(t, err)
	require.Len(t, msgs, 1, "expected 1 message from Redis backend")
	assert.Equal(t, msg.ID, msgs[0].ID)

	// GetMessagesByID should also return the same message.
	byID, err := backend.GetMessagesByID(ctx2, "e2e-guild", []uint64{id.ToInt()})
	require.NoError(t, err)
	require.Len(t, byID, 1)
	assert.Equal(t, msg.ID, byID[0].ID)
}

// TestStartServer_NATSBackend_MessagingPublishAndRetrieve starts a single-process Forge server
// with a NATS messaging backend (in-process nats-server), publishes a message via a second
// NATSBackend sharing the same NATS server, and verifies round-trip retrieval.
func TestStartServer_NATSBackend_MessagingPublishAndRetrieve(t *testing.T) {
	ns := startNATSServerForMessagingTest(t)

	// Redis is still needed for control-plane (leader election, control queues).
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	listenAddr := reserveLocalAddr(t)
	cfg := &ServerConfig{
		DatabaseURL:        "file:testmsg_nats_e2e?mode=memory&cache=shared",
		RedisURL:           mr.Addr(),
		NATSUrl:            ns.ClientURL(),
		ListenAddress:      listenAddr,
		LeaderElectionMode: "redis",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = StartServer(ctx, cfg) }()
	awaitServerReady(t, "http://"+listenAddr, 8*time.Second)

	// After StartServer sets the env var, the test process should see NATS_URL.
	assert.Equal(t, ns.ClientURL(), os.Getenv("NATS_URL"),
		"StartServer should export NATS_URL to the process environment")

	// Create a parallel NATS backend pointing at the same in-process NATS server.
	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)
	defer func() { _ = nc.Drain() }()

	backend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	defer func() { _ = backend.Close() }()

	gen, err := protocol.NewGemstoneGenerator(11)
	require.NoError(t, err)

	id1, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	id2, err := gen.Generate(protocol.PriorityNormal)
	require.NoError(t, err)

	ctx2 := context.Background()
	msg1 := &protocol.Message{ID: id1.ToInt()}
	msg2 := &protocol.Message{ID: id2.ToInt()}

	require.NoError(t, backend.PublishMessage(ctx2, "nats-guild", "nats-topic", msg1))
	require.NoError(t, backend.PublishMessage(ctx2, "nats-guild", "nats-topic", msg2))

	// GetMessagesForTopic — both messages, sorted.
	msgs, err := backend.GetMessagesForTopic(ctx2, "nats-guild", "nats-topic")
	require.NoError(t, err)
	require.Len(t, msgs, 2, "expected 2 messages from NATS JetStream")
	assert.Equal(t, msg1.ID, msgs[0].ID)
	assert.Equal(t, msg2.ID, msgs[1].ID)

	// GetMessagesSince — only msg2.
	since, err := backend.GetMessagesSince(ctx2, "nats-guild", "nats-topic", id1.ToInt())
	require.NoError(t, err)
	require.Len(t, since, 1, "expected 1 message after id1")
	assert.Equal(t, msg2.ID, since[0].ID)

	// GetMessagesByID — KV lookup.
	byID, err := backend.GetMessagesByID(ctx2, "nats-guild", []uint64{id1.ToInt(), id2.ToInt()})
	require.NoError(t, err)
	require.Len(t, byID, 2)
	assert.Equal(t, msg1.ID, byID[0].ID)
	assert.Equal(t, msg2.ID, byID[1].ID)
}
