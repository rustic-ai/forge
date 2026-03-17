package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

// fakeStatusStore for handler idempotency tests.
type fakeStatusStore struct {
	mu       sync.RWMutex
	statuses map[string]*supervisor.AgentStatusJSON
}

func newFakeStatusStore() *fakeStatusStore {
	return &fakeStatusStore{statuses: make(map[string]*supervisor.AgentStatusJSON)}
}

func (f *fakeStatusStore) WriteStatus(_ context.Context, guildID, agentID string, status *supervisor.AgentStatusJSON, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[guildID+":"+agentID] = status
	return nil
}

func (f *fakeStatusStore) RefreshStatus(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

func (f *fakeStatusStore) GetStatus(_ context.Context, guildID, agentID string) (*supervisor.AgentStatusJSON, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.statuses[guildID+":"+agentID], nil
}

func (f *fakeStatusStore) DeleteStatus(_ context.Context, guildID, agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.statuses, guildID+":"+agentID)
	return nil
}

// orderTrackingSupervisor records when Launch is called, for testing call ordering.
type orderTrackingSupervisor struct {
	mu        sync.Mutex
	launched  map[string]struct{}
	stopped   map[string]struct{}
	callOrder []string
}

func newOrderTrackingSupervisor() *orderTrackingSupervisor {
	return &orderTrackingSupervisor{
		launched: make(map[string]struct{}),
		stopped:  make(map[string]struct{}),
	}
}

func (o *orderTrackingSupervisor) Launch(_ context.Context, guildID string, agentSpec *protocol.AgentSpec, _ *registry.Registry, _ []string) error {
	key := guildID + "::" + agentSpec.ID
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.launched[key]; exists {
		return fmt.Errorf("agent %s is already managed in guild %s", agentSpec.ID, guildID)
	}
	o.launched[key] = struct{}{}
	o.callOrder = append(o.callOrder, "launch:"+key)
	return nil
}

func (o *orderTrackingSupervisor) Stop(_ context.Context, guildID, agentID string) error {
	key := guildID + "::" + agentID
	o.mu.Lock()
	defer o.mu.Unlock()
	o.stopped[key] = struct{}{}
	return nil
}

func (o *orderTrackingSupervisor) Status(_ context.Context, guildID, agentID string) (string, error) {
	key := guildID + "::" + agentID
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.launched[key]; ok {
		return "running", nil
	}
	return "unknown", nil
}

func (o *orderTrackingSupervisor) StopAll(_ context.Context) error { return nil }

func setupIdempotencyTest(t *testing.T) (*redis.Client, *fakeStatusStore, *orderTrackingSupervisor, *ControlQueueHandler) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	reg := loadTestRegistry(t, `entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`)
	ss := newFakeStatusStore()
	sup := newOrderTrackingSupervisor()

	handler := NewControlQueueHandler(
		NewRedisControlTransport(rdb),
		reg,
		secrets.NewEnvSecretProvider(),
		sup,
		nil,
		WithStatusStore(ss),
		WithNodeID("test-node"),
	)
	return rdb, ss, sup, handler
}

func TestHandleSpawn_SkipsIfRunningOnDifferentNode(t *testing.T) {
	rdb, ss, sup, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	// Pre-write status: running on a different node
	_ = ss.WriteStatus(ctx, "g1", "a1", &supervisor.AgentStatusJSON{
		State: "running", NodeID: "other-node", Timestamp: time.Now(),
	}, 30*time.Second)

	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-skip-running",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	// Should NOT have launched
	sup.mu.Lock()
	assert.Empty(t, sup.launched)
	sup.mu.Unlock()

	// Should have sent a success response
	b, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-skip-running").Result()
	require.NoError(t, err)
	var resp protocol.SpawnResponse
	require.NoError(t, json.Unmarshal([]byte(b[1]), &resp))
	assert.True(t, resp.Success)
	assert.Contains(t, resp.Message, "already running")
}

func TestHandleSpawn_SkipsIfStartingOnDifferentNode(t *testing.T) {
	rdb, ss, sup, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	_ = ss.WriteStatus(ctx, "g1", "a1", &supervisor.AgentStatusJSON{
		State: "starting", NodeID: "other-node", Timestamp: time.Now(),
	}, 30*time.Second)

	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-skip-starting",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	sup.mu.Lock()
	assert.Empty(t, sup.launched)
	sup.mu.Unlock()

	b, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-skip-starting").Result()
	require.NoError(t, err)
	var resp protocol.SpawnResponse
	require.NoError(t, json.Unmarshal([]byte(b[1]), &resp))
	assert.True(t, resp.Success)
	assert.Contains(t, resp.Message, "already starting")
}

func TestHandleSpawn_ProceedsIfStartingOnSameNode(t *testing.T) {
	_, ss, sup, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	_ = ss.WriteStatus(ctx, "g1", "a1", &supervisor.AgentStatusJSON{
		State: "starting", NodeID: "test-node", Timestamp: time.Now(),
	}, 30*time.Second)

	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-same-node",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	// Should have launched (same node = re-entrant)
	sup.mu.Lock()
	assert.Contains(t, sup.launched, "g1::a1")
	sup.mu.Unlock()
}

func TestHandleSpawn_ProceedsIfNoStatus(t *testing.T) {
	_, _, sup, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-no-status",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	sup.mu.Lock()
	assert.Contains(t, sup.launched, "g1::a1")
	sup.mu.Unlock()
}

func TestHandleSpawn_WritesStartingToStatusStore(t *testing.T) {
	_, ss, _, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-ack-write",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	status, err := ss.GetStatus(ctx, "g1", "a1")
	require.NoError(t, err)
	require.NotNil(t, status)
	// After a successful launch the supervisor may overwrite to "running",
	// but at minimum the handler wrote "starting" before launch.
	// With our fake supervisor that doesn't write status, it stays "starting".
	assert.Equal(t, "starting", status.State)
	assert.Equal(t, "test-node", status.NodeID)
}

func TestHandleSpawn_AlreadyManagedLocally_SendsError(t *testing.T) {
	rdb, _, sup, handler := setupIdempotencyTest(t)
	ctx := context.Background()

	// First spawn succeeds
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-first",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	sup.mu.Lock()
	assert.Contains(t, sup.launched, "g1::a1")
	sup.mu.Unlock()

	// Second spawn for same agent: supervisor returns "already managed" error
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-dup",
		GuildID:   "g1",
		AgentSpec: protocol.AgentSpec{ID: "a1", ClassName: "test.Agent"},
	})

	// The second request should get an error response (since same nodeID means the gate doesn't skip)
	// but note: statusStore now has "starting" for test-node, so gate allows it through.
	// The supervisor itself returns duplicate error.
	b, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-dup").Result()
	require.NoError(t, err)
	var errResp protocol.ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(b[1]), &errResp))
	assert.False(t, errResp.Success)
	assert.Contains(t, errResp.Error, "already managed")
}
