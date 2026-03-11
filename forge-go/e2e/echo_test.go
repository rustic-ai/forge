package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/helper/envvars"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/rustic-ai/forge/forge-go/testutil/probe"
)

// TestLevel1_EchoAgentIntegration tests a full end-to-end flow with a Python EchoAgent
// using the standard forge-python agent runner.
func TestLevel1_EchoAgentIntegration(t *testing.T) {

	supervisors := []string{"process", "docker", "bwrap"}

	for _, reqSup := range supervisors {
		t.Run(reqSup, func(t *testing.T) {
			// 1. Define the Guild and Agent specifications
			// The AgentSpec uses the fully qualified Python class name so the runner
			// knows exactly which module to import and instantiate.
			agentSpecJSON := `{
				"id": "echo-agent",
				"name": "EchoAgent",
				"description": "Echoes messages",
				"class_name": "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				"additional_topics": ["echo_topic"],
				"listen_to_default_topic": false,
				"properties": {}
			}`

			// The GuildSpec holds the messaging bus configuration (InMemory for tests)
			guildSpecJSON := `{
				"id": "test-guild",
				"name": "Test Guild",
				"version": "1.0",
				"description": "Integration test guild",
				"properties": {
					"messaging": {
						"backend_module": "rustic_ai.core.messaging.local.local_messaging_backend",
						"backend_class": "LocalMessagingBackend",
						"backend_config": {}
					}
				},
				"agents": [],
				"routes": {
					"steps": []
				}
			}`

			// 2. Parse the JSON strings into abstract Go structs so we can feed them into envvars
			var agentSpec protocol.AgentSpec
			require.NoError(t, json.Unmarshal([]byte(agentSpecJSON), &agentSpec))
			agentSpec.ID = fmt.Sprintf("echo-agent-%s", reqSup)

			var guildSpec protocol.GuildSpec
			require.NoError(t, json.Unmarshal([]byte(guildSpecJSON), &guildSpec))
			guildSpec.ID = fmt.Sprintf("test-guild-%s", reqSup)

			// 2b. Start an embedded miniredis server to act as the messaging bus
			mr, err := miniredis.Run()
			require.NoError(t, err)
			defer mr.Close()

			rdb := redis.NewClient(&redis.Options{
				Addr: mr.Addr(),
			})
			defer rdb.Close()
			// Update guild configuration dynamically to point to our InMemory backend
			if guildSpec.Properties == nil {
				guildSpec.Properties = make(map[string]interface{})
			}
			guildSpec.Properties["messaging"] = map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{
						"host": mr.Host(),
						"port": mr.Port(),
						"db":   0,
					},
				},
			}

			// 3. Setup the messaging probe.
			probeAgent := probe.NewProbeAgent(rdb)

			ctx := context.Background()

			// 4b. Identify execution parameters via the AgentRegistry
			pwd, _ := os.Getwd()
			forgePythonPath, _ := filepath.Abs(filepath.Join(pwd, "..", "..", "forge-python"))
			t.Setenv("FORGE_PYTHON_PKG", forgePythonPath)

			regConfPath := filepath.Join(pwd, "..", "conf", "forge-agent-registry.yaml")
			r, err := registry.Load(regConfPath)
			require.NoError(t, err, "Failed to load registry yaml")

			// Dynamically mount the local Python workspace into the sandbox for testing
			err = r.InjectFilesystem(agentSpec.ClassName, registry.FilesystemPermission{
				Path: forgePythonPath,
				Mode: "rw",
			})
			require.NoError(t, err)

			// Temporarily disable strict AirGapped networking for testing so Docker can rebuild missing .whl cache variants
			err = r.InjectNetwork(agentSpec.ClassName, []string{"host"})
			require.NoError(t, err)

			entry, _ := r.Lookup(agentSpec.ClassName)

			// 4. Construct the environment variables for the Python agent runner.
			secretProvider := secrets.NewEnvSecretProvider() // Use env provider for basic mock
			envVars, err := envvars.BuildAgentEnv(ctx, &guildSpec, &agentSpec, entry, secretProvider)
			require.NoError(t, err)

			env := append(envVars, "PYTHONUNBUFFERED=1")

			// 5. Launch the Agent using chosen supervisor
			var sup supervisor.AgentSupervisor
			switch reqSup {
			case "process":
				sup = supervisor.NewProcessSupervisor(rdb, supervisor.WithWorkDirBase(t.TempDir()))
			case "docker":
				ds, err := supervisor.NewDockerSupervisor(rdb)
				if err != nil || !ds.Available() {
					t.Skip("Docker not available")
				}
				sup = ds
			case "bwrap":
				bs := supervisor.NewBubblewrapSupervisor(rdb)
				if !bs.Available() || !bubblewrapUsable() {
					t.Skip("Bubblewrap not available/usable in this environment")
				}
				sup = bs
			}

			defer func() {
				if err := sup.StopAll(context.Background()); err != nil {
					t.Logf("failed to stop all agents: %v", err)
				}
			}()
			agentCtx, cancelAgent := context.WithCancel(context.Background())
			defer cancelAgent() // Ensure process is killed when test finishes

			go func() {
				// Launch the Python process managed by the supervisor
				err := sup.Launch(agentCtx, guildSpec.ID, &agentSpec, r, env)
				if err != nil && !strings.Contains(err.Error(), "killed") {
					t.Logf("Agent process exited with error: %v", err)
				}
			}()

			// 6. Execute the core test assertions
			// Send a payload to the topic the EchoAgent is configured to listen to.
			testPayload := map[string]interface{}{
				"message": "Hello from Go E2E!",
			}
			msg := probe.DefaultMessage(1001, "probe-agent", testPayload)
			topicIn := fmt.Sprintf("%s:echo_topic", guildSpec.ID)
			msg.TopicPublishedTo = topicIn

			// Background routine to continually ping the agent until it finishes uvx boots and subscribes
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-time.After(2 * time.Second):
						_ = probeAgent.Publish(ctx, guildSpec.ID, topicIn, msg)
					}
				}
			}()

			// Wait up to 30 seconds for the EchoAgent to receive, process, and publish a response
			// Docker boots usually take ~3s the first time if cached, but we give 30s.
			topicOut := fmt.Sprintf("%s:default_topic", guildSpec.ID)
			respMsg, err := probeAgent.WaitForMessage(ctx, topicOut, 30*time.Second)
			close(done)
			require.NoError(t, err, "EchoAgent did not respond in time")

			// Verify the payload contents match what we sent.
			assert.Equal(t, "Hello from Go E2E!", respMsg.Payload["message"])

			// Cancel the context to signify completion
			cancelAgent()
		})
	}
}
