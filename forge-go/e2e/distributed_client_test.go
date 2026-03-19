package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/agent"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/rustic-ai/forge/forge-go/testutil/probe"
)

// TestE2E_StrictDistributedDockerClient validates full distributed flow:
// YAML guild spec -> API bootstrap -> server scheduler -> client node queue -> docker supervisor -> message roundtrip.
func TestE2E_StrictDistributedDockerClient(t *testing.T) {
	ds, err := supervisor.NewDockerSupervisor(nil)
	if err != nil || !ds.Available() {
		t.Skipf("Docker not available for strict distributed E2E test: %v", err)
	}

	pwd, err := os.Getwd()
	require.NoError(t, err)
	binPath := requireE2EForgeBin(t)

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalPlacementMap = scheduler.NewPlacementMap()
	scheduler.GlobalScheduler = scheduler.NewScheduler(scheduler.GlobalNodeRegistry)

	guildID := "guild-dist-docker"
	yamlContent := `
id: ` + guildID + `
name: Strict Distributed Docker Echo
version: 1.0.0
description: Validates node queue consumption in distributed client process
agents:
  - id: echo-agent
    name: Echo Agent
    description: Echoes incoming messages
    class_name: rustic_ai.core.agents.testutils.echo_agent.EchoAgent
    additional_topics:
      - echo_topic
    listen_to_default_topic: false
`
	specPath := filepath.Join(t.TempDir(), guildID+".yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(yamlContent), 0o644))

	parsedSpec, _, err := guild.ParseFile(specPath)
	require.NoError(t, err)

	serverHTTPAddr, err := reserveLocalAddr()
	require.NoError(t, err)
	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer cancelServer()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "forge_dist_server.db")

	srvCfg := &agent.ServerConfig{
		DatabaseURL:        "sqlite:///" + dbPath,
		RedisURL:           mr.Addr(),
		ListenAddress:      serverHTTPAddr,
		LeaderElectionMode: "redis",
	}
	go func() {
		_ = agent.StartServer(serverCtx, srvCfg)
	}()

	baseURL := "http://" + serverHTTPAddr
	require.NoError(t, waitFor(10*time.Second, 100*time.Millisecond, func() error {
		resp, err := http.Get(baseURL + "/readyz")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("readyz returned %d", resp.StatusCode)
		}
		return nil
	}), "server did not become ready")

	var redisHost, redisPort string
	addr := mr.Addr()
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			redisHost = addr[:i]
			redisPort = addr[i+1:]
			break
		}
	}
	if redisHost == "" {
		redisHost = "localhost"
	}

	forgePythonPath, _ := filepath.Abs(filepath.Join(pwd, "..", "..", "forge-python"))
	registryPath, _ := filepath.Abs(filepath.Join(pwd, "..", "conf", "forge-agent-registry.yaml"))

	clientNodeID := "test-worker-node-1"
	clientCmd := exec.Command(binPath, "client",
		"--server", baseURL,
		"--redis", mr.Addr(),
		"--node-id", clientNodeID,
		"--cpus", "4",
		"--memory", "4096",
		"--default-supervisor", "docker",
		"--metrics-addr", "127.0.0.1:0",
	)

	clientStdout := &bytes.Buffer{}
	clientStderr := &bytes.Buffer{}
	clientCmd.Stdout = io.MultiWriter(os.Stdout, clientStdout)
	clientCmd.Stderr = io.MultiWriter(os.Stderr, clientStderr)
	clientCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	clientCmd.Env = append(os.Environ(),
		"FORGE_AGENT_REGISTRY="+registryPath,
		"FORGE_PYTHON_PKG="+forgePythonPath,
		"PYTHONUNBUFFERED=1",
		"REDIS_HOST="+redisHost,
		"REDIS_PORT="+redisPort,
		"FORGE_INJECT_FS="+forgePythonPath+":rw,"+dbDir+":rw",
		"FORGE_INJECT_NET=host",
	)

	err = clientCmd.Start()
	require.NoError(t, err, "Failed to start forge client process")
	clientPID := clientCmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-clientPID, syscall.SIGKILL)
	}()

	require.NoError(t, waitFor(10*time.Second, 150*time.Millisecond, func() error {
		resp, err := http.Get(baseURL + "/nodes")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("nodes returned %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), clientNodeID) {
			return fmt.Errorf("node %s not yet registered", clientNodeID)
		}
		return nil
	}), "client node did not register with server")

	launchBody, _ := json.Marshal(map[string]interface{}{
		"spec":   parsedSpec,
		"org_id": "e2e-org",
	})
	launchResp, err := http.Post(baseURL+"/api/guilds", "application/json", bytes.NewBuffer(launchBody))
	require.NoError(t, err)
	defer func() { _ = launchResp.Body.Close() }()
	require.Equal(t, http.StatusCreated, launchResp.StatusCode)

	require.NoError(t, waitFor(30*time.Second, 300*time.Millisecond, func() error {
		statusKey := fmt.Sprintf("forge:agent:status:%s:%s", guildID, "echo-agent")
		val, err := rdb.Get(context.Background(), statusKey).Result()
		if err != nil {
			return err
		}
		var status map[string]interface{}
		if err := json.Unmarshal([]byte(val), &status); err != nil {
			return err
		}
		if state, _ := status["state"].(string); state != "running" {
			return fmt.Errorf("agent state is %q", state)
		}
		return nil
	}), "echo agent did not reach running state; client stderr: %s", clientStderr.String())

	probeAgent := probe.NewProbeAgent(rdb)
	ctx, cancelProbe := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelProbe()

	testPayload := map[string]interface{}{
		"message": "Hello from explicitly distributed Docker!",
	}
	msg := probe.DefaultMessage(1001, "probe-agent", testPayload)
	topicIn := guildID + ":echo_topic"
	msg.TopicPublishedTo = topicIn

	donePing := make(chan struct{})
	go func() {
		counter := int64(1001)
		for {
			select {
			case <-donePing:
				return
			case <-time.After(2 * time.Second):
				counter++
				msg.ID = counter
				_ = probeAgent.Publish(ctx, guildID, topicIn, msg)
			}
		}
	}()

	topicOut := guildID + ":default_topic"
	respMsg, err := probeAgent.WaitForMessage(ctx, topicOut, 30*time.Second)
	close(donePing)
	require.NoError(t, err, "Distributed EchoAgent under Docker supervisor did not respond. stderr: %s", clientStderr.String())

	assert.Equal(t, "Hello from explicitly distributed Docker!", respMsg.Payload["message"])
	if assert.NotNil(t, respMsg.Sender.Name) {
		assert.Equal(t, "Echo Agent", *respMsg.Sender.Name)
	}

	// Verify node queue drains (client consumed dispatches)
	nodeQueue := "forge:control:node:" + clientNodeID
	require.NoError(t, waitFor(5*time.Second, 200*time.Millisecond, func() error {
		qLen, err := rdb.LLen(context.Background(), nodeQueue).Result()
		if err != nil {
			return err
		}
		if qLen > 0 {
			return fmt.Errorf("node queue still has %d messages", qLen)
		}
		return nil
	}))
}

func waitFor(timeout, interval time.Duration, check func() error, msgAndArgs ...interface{}) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(interval)
	}
	if len(msgAndArgs) > 0 {
		return fmt.Errorf(fmt.Sprint(msgAndArgs...)+": %w", lastErr)
	}
	return lastErr
}

func reserveLocalAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}
