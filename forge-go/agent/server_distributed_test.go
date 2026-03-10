package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
)

func TestStartServer_EnrichesMessagingConfigForNodeDispatch(t *testing.T) {
	s, err := miniredis.Run()
	require.NoError(t, err)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()

	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalPlacementMap = scheduler.NewPlacementMap()
	scheduler.GlobalScheduler = scheduler.NewScheduler(scheduler.GlobalNodeRegistry)

	port := getTestPort(9450, 0)
	cfg := &ServerConfig{
		DatabaseURL:        "file:testserverdispatch?mode=memory&cache=shared",
		RedisURL:           s.Addr(),
		ListenAddress:      port,
		LeaderElectionMode: "redis",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = StartServer(ctx, cfg)
	}()

	baseURL := "http://" + port
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("server did not become ready")
		}
		resp, err := http.Get(baseURL + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	guildReq := map[string]interface{}{
		"org_id": "org-1",
		"spec": map[string]interface{}{
			"id":          "guild-dispatch",
			"name":        "guild-dispatch",
			"description": "guild dispatch test",
			"agents": []map[string]interface{}{
				{
					"id":          "echo-agent",
					"name":        "Echo Agent",
					"description": "Echo",
					"class_name":  "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				},
			},
		},
	}
	body, _ := json.Marshal(guildReq)
	resp, err := http.Post(baseURL+"/api/guilds", "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var launchResp map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&launchResp))
	resp.Body.Close()
	createdGuildID, ok := launchResp["id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, createdGuildID)

	nodeReq := map[string]interface{}{
		"node_id": "node-dispatch-1",
		"capacity": map[string]interface{}{
			"cpus":   4,
			"memory": 4096,
			"gpus":   0,
		},
	}
	nodeBody, _ := json.Marshal(nodeReq)
	nResp, err := http.Post(baseURL+"/nodes/register", "application/json", bytes.NewBuffer(nodeBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, nResp.StatusCode)
	nResp.Body.Close()

	spawnReq := protocol.SpawnRequest{
		RequestID:      "dispatch-spawn-1",
		OrganizationID: "org-1",
		GuildID:        createdGuildID,
		AgentSpec: protocol.AgentSpec{
			ID:        "echo-agent",
			Name:      "Echo Agent",
			ClassName: "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
		},
		ClientProperties: protocol.JSONB{
			"organization_id": "org-1",
		},
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"command": "spawn",
		"payload": spawnReq,
	})
	require.NoError(t, rdb.LPush(context.Background(), control.ControlQueueRequestKey, payload).Err())

	foundEcho := false
	for i := 0; i < 3; i++ {
		res, err := rdb.BRPop(context.Background(), 5*time.Second, "forge:control:node:node-dispatch-1").Result()
		require.NoError(t, err)

		var wrapper control.ControlMessageWrapper
		require.NoError(t, json.Unmarshal([]byte(res[1]), &wrapper))
		require.Equal(t, "spawn", wrapper.Command)

		var dispatched protocol.SpawnRequest
		require.NoError(t, json.Unmarshal(wrapper.Payload, &dispatched))
		if dispatched.AgentSpec.ID == "echo-agent" {
			foundEcho = true
			if assert.NotNil(t, dispatched.MessagingConfig) {
				assert.NotEmpty(t, dispatched.MessagingConfig.BackendModule)
				assert.NotEmpty(t, dispatched.MessagingConfig.BackendClass)
			}
			assert.Equal(t, "org-1", dispatched.OrganizationID, "expected spawn dispatch to preserve organization from request")
			gsRaw, ok := dispatched.ClientProperties["guild_spec"]
			require.True(t, ok, "expected dispatched spawn payload to include guild_spec")
			gsMap, ok := gsRaw.(map[string]interface{})
			require.True(t, ok, "expected guild_spec to be an object")
			assert.Equal(t, "guild-dispatch", gsMap["name"])
			orgRaw, ok := dispatched.ClientProperties["organization_id"]
			require.True(t, ok, "expected dispatched spawn payload to preserve organization_id in client_properties")
			org, ok := orgRaw.(string)
			require.True(t, ok, "expected client_properties.organization_id to be a string")
			assert.Equal(t, "org-1", org)
			agents, ok := gsMap["agents"].([]interface{})
			require.True(t, ok, "expected guild_spec.agents to be an array")
			assert.NotEmpty(t, agents, "expected guild_spec.agents to contain at least one agent")
			break
		}
	}
	require.True(t, foundEcho, "did not observe dispatched echo-agent payload on node queue")

	var (
		placement      scheduler.AgentPlacement
		foundPlacement bool
	)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		placement, foundPlacement = scheduler.GlobalPlacementMap.Find(createdGuildID, "echo-agent")
		if foundPlacement {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, foundPlacement)
	assert.Equal(t, "node-dispatch-1", placement.NodeID)
}

func TestStartServer_WithClient_RegistersNodeWithEmbeddedRedis(t *testing.T) {
	listenAddr := reserveLocalAddr(t)
	redisAddr := reserveLocalAddr(t)

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	regPath := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(regPath, []byte(regYaml), 0o644))
	t.Setenv("FORGE_AGENT_REGISTRY", regPath)

	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalPlacementMap = scheduler.NewPlacementMap()
	scheduler.GlobalScheduler = scheduler.NewScheduler(scheduler.GlobalNodeRegistry)

	cfg := &ServerConfig{
		DatabaseURL:        "file:testserverwithclient?mode=memory&cache=shared",
		RedisURL:           "",
		EmbeddedRedisAddr:  redisAddr,
		ListenAddress:      listenAddr,
		LeaderElectionMode: "redis",
		WithClient:         true,
		ClientNodeID:       "embedded-node-1",
		ClientMetricsAddr:  "127.0.0.1:0",
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, cfg)
	}()

	baseURL := "http://" + listenAddr
	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 8*time.Second, 150*time.Millisecond, "server did not become ready")

	require.Eventually(t, func() bool {
		resp, err := http.Get(baseURL + "/nodes")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var nodes []map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&nodes); err != nil {
			return false
		}
		for _, node := range nodes {
			if id, _ := node["node_id"].(string); id == "embedded-node-1" {
				return true
			}
		}
		return false
	}, 10*time.Second, 200*time.Millisecond, "embedded client node did not register")

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()
	require.NoError(t, rdb.Ping(context.Background()).Err(), "embedded redis should be reachable at configured address")

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down after cancel")
	}

	assert.False(t, scheduler.GlobalNodeRegistry.IsHealthy("embedded-node-1"), "embedded client node should deregister during shutdown")
}

func TestStartServer_FailsWhenEmbeddedRedisAddressOccupied(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	cfg := &ServerConfig{
		DatabaseURL:        "file:testserveroccupiedredis?mode=memory&cache=shared",
		RedisURL:           "",
		EmbeddedRedisAddr:  ln.Addr().String(),
		ListenAddress:      reserveLocalAddr(t),
		LeaderElectionMode: "redis",
	}

	err = StartServer(context.Background(), cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to start miniredis")
}

func reserveLocalAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}
