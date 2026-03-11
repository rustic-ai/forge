package control

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

type fakeSupervisor struct {
	mu       sync.Mutex
	launched map[string]struct{}
	stopped  map[string]struct{}
}

func newFakeSupervisor() *fakeSupervisor {
	return &fakeSupervisor{
		launched: make(map[string]struct{}),
		stopped:  make(map[string]struct{}),
	}
}

func (f *fakeSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	_ = ctx
	_ = reg
	_ = env
	key := guildID + "::" + agentSpec.ID
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.launched[key]; exists {
		return fmt.Errorf("duplicate launch %s", key)
	}
	f.launched[key] = struct{}{}
	return nil
}

func (f *fakeSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	_ = ctx
	key := guildID + "::" + agentID
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.launched[key]; !exists {
		return fmt.Errorf("unknown agent %s", key)
	}
	f.stopped[key] = struct{}{}
	return nil
}

func (f *fakeSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	_ = ctx
	key := guildID + "::" + agentID
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, stopped := f.stopped[key]; stopped {
		return "stopped", nil
	}
	if _, exists := f.launched[key]; exists {
		return "running", nil
	}
	return "unknown", nil
}

func (f *fakeSupervisor) StopAll(ctx context.Context) error {
	_ = ctx
	return nil
}

func TestHandler_OrganizationScopedSupervisors(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	tmpfile := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(tmpfile, []byte(regYaml), 0644))
	reg, err := registry.Load(tmpfile)
	require.NoError(t, err)

	supervisors := make(map[string]*fakeSupervisor)
	var mu sync.Mutex

	handler := NewControlQueueHandlerWithFactory(
		rdb,
		reg,
		secrets.NewEnvSecretProvider(),
		func(orgID string) supervisor.AgentSupervisor {
			mu.Lock()
			defer mu.Unlock()
			if sup, ok := supervisors[orgID]; ok {
				return sup
			}
			sup := newFakeSupervisor()
			supervisors[orgID] = sup
			return sup
		},
		nil,
	)

	reqA := &protocol.SpawnRequest{
		RequestID:      "req-org-a",
		OrganizationID: "org-a",
		GuildID:        "same-guild",
		AgentSpec: protocol.AgentSpec{
			ID:        "same-agent",
			ClassName: "test.Agent",
		},
	}
	reqB := &protocol.SpawnRequest{
		RequestID:      "req-org-b",
		OrganizationID: "org-b",
		GuildID:        "same-guild",
		AgentSpec: protocol.AgentSpec{
			ID:        "same-agent",
			ClassName: "test.Agent",
		},
	}

	handler.handleSpawn(ctx, reqA)
	handler.handleSpawn(ctx, reqB)

	respA, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-org-a").Result()
	require.NoError(t, err)
	respB, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-org-b").Result()
	require.NoError(t, err)

	var bodyA protocol.SpawnResponse
	require.NoError(t, json.Unmarshal([]byte(respA[1]), &bodyA))
	require.True(t, bodyA.Success)
	var bodyB protocol.SpawnResponse
	require.NoError(t, json.Unmarshal([]byte(respB[1]), &bodyB))
	require.True(t, bodyB.Success)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, supervisors, 2)
	require.Len(t, supervisors["org-a"].launched, 1)
	require.Len(t, supervisors["org-b"].launched, 1)
}

func TestHandler_StopUsesRecordedOrganization(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	tmpfile := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(tmpfile, []byte(regYaml), 0644))
	reg, err := registry.Load(tmpfile)
	require.NoError(t, err)

	supervisors := make(map[string]*fakeSupervisor)
	var mu sync.Mutex

	handler := NewControlQueueHandlerWithFactory(
		rdb,
		reg,
		secrets.NewEnvSecretProvider(),
		func(orgID string) supervisor.AgentSupervisor {
			mu.Lock()
			defer mu.Unlock()
			if sup, ok := supervisors[orgID]; ok {
				return sup
			}
			sup := newFakeSupervisor()
			supervisors[orgID] = sup
			return sup
		},
		nil,
	)

	spawnReq := &protocol.SpawnRequest{
		RequestID:      "req-spawn-org-stop",
		OrganizationID: "org-stop",
		GuildID:        "guild-stop",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-stop",
			ClassName: "test.Agent",
		},
	}
	handler.handleSpawn(ctx, spawnReq)

	_, err = rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-spawn-org-stop").Result()
	require.NoError(t, err)

	stopReq := &protocol.StopRequest{
		RequestID: "req-stop-org-stop",
		GuildID:   "guild-stop",
		AgentID:   "agent-stop",
	}
	handler.handleStop(ctx, stopReq)

	stopResp, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:req-stop-org-stop").Result()
	require.NoError(t, err)
	var stopBody protocol.StopResponse
	require.NoError(t, json.Unmarshal([]byte(stopResp[1]), &stopBody))
	require.True(t, stopBody.Success)

	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, supervisors["org-stop"].stopped, "guild-stop::agent-stop")
}

func TestHandler_OrganizationResolutionPrecedence(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	reg := loadTestRegistry(t, `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`)

	dbStore, err := store.NewGormStore(store.DriverSQLite, filepath.Join(t.TempDir(), "org-precedence.db"))
	require.NoError(t, err)
	defer dbStore.Close()
	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             "guild-store",
		Name:           "Guild Store",
		Description:    "Guild Store",
		OrganizationID: "org-store",
		BackendConfig:  store.JSONB{},
	}))

	supervisors := make(map[string]*fakeSupervisor)
	var mu sync.Mutex
	handler := NewControlQueueHandlerWithFactory(
		rdb,
		reg,
		secrets.NewEnvSecretProvider(),
		func(orgID string) supervisor.AgentSupervisor {
			mu.Lock()
			defer mu.Unlock()
			if sup, ok := supervisors[orgID]; ok {
				return sup
			}
			sup := newFakeSupervisor()
			supervisors[orgID] = sup
			return sup
		},
		dbStore,
	)

	// 1) request.organization_id wins over all fallbacks
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID:      "req-org-precedence-1",
		OrganizationID: "org-request",
		GuildID:        "guild-precedence-1",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-precedence-1",
			ClassName: "test.Agent",
			Properties: map[string]interface{}{
				"organization_id": "org-agent",
			},
		},
		ClientProperties: protocol.JSONB{"organization_id": "org-client"},
	})

	// 2) client_properties.organization_id when request org is empty
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-org-precedence-2",
		GuildID:   "guild-precedence-2",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-precedence-2",
			ClassName: "test.Agent",
			Properties: map[string]interface{}{
				"organization_id": "org-agent",
			},
		},
		ClientProperties: protocol.JSONB{"organization_id": "org-client"},
	})

	// 3) agent.properties.organization_id when request + client org are empty
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-org-precedence-3",
		GuildID:   "guild-precedence-3",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-precedence-3",
			ClassName: "test.Agent",
			Properties: map[string]interface{}{
				"organization_id": "org-agent",
			},
		},
	})

	// 4) guild store organization_id when payload has no org
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-org-precedence-4",
		GuildID:   "guild-store",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-precedence-4",
			ClassName: "test.Agent",
		},
	})

	// 5) default organization fallback
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-org-precedence-5",
		GuildID:   "guild-precedence-5",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-precedence-5",
			ClassName: "test.Agent",
		},
	})

	for _, reqID := range []string{
		"req-org-precedence-1",
		"req-org-precedence-2",
		"req-org-precedence-3",
		"req-org-precedence-4",
		"req-org-precedence-5",
	} {
		res, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:"+reqID).Result()
		require.NoError(t, err)
		var body protocol.SpawnResponse
		require.NoError(t, json.Unmarshal([]byte(res[1]), &body))
		require.True(t, body.Success, "spawn failed for %s", reqID)
	}

	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, supervisors, "org-request")
	require.Contains(t, supervisors, "org-client")
	require.Contains(t, supervisors, "org-agent")
	require.Contains(t, supervisors, "org-store")
	require.Contains(t, supervisors, defaultOrganizationID)
}

func TestHandler_Integration_ProcessSupervisorsScopedByOrganization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires /bin/sleep")
	}

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	reg := loadTestRegistry(t, `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/sleep"
    args: ["5"]
`)

	handler := NewControlQueueHandlerWithFactory(
		rdb,
		reg,
		secrets.NewEnvSecretProvider(),
		func(orgID string) supervisor.AgentSupervisor {
			_ = orgID
			return supervisor.NewDispatchingSupervisor(
				"process",
				supervisor.NewProcessSupervisor(rdb, supervisor.WithWorkDirBase(t.TempDir())),
				nil,
				nil,
			)
		},
		nil,
	)
	require.NoError(t, handler.Start(ctx))
	defer handler.Stop()

	// Same guild + same agent id, different orgs: should both launch because supervisors are org-scoped.
	spawnA := &protocol.SpawnRequest{
		RequestID:      "req-org-int-spawn-a",
		OrganizationID: "org-int-a",
		GuildID:        "guild-int",
		AgentSpec: protocol.AgentSpec{
			ID:        "same-agent",
			ClassName: "test.Agent",
		},
	}
	spawnB := &protocol.SpawnRequest{
		RequestID:      "req-org-int-spawn-b",
		OrganizationID: "org-int-b",
		GuildID:        "guild-int",
		AgentSpec: protocol.AgentSpec{
			ID:        "same-agent",
			ClassName: "test.Agent",
		},
	}

	require.NoError(t, pushControlRequest(ctx, rdb, "spawn", spawnA))
	require.NoError(t, pushControlRequest(ctx, rdb, "spawn", spawnB))

	for _, reqID := range []string{"req-org-int-spawn-a", "req-org-int-spawn-b"} {
		res, err := rdb.BRPop(ctx, 5*time.Second, "forge:control:response:"+reqID).Result()
		require.NoError(t, err)
		var body protocol.SpawnResponse
		require.NoError(t, json.Unmarshal([]byte(res[1]), &body))
		require.True(t, body.Success)
	}

	stopA := &protocol.StopRequest{
		RequestID:      "req-org-int-stop-a",
		OrganizationID: "org-int-a",
		GuildID:        "guild-int",
		AgentID:        "same-agent",
	}
	stopB := &protocol.StopRequest{
		RequestID:      "req-org-int-stop-b",
		OrganizationID: "org-int-b",
		GuildID:        "guild-int",
		AgentID:        "same-agent",
	}

	require.NoError(t, pushControlRequest(ctx, rdb, "stop", stopA))
	require.NoError(t, pushControlRequest(ctx, rdb, "stop", stopB))

	for _, reqID := range []string{"req-org-int-stop-a", "req-org-int-stop-b"} {
		res, err := rdb.BRPop(ctx, 5*time.Second, "forge:control:response:"+reqID).Result()
		require.NoError(t, err)
		var body protocol.StopResponse
		require.NoError(t, json.Unmarshal([]byte(res[1]), &body))
		require.True(t, body.Success)
	}
}

func loadTestRegistry(t *testing.T, yamlContent string) *registry.Registry {
	t.Helper()
	tmpfile := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(tmpfile, []byte(yamlContent), 0644))
	reg, err := registry.Load(tmpfile)
	require.NoError(t, err)
	return reg
}

func pushControlRequest(ctx context.Context, rdb *redis.Client, command string, payload interface{}) error {
	wrapper := map[string]interface{}{
		"command": command,
		"payload": payload,
	}
	wb, err := json.Marshal(wrapper)
	if err != nil {
		return err
	}
	return rdb.LPush(ctx, ControlQueueRequestKey, wb).Err()
}
