package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

// TestHandler_SpawnFallbackGuildSpecIsCoreValid guards the last-resort guild spec stub that
// handleSpawn synthesizes when a guild is in neither the store nor the spawn payload.
//
// That stub is serialized into FORGE_GUILD_JSON and validated by rustic-ai core, whose
// GuildSpec requires a non-empty name and description and a list-typed `agents`. A stub
// violating those kills the agent process with a pydantic ValidationError before any agent
// code runs. It surfaces only as a crash-looping agent, never as a Go test failure — which
// is why it went unnoticed for so long behind a Python-side lenient-parsing workaround.
//
// The assertions run against the actual FORGE_GUILD_JSON handed to the supervisor, not a
// re-derivation of it, so they fail if any step in the chain reintroduces an invalid spec.
func TestHandler_SpawnFallbackGuildSpecIsCoreValid(t *testing.T) {
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

	// No store, and no guild_spec in the payload: forces the synthesized stub.
	sup := newCapturingSupervisor()
	handler := NewControlQueueHandlerWithFactory(
		NewRedisControlTransport(rdb),
		reg,
		secrets.NewEnvSecretProvider(),
		func(string) supervisor.AgentSupervisor { return sup },
		nil,
	)

	cases := []struct{ name, guildID string }{
		{"normal_guild_id", "guild-no-store"},
		{"empty_guild_id", ""},
		{"over_long_guild_id", strings.Repeat("g", 200)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqID := "req-fallback-" + tc.name
			agentID := "agent-" + tc.name
			handler.handleSpawn(ctx, &protocol.SpawnRequest{
				RequestID: reqID,
				GuildID:   tc.guildID,
				AgentSpec: protocol.AgentSpec{ID: agentID, ClassName: "test.Agent"},
			})

			res, err := rdb.BRPop(ctx, 2*time.Second, "forge:control:response:"+reqID).Result()
			require.NoError(t, err)
			var body protocol.SpawnResponse
			require.NoError(t, json.Unmarshal([]byte(res[1]), &body))
			require.True(t, body.Success, "spawn failed for %s", reqID)

			raw := sup.guildJSON(t, tc.guildID, agentID)

			// Decode into a map so we see exactly what core sees, including nulls that a
			// typed decode would quietly turn into an empty slice.
			var decoded map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(raw), &decoded))

			name, _ := decoded["name"].(string)
			require.GreaterOrEqual(t, len(name), 1, "core requires name min_length=1; got %q", name)
			require.LessOrEqual(t, len(name), 64, "core requires name max_length=64; got %q", name)

			desc, _ := decoded["description"].(string)
			require.GreaterOrEqual(t, len(desc), 1, "core requires description min_length=1; got %q", desc)

			agents, present := decoded["agents"]
			require.True(t, present, "`agents` must be present")
			require.NotNil(t, agents, "core requires `agents` to be a list, not null")
			_, isList := agents.([]interface{})
			require.True(t, isList, "`agents` must serialize as a JSON array, got %T", agents)
		})
	}
}
