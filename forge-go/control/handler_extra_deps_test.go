package control

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

// capturingSupervisor records the AgentSpec each launch was given, so tests can assert on
// what actually reached the supervisor rather than on the request that arrived.
type capturingSupervisor struct {
	mu       sync.Mutex
	launched map[string]protocol.AgentSpec
	envs     map[string][]string
}

func newCapturingSupervisor() *capturingSupervisor {
	return &capturingSupervisor{
		launched: make(map[string]protocol.AgentSpec),
		envs:     make(map[string][]string),
	}
}

func (c *capturingSupervisor) Launch(_ context.Context, guildID string, agentSpec *protocol.AgentSpec, _ *registry.Registry, env []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := guildID + "::" + agentSpec.ID
	c.launched[key] = *agentSpec
	c.envs[key] = append([]string(nil), env...)
	return nil
}

func (c *capturingSupervisor) Stop(_ context.Context, _, _ string) error { return nil }

func (c *capturingSupervisor) Status(_ context.Context, guildID, agentID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.launched[guildID+"::"+agentID]; ok {
		return "running", nil
	}
	return "unknown", nil
}

func (c *capturingSupervisor) StopAll(_ context.Context) error { return nil }

// guildJSON returns the FORGE_GUILD_JSON the agent process would have been started with —
// the exact bytes rustic-ai core validates.
func (c *capturingSupervisor) guildJSON(t *testing.T, guildID, agentID string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	env, ok := c.envs[guildID+"::"+agentID]
	require.True(t, ok, "agent %s was never launched", agentID)
	for _, kv := range env {
		if v, found := strings.CutPrefix(kv, "FORGE_GUILD_JSON="); found {
			return v
		}
	}
	t.Fatalf("FORGE_GUILD_JSON not present in env for %s", agentID)
	return ""
}

func (c *capturingSupervisor) spec(t *testing.T, guildID, agentID string) protocol.AgentSpec {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	spec, ok := c.launched[guildID+"::"+agentID]
	require.True(t, ok, "agent %s was never launched", agentID)
	return spec
}

// TestHandler_SpawnRestoresForgeExtraDeps guards the seam that makes per-agent package
// requirements work at all.
//
// AgentSpec.ForgeExtraDeps is a Forge extension that rustic-ai core's AgentSpec does not
// model. Core ignores unknown keys rather than rejecting them, so when the Python guild
// manager re-serializes a spec to build a spawn request the field is silently dropped —
// no error, no warning. Every agent except the manager is spawned that way, so without
// this restore the field would reach the launcher empty for exactly the agents that need
// it. The guild store is the authoritative copy.
func TestHandler_SpawnRestoresForgeExtraDeps(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	reg := loadTestRegistry(t, `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`)

	dbStore, err := store.NewGormStore(store.DriverSQLite, filepath.Join(t.TempDir(), "extra-deps.db"))
	require.NoError(t, err)
	defer func() { _ = dbStore.Close() }()

	guildID := "guild-deps"
	require.NoError(t, dbStore.CreateGuild(&store.GuildModel{
		ID:             guildID,
		Name:           "Deps Guild",
		Description:    "Guild with a plugin-owning agent",
		OrganizationID: "org-deps",
		BackendConfig:  store.JSONB{},
		Agents: []store.AgentModel{
			{
				ID:             "analyst",
				Name:           "Analyst",
				Description:    "Owns the pandas toolset",
				ClassName:      "test.Agent",
				ForgeExtraDeps: store.JSONBStringList{"rusticai-pandas-analyst"},
				Status:         store.AgentStatusPendingLaunch,
			},
			{
				ID:          "plain",
				Name:        "Plain",
				Description: "Needs no extra packages",
				ClassName:   "test.Agent",
				Status:      store.AgentStatusPendingLaunch,
			},
		},
	}))

	sup := newCapturingSupervisor()
	handler := NewControlQueueHandlerWithFactory(
		NewRedisControlTransport(rdb),
		reg,
		secrets.NewEnvSecretProvider(),
		func(string) supervisor.AgentSupervisor { return sup },
		dbStore,
	)

	requireSpawned := func(t *testing.T, reqID string) {
		t.Helper()
		res, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:"+reqID).Result()
		require.NoError(t, err)
		var body protocol.SpawnResponse
		require.NoError(t, json.Unmarshal([]byte(res[1]), &body))
		require.True(t, body.Success, "spawn failed for %s", reqID)
	}

	// The payload deliberately carries no ForgeExtraDeps: this is what a spawn request
	// looks like after a round-trip through the Python guild manager.
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-deps-analyst",
		GuildID:   guildID,
		AgentSpec: protocol.AgentSpec{ID: "analyst", ClassName: "test.Agent"},
	})
	requireSpawned(t, "req-deps-analyst")
	require.Equal(t, []string{"rusticai-pandas-analyst"},
		sup.spec(t, guildID, "analyst").ForgeExtraDeps,
		"deps should have been restored from the guild store")

	// Sibling agents must not inherit them — per-agent scoping is the whole point.
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-deps-plain",
		GuildID:   guildID,
		AgentSpec: protocol.AgentSpec{ID: "plain", ClassName: "test.Agent"},
	})
	requireSpawned(t, "req-deps-plain")
	require.Empty(t, sup.spec(t, guildID, "plain").ForgeExtraDeps,
		"sibling agent must not inherit another agent's packages")

	// An agent absent from the store (e.g. created at runtime by the manager) is left
	// untouched and still launches.
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-deps-unknown",
		GuildID:   guildID,
		AgentSpec: protocol.AgentSpec{ID: "runtime-created", ClassName: "test.Agent"},
	})
	requireSpawned(t, "req-deps-unknown")
	require.Empty(t, sup.spec(t, guildID, "runtime-created").ForgeExtraDeps)

	// A payload that does carry deps wins over the store copy.
	handler.handleSpawn(ctx, &protocol.SpawnRequest{
		RequestID: "req-deps-payload",
		GuildID:   guildID,
		AgentSpec: protocol.AgentSpec{
			ID:             "analyst-2",
			ClassName:      "test.Agent",
			ForgeExtraDeps: []string{"rusticai-from-payload"},
		},
	})
	requireSpawned(t, "req-deps-payload")
	require.Equal(t, []string{"rusticai-from-payload"},
		sup.spec(t, guildID, "analyst-2").ForgeExtraDeps)
}
