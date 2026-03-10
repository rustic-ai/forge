package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/embed"
	"github.com/rustic-ai/forge/forge-go/testutil/probe"
)

// TestE2E_ForgeRun_GuildManagerBootstrapsAgents tests the entire lifecycle 1.11.1
func TestE2E_ForgeRun_GuildManagerBootstrapsAgents(t *testing.T) {
	// Skip if we are just running normal unit tests, or if Python/uv isn't installed
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Bootstrap does real dependency work on cold CI runners, so keep setup separate
	// from the shorter runtime/assertion window.
	setupCtx, setupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer setupCancel()

	// 1. Setup Test Environment (Miniredis & SQLite)
	er, err := embed.StartEmbeddedRedis()
	require.NoError(t, err, "failed to start embedded redis")

	rdb := redis.NewClient(&redis.Options{Addr: er.Addr()})
	defer rdb.Close()

	err = rdb.Ping(setupCtx).Err()
	require.NoError(t, err, "failed to ping embedded redis")

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "forge_e2e.db")
	specPath, err := filepath.Abs("testdata/echo-guild.yaml")
	require.NoError(t, err, "failed to get absolute path to guild spec")
	regPath, err := filepath.Abs("../../conf/forge-agent-registry.yaml")
	require.NoError(t, err, "failed to get absolute path to registry")

	// 4. Build the latest forge binary to execute
	forgeBin := filepath.Join(tempDir, "forge")
	cmdBuild := exec.CommandContext(setupCtx, "go", "build", "-o", forgeBin, "../../main.go")
	out, err := cmdBuild.CombinedOutput()
	require.NoError(t, err, "Failed to compile forge binary:\n%s", out)

	redisHost := "localhost"
	redisPort := "6379"
	if host, port, err := net.SplitHostPort(er.Addr()); err == nil {
		if host != "" {
			redisHost = host
		}
		redisPort = port
	}

	// Create absolute paths for python environment
	absPythonPkg, _ := filepath.Abs("../../../forge-python")

	// Ensure the forge-python package has a populated virtual environment
	cmdSync := exec.CommandContext(setupCtx, "uv", "sync", "--frozen")
	cmdSync.Dir = absPythonPkg
	cmdSync.Stdout = os.Stdout
	cmdSync.Stderr = os.Stderr
	require.NoError(t, cmdSync.Run(), "Failed to run uv sync in forge-python")

	runCtx, runCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer runCancel()

	// 5. Execute 'forge run' in the background
	cmdRun := exec.CommandContext(runCtx, forgeBin, "run", specPath,
		"--redis", er.Addr(),
		"--registry", regPath,
		"--db-path", dbPath,
	)

	// Inject the local python package paths so the local Python binary can find both forge and core namespaces
	cmdRun.Env = append(os.Environ(),
		fmt.Sprintf("FORGE_PYTHON_PKG=%s", absPythonPkg),
		fmt.Sprintf("REDIS_HOST=%s", redisHost),
		fmt.Sprintf("REDIS_PORT=%s", redisPort),
		"LOG_LEVEL=DEBUG",
		"RUSTIC_AI_LOG_LEVEL=DEBUG",
	)

	// We'll capture output for debugging failing tests
	cmdRun.Stdout = os.Stdout
	cmdRun.Stderr = os.Stderr

	err = cmdRun.Start()
	require.NoError(t, err, "Failed to start forge run")

	// Ensure forge run cleans up when test exits
	defer func() {
		if cmdRun.Process != nil {
			_ = cmdRun.Process.Signal(os.Interrupt)
			// Wait a beat before force killing if it didn't shut down
			time.Sleep(1 * time.Second)
			_ = cmdRun.Process.Kill()
		}
	}()

	// 6. Connect Test Probe to Redis
	probeAgent := probe.NewProbeAgent(rdb)

	// 7. Probe Validation: Send AgentListRequest to the System Topic
	// The Guild Manager Agent should be listening here!
	sysTopic := "e2e-guild-1:system_topic"
	listReq := probe.DefaultMessage(1, "GoProbe", map[string]interface{}{
		"guild_id": "e2e-guild-1",
	})
	listReq.Format = "rustic_ai.core.agents.system.models.AgentListRequest"
	listReq.TopicPublishedTo = sysTopic

	var listRes *probe.Message
	// Retrying because it takes a moment for UV to download and start python processes
	replyTopic := "e2e-guild-1:default_topic"
	require.Eventually(t, func() bool {
			err := probeAgent.Publish(runCtx, "e2e-guild-1", sysTopic, listReq)
			if err != nil {
				return false
			}

			res, err := probeAgent.WaitForMessage(runCtx, replyTopic, 2*time.Second)
			if err == nil && res.Format == "rustic_ai.core.agents.system.models.AgentListResponse" {
				listRes = res
				return true
		}
		return false
	}, 20*time.Second, 2*time.Second, "GuildManagerAgent should eventually respond to AgentListRequest")

	// 8. Assert the Echo Agent is in the returned active agents list
	agentsRaw, ok := listRes.Payload["agents"].([]interface{})
	require.True(t, ok, "AgentListResponse should contain 'agents' list")

	foundEcho := false
	for _, a := range agentsRaw {
		agentMap := a.(map[string]interface{})
		if agentMap["name"] == "EchoAgent" {
			foundEcho = true
			break
		}
	}
	assert.True(t, foundEcho, "GuildManagerAgent should have successfully spawned the EchoAgent")

	// 9. Send a message to the EchoAgent and verify response
	echoTopic := "e2e-guild-1:echo_topic"
	echoOutTopic := "e2e-guild-1:go_probe_outbox" // We await the absolute routed topic produced by the slip
	echoReq := probe.DefaultMessage(2, "GoProbe", map[string]interface{}{
		"message": "Hello from Go E2E!",
	})
	echoReq.TopicPublishedTo = echoTopic

	// 9.5 Mock the GuildSpec routing slip behavior usually provided natively by the Python GatewayAgent,
	// enforcing the EchoAgent to target 'go_probe_outbox'
	echoReq.RoutingSlip = map[string]interface{}{
		"steps": []map[string]interface{}{
			{
				"agent": map[string]interface{}{
					"id":   "echo-1",
					"name": "EchoAgent",
				},
				"destination": map[string]interface{}{
					"topics": []string{"go_probe_outbox"},
				},
				"process_status": "completed",
				"reason":         "simply doing my job",
			},
		},
	}

	var echoRes *probe.Message
	require.Eventually(t, func() bool {
			err := probeAgent.Publish(runCtx, "e2e-guild-1", echoTopic, echoReq)
			if err != nil {
				return false
			}

			res, err := probeAgent.WaitForMessage(runCtx, echoOutTopic, 2*time.Second)
			if err == nil && res != nil {
				echoRes = res
				return true
		}
		return false
	}, 15*time.Second, 2*time.Second, "EchoAgent should eventually respond to our message")

	assert.NotNil(t, echoRes)
	assert.Equal(t, "Hello from Go E2E!", echoRes.Payload["message"])

	// 10. Test graceful shutdown (SIGINT)
	err = cmdRun.Process.Signal(os.Interrupt)
	require.NoError(t, err)

	// Command should exit cleanly within the context window
	err = cmdRun.Wait()
	require.NoError(t, err, "Forge should shutdown gracefully without error")
}
