package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

func TestHandler_Integration(t *testing.T) {
	// 1. Setup Miniredis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	// 2. Setup Dependencies
	regYaml := "entries:\n" +
		"  - id: TestAgent\n" +
		"    class_name: \"test.Agent\"\n" +
		"    runtime: binary\n" +
		"    executable: \"/bin/echo\"\n"
	tmpfile := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(tmpfile, []byte(regYaml), 0644))
	reg, err := registry.Load(tmpfile)
	require.NoError(t, err)

	sec := secrets.NewEnvSecretProvider()
	cp := NewRedisControlTransport(rdb)
	sup := supervisor.NewProcessSupervisor(supervisor.NewRedisAgentStatusStore(rdb), supervisor.WithWorkDirBase(t.TempDir()))

	dbStore, err := store.NewGormStore(store.DriverSQLite, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)

	spec := &protocol.GuildSpec{
		ID:          "guild-abc",
		Name:        "Test Guild",
		Description: "Mock guild",
		Agents:      []protocol.AgentSpec{},
		Properties:  make(map[string]interface{}),
	}
	gmodel := store.FromGuildSpec(spec, "test-org")
	err = dbStore.CreateGuild(gmodel)
	require.NoError(t, err)

	handler := NewControlQueueHandler(cp, reg, sec, sup, dbStore)
	err = handler.Start(ctx)
	require.NoError(t, err)

	// Give the listener loop a moment to start
	time.Sleep(50 * time.Millisecond)

	// 4. Send a protocol.SpawnRequest via Redis LPUSH (simulating a python control client)
	req := &protocol.SpawnRequest{
		RequestID: "req-spawn-1",
		GuildID:   "guild-abc",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-xyz",
			ClassName: "test.Agent",
		},
	}
	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}
	wb, _ := json.Marshal(wrapper)
	rdb.LPush(ctx, ControlQueueRequestKey, wb)

	// 5. Wait for the protocol.SpawnResponse via Redis LPUSH from the Responder
	respKey := "forge:control:response:req-spawn-1"
	b, err := rdb.BRPop(ctx, 3*time.Second, respKey).Result()
	require.NoError(t, err, "Handler did not send response in time")

	var raw map[string]interface{}
	err = json.Unmarshal([]byte(b[1]), &raw)
	require.NoError(t, err)

	if success, ok := raw["success"].(bool); !success || !ok {
		t.Fatalf("Spawn failed: %v", raw["detail"])
	}

	var spawnResp protocol.SpawnResponse
	err = json.Unmarshal([]byte(b[1]), &spawnResp)
	require.NoError(t, err)

	assert.True(t, spawnResp.Success)
	assert.NotEmpty(t, spawnResp.NodeID)
	assert.True(t, spawnResp.PID > 0)

	// 6. Send a protocol.StopRequest via Redis LPUSH
	stopReq := &protocol.StopRequest{
		RequestID: "req-stop-1",
		GuildID:   "guild-abc",
		AgentID:   "agent-xyz",
	}
	wrapper = map[string]interface{}{
		"command": "stop",
		"payload": stopReq,
	}
	wb, _ = json.Marshal(wrapper)
	rdb.LPush(ctx, ControlQueueRequestKey, wb)

	// 7. Wait for protocol.StopResponse
	stopRespKey := "forge:control:response:req-stop-1"
	b, err = rdb.BRPop(ctx, 5*time.Second, stopRespKey).Result()
	require.NoError(t, err, "Handler did not send stop response in time")

	var stopResp protocol.StopResponse
	err = json.Unmarshal([]byte(b[1]), &stopResp)
	require.NoError(t, err)

	assert.True(t, stopResp.Success)

	// 8. Cleanup
	handler.Stop()
}

func TestHandler_SpawnWithoutGuildStore_UsesFallback(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	regYaml := "entries:\n" +
		"  - id: TestAgent\n" +
		"    class_name: \"test.Agent\"\n" +
		"    runtime: binary\n" +
		"    executable: \"/bin/echo\"\n"
	tmpfile := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(tmpfile, []byte(regYaml), 0644))
	reg, err := registry.Load(tmpfile)
	require.NoError(t, err)

	sec := secrets.NewEnvSecretProvider()
	cp := NewRedisControlTransport(rdb)
	sup := supervisor.NewProcessSupervisor(supervisor.NewRedisAgentStatusStore(rdb), supervisor.WithWorkDirBase(t.TempDir()))

	handler := NewControlQueueHandler(cp, reg, sec, sup, nil)
	err = handler.Start(ctx)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	req := &protocol.SpawnRequest{
		RequestID: "req-spawn-fallback-1",
		GuildID:   "guild-missing",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-fallback",
			ClassName: "test.Agent",
		},
		MessagingConfig: &protocol.MessagingConfig{
			BackendModule: "rustic_ai.redis.messaging.backend",
			BackendClass:  "RedisMessagingBackend",
			BackendConfig: map[string]interface{}{
				"redis_client": map[string]interface{}{
					"host": "127.0.0.1",
					"port": "6379",
					"db":   0,
				},
			},
		},
	}
	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}
	wb, _ := json.Marshal(wrapper)
	rdb.LPush(ctx, ControlQueueRequestKey, wb)

	respKey := "forge:control:response:req-spawn-fallback-1"
	b, err := rdb.BRPop(ctx, 3*time.Second, respKey).Result()
	require.NoError(t, err, "Handler did not send fallback spawn response in time")

	var spawnResp protocol.SpawnResponse
	err = json.Unmarshal([]byte(b[1]), &spawnResp)
	require.NoError(t, err)
	assert.True(t, spawnResp.Success)
	assert.True(t, spawnResp.PID > 0)

	stopReq := &protocol.StopRequest{
		RequestID: "req-stop-fallback-1",
		GuildID:   "guild-missing",
		AgentID:   "agent-fallback",
	}
	stopWrapper := map[string]interface{}{
		"command": "stop",
		"payload": stopReq,
	}
	swb, _ := json.Marshal(stopWrapper)
	rdb.LPush(ctx, ControlQueueRequestKey, swb)

	stopRespKey := "forge:control:response:req-stop-fallback-1"
	sb, err := rdb.BRPop(ctx, 5*time.Second, stopRespKey).Result()
	require.NoError(t, err)

	var stopResp protocol.StopResponse
	err = json.Unmarshal([]byte(sb[1]), &stopResp)
	require.NoError(t, err)
	assert.True(t, stopResp.Success)

	handler.Stop()
}
