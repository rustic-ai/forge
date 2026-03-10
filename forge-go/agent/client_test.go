package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestStartClient_ConsumesNodeQueueAndLaunches(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /nodes/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /nodes/{node_id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /nodes/{node_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	regPath := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(regPath, []byte(regYaml), 0644))
	t.Setenv("FORGE_AGENT_REGISTRY", regPath)

	nodeID := "client-node-1"
	cfg := &ClientConfig{
		ServerURL:   ts.URL,
		RedisURL:    mr.Addr(),
		NodeID:      nodeID,
		CPUs:        2,
		Memory:      1024,
		GPUs:        0,
		MetricsAddr: "127.0.0.1:0",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartClient(ctx, cfg)
	}()

	time.Sleep(300 * time.Millisecond)

	req := &protocol.SpawnRequest{
		RequestID: "spawn-client-test-1",
		GuildID:   "guild-client-test",
		AgentSpec: protocol.AgentSpec{
			ID:        "agent-client-test",
			ClassName: "test.Agent",
		},
	}
	wrapper := map[string]interface{}{
		"command": "spawn",
		"payload": req,
	}
	wb, _ := json.Marshal(wrapper)
	nodeQueue := "forge:control:node:" + nodeID
	require.NoError(t, rdb.LPush(context.Background(), nodeQueue, wb).Err())

	respKey := "forge:control:response:spawn-client-test-1"
	res, err := rdb.BRPop(context.Background(), 5*time.Second, respKey).Result()
	require.NoError(t, err, "client did not process node queue spawn request")

	var spawnResp protocol.SpawnResponse
	require.NoError(t, json.Unmarshal([]byte(res[1]), &spawnResp))
	assert.True(t, spawnResp.Success)

	queueLen, err := rdb.LLen(context.Background(), nodeQueue).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), queueLen)

	stopReq := &protocol.StopRequest{
		RequestID: "stop-client-test-1",
		GuildID:   "guild-client-test",
		AgentID:   "agent-client-test",
	}
	stopWrapper := map[string]interface{}{
		"command": "stop",
		"payload": stopReq,
	}
	swb, _ := json.Marshal(stopWrapper)
	require.NoError(t, rdb.LPush(context.Background(), nodeQueue, swb).Err())

	stopRespKey := "forge:control:response:stop-client-test-1"
	stopRes, err := rdb.BRPop(context.Background(), 5*time.Second, stopRespKey).Result()
	require.NoError(t, err, "client did not process node queue stop request")

	var stopResp protocol.StopResponse
	require.NoError(t, json.Unmarshal([]byte(stopRes[1]), &stopResp))
	assert.True(t, stopResp.Success)

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop after cancel")
	}

	// prevent accidental import pruning checks for control key constants in this integration test file
	_ = control.ControlQueueRequestKey
}

func TestStartClient_ReRegistersWhenHeartbeatNodeMissing(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	var registerCount atomic.Int32
	var missingNode atomic.Bool
	missingNode.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /nodes/register", func(w http.ResponseWriter, r *http.Request) {
		registerCount.Add(1)
		missingNode.Store(false)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /nodes/{node_id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		if missingNode.Load() {
			http.Error(w, "unknown node", http.StatusNotFound)
			return
		}
		// Simulate server restart dropping node state after first heartbeat.
		missingNode.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /nodes/{node_id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	regPath := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(regPath, []byte(regYaml), 0644))
	t.Setenv("FORGE_AGENT_REGISTRY", regPath)

	cfg := &ClientConfig{
		ServerURL:   ts.URL,
		RedisURL:    mr.Addr(),
		NodeID:      "client-node-reregister",
		CPUs:        2,
		Memory:      1024,
		GPUs:        0,
		MetricsAddr: "127.0.0.1:0",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartClient(ctx, cfg)
	}()

	require.Eventually(t, func() bool {
		return registerCount.Load() >= 2
	}, 12*time.Second, 250*time.Millisecond, "expected client to re-register after heartbeat 404")

	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop after cancel")
	}
}

func TestStartClient_DeregistersNodeOnShutdown(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	var deregisterCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("POST /nodes/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /nodes/{node_id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /nodes/{node_id}", func(w http.ResponseWriter, r *http.Request) {
		deregisterCount.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	regYaml := `
entries:
  - id: TestAgent
    class_name: "test.Agent"
    runtime: binary
    executable: "/bin/echo"
`
	regPath := filepath.Join(t.TempDir(), "reg.yaml")
	require.NoError(t, os.WriteFile(regPath, []byte(regYaml), 0644))
	t.Setenv("FORGE_AGENT_REGISTRY", regPath)

	cfg := &ClientConfig{
		ServerURL:   ts.URL,
		RedisURL:    mr.Addr(),
		NodeID:      "client-node-deregister",
		CPUs:        2,
		Memory:      1024,
		GPUs:        0,
		MetricsAddr: "127.0.0.1:0",
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- StartClient(ctx, cfg)
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("client did not stop after cancel")
	}

	assert.Equal(t, int32(1), deregisterCount.Load())
}
