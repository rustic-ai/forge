package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/api"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/filesystem"
	score "github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

func TestLevel1_MultiNodeSchedulingIntegration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. Stand up embedded Redis
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	// Reset Global states for fresh test
	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalPlacementMap = scheduler.NewPlacementMap()
	scheduler.GlobalScheduler = scheduler.NewScheduler(scheduler.GlobalNodeRegistry)

	// 2. Wrap the API Server specifically for Node Endpoints so we can test HTTP flow
	db, _ := score.NewGormStore("sqlite", ":memory:")
	msgClient := messaging.NewClient(rdb)
	resolver := filesystem.NewFileSystemResolver("/tmp/dummy")
	fs := filesystem.NewLocalFileStore(resolver)

	server := api.NewServer(db, supervisor.NewRedisAgentStatusStore(rdb), control.NewRedisControlTransport(rdb), msgClient, fs, ":0")
	_ = server

	// Create a test multiplexer and bind node endpoints manually like in StartServer
	mux := http.NewServeMux()
	mux.HandleFunc("POST /nodes/register", api.RegisterNodeHandler)
	mux.HandleFunc("POST /nodes/{node_id}/heartbeat", api.NodeHeartbeatHandler)
	mux.HandleFunc("GET /nodes", api.ListNodesHandler)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 3. Register Node A (Small) and Node B (Large) via HTTP
	registerNode := func(id string, cpus, mem int) {
		reqPayload := struct {
			NodeID   string                     `json:"node_id"`
			Capacity scheduler.ResourceCapacity `json:"capacity"`
		}{
			NodeID:   id,
			Capacity: scheduler.ResourceCapacity{CPUs: cpus, Memory: mem},
		}
		body, _ := json.Marshal(reqPayload)
		resp, err := http.Post(fmt.Sprintf("%s/nodes/register", ts.URL), "application/json", bytes.NewBuffer(body))
		require.NoError(t, err)
		assert.Equal(t, http.StatusCreated, resp.StatusCode)
		_ = resp.Body.Close()
	}

	registerNode("node-a", 2, 2048)
	registerNode("node-b", 8, 16384)

	// Ensure they registered
	time.Sleep(100 * time.Millisecond) // Let HTTP resolve
	nodes := scheduler.GlobalNodeRegistry.ListHealthy()
	require.Len(t, nodes, 2)

	// 4. Start the Global Control Queue Listener mapping spawns to schedules
	queueListener := control.NewControlQueueListener(control.NewRedisControlTransport(rdb))
	queueListener.OnSpawn = func(ctx context.Context, req *protocol.SpawnRequest) {
		nodeID, err := scheduler.GlobalScheduler.Schedule(req.AgentSpec)
		require.NoError(t, err)

		payloadBytes, _ := json.Marshal(req)
		scheduler.GlobalPlacementMap.Place(req.GuildID, req.AgentSpec.ID, nodeID, payloadBytes)

		wrapper := control.ControlMessageWrapper{Command: "spawn"}
		wrapper.Payload = payloadBytes
		wrapperBytes, _ := json.Marshal(wrapper)
		rdb.LPush(ctx, "forge:control:node:"+nodeID, wrapperBytes)
	}

	go queueListener.Start(ctx)
	defer queueListener.Stop()

	// 5. Submit a large SpawnRequest payload asking for 6 CPUs
	// This should mathematically FORWARD to node-b implicitly and appear in node-b's queue
	numCPUs := float64(6)
	spawnReq := protocol.SpawnRequest{
		GuildID: "test-guild",
		AgentSpec: protocol.AgentSpec{
			ID: "heavy-agent",
			Resources: protocol.ResourceSpec{
				NumCPUs: &numCPUs,
			},
		},
	}

	err = protocol.PushSpawnRequest(ctx, control.NewRedisControlTransport(rdb), spawnReq)
	require.NoError(t, err)

	// Wait a moment for BRPop -> Schedule -> LPush loop to resolve
	time.Sleep(1 * time.Second)

	// 6. Inspect forge:control:node:node-b and ensure the payload is waiting for node-b!
	res, err := rdb.LPop(ctx, "forge:control:node:node-b").Result()
	require.NoError(t, err, "Node B should have received the scheduled payload")

	var parsed control.ControlMessageWrapper
	err = json.Unmarshal([]byte(res), &parsed)
	require.NoError(t, err)
	assert.Equal(t, "spawn", parsed.Command)

	var extractedReq protocol.SpawnRequest
	err = json.Unmarshal(parsed.Payload, &extractedReq)
	require.NoError(t, err)
	assert.Equal(t, "heavy-agent", extractedReq.AgentSpec.ID)

	// Node A should be empty
	_, err = rdb.LPop(ctx, "forge:control:node:node-a").Result()
	assert.ErrorIs(t, err, redis.Nil, "Node A should NOT have received the payload")

	// Validate placement tracker map
	placements := scheduler.GlobalPlacementMap.AgentsOnNode("node-b")
	require.Len(t, placements, 1)
	assert.Equal(t, "heavy-agent", placements[0].AgentID)
}
