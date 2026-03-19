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

func TestLevel3_LLMAgentIntegration(t *testing.T) {
	if os.Getenv("FORGE_E2E_ENABLE_LIVE_LLM") != "1" {
		t.Skip("set FORGE_E2E_ENABLE_LIVE_LLM=1 to run live LLM e2e")
	}

	// 2. Setup Miniredis
	s, err := miniredis.Run()
	require.NoError(t, err)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = rdb.Close() }()
	ctx := context.Background()

	guildID := "test-llm-guild"
	orgID := "test-org"

	// 3. Craft the Guild and Agent Specs manually
	agentSpecJSON := `{
		"id": "test-llm-agent",
		"name": "LLMAgent",
		"class_name": "rustic_ai.llm_agent.llm_agent.LLMAgent",
		"description": "Integration test LLM agent",
		"listen_to_default_topic": false,
		"act_only_when_tagged": false,
		"properties": {
			"model": "gpt-4o-mini"
		},
		"dependency_map": {
			"llm": {
				"class_name": "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver",
				"properties": {
					"model": "gpt-4o-mini"
				}
			}
		},
		"additional_topics": ["llm_in"]
	}`

	var agentSpec protocol.AgentSpec
	err = json.Unmarshal([]byte(agentSpecJSON), &agentSpec)
	require.NoError(t, err, "Failed to parse agent spec JSON")

	guildSpec := &protocol.GuildSpec{
		ID:          guildID,
		Name:        "Test LLM Guild",
		Description: "A guild for testing LLM dependencies",
		Agents:      []protocol.AgentSpec{agentSpec},
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{
						"host": s.Host(),
						"port": s.Port(),
						"db":   0,
					},
					"organization_id": orgID,
				},
			},
		},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{},
		},
	}

	// 4. Resolve Environment Variables
	t.Setenv("FORGE_CLIENT_MODULE", "rustic_ai.redis.messaging.backend")
	t.Setenv("FORGE_CLIENT_TYPE", "RedisMessagingBackend")
	t.Setenv("FORGE_CLIENT_PROPERTIES_JSON", fmt.Sprintf(`{"organization_id": "%s", "redis_client": {"host": "%s", "port": %s, "db": 0}}`, orgID, s.Host(), s.Port()))
	t.Setenv("PYTHONUNBUFFERED", "1")
	t.Setenv("OPENAI_API_KEY", "dummy-openai-key")       // Required by forge-agent-registry.yaml
	t.Setenv("ANTHROPIC_API_KEY", "dummy-anthropic-key") // Required by forge-agent-registry.yaml
	t.Setenv("GEMINI_API_KEY", "dummy-gemini-key")       // Required by forge-agent-registry.yaml
	t.Setenv("UNREQUESTED_SECRET", "should-be-dropped")  // Negative test to ensure it does not leak into child

	secretProvider := secrets.NewChainSecretProvider(
		secrets.NewEnvSecretProvider(),
	)

	pwd, _ := os.Getwd()
	forgePythonPath := filepath.Join(pwd, "..", "..", "forge-python")
	t.Setenv("FORGE_PYTHON_PKG", forgePythonPath)

	r, err := registry.Load("../conf/forge-agent-registry.yaml")
	require.NoError(t, err, "Failed to load custom registry yaml")

	entry, _ := r.Lookup(agentSpec.ClassName)
	envVars, err := envvars.BuildAgentEnv(ctx, guildSpec, &agentSpec, entry, secretProvider)
	require.NoError(t, err)

	// Negative Test: Ensure unrequested host environment variables are NOT passed into the child
	for _, e := range envVars {
		if strings.HasPrefix(e, "UNREQUESTED_SECRET=") {
			t.Fatalf("CRITICAL SECURITY FAILURE: Unrequested secret leaked into child environment payload: %s", e)
		}
	}

	env := append(envVars, "PYTHONUNBUFFERED=1", "LOG_LEVEL=DEBUG")

	// 6. Launch via Local Process Supervisor
	sup := supervisor.NewProcessSupervisor(supervisor.NewRedisAgentStatusStore(rdb), supervisor.WithWorkDirBase(t.TempDir()))
	defer func() {
		if err := sup.StopAll(context.Background()); err != nil {
			t.Logf("failed to stop all agents: %v", err)
		}
	}()
	agentCtx, cancelAgent := context.WithCancel(ctx)
	defer cancelAgent()

	go func() {
		err := sup.Launch(agentCtx, "test-guild", &agentSpec, r, env)
		if err != nil && !strings.Contains(err.Error(), "killed") {
			t.Logf("Agent process exited with error: %v", err)
		}
	}()

	t.Logf("Waiting for LLM Agent to wake up...")

	topicIn := fmt.Sprintf("%s:llm_in", guildSpec.ID)
	topicOut := topicIn

	p := probe.NewProbeAgent(rdb)

	llmRequest := map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello, LLM!"},
		},
		"model": "gpt-4o-mini",
	}

	reqMsg := probe.DefaultMessage(2001, "TestProbe", llmRequest)
	reqMsg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest"
	reqMsg.Topics = []string{topicIn}
	reqMsg.TopicPublishedTo = topicIn

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(3 * time.Second):
				_ = p.Publish(ctx, guildSpec.ID, topicIn, reqMsg)
			}
		}
	}()

	var respMsg *probe.Message
	for {
		respMsg, err = p.WaitForMessage(ctx, topicOut, 45*time.Second) // UV might take a bit to install first time
		require.NoError(t, err, "Agent did not respond in time")
		if respMsg.Sender.Name == nil || *respMsg.Sender.Name != "TestProbe" {
			break
		}
	}
	close(done)

	// Validate output payload
	require.NotNil(t, respMsg.Payload)
	require.Contains(t, respMsg.Payload, "choices")

	choices, ok := respMsg.Payload["choices"].([]interface{})
	require.True(t, ok, "choices should be an array")
	require.NotEmpty(t, choices)

	firstChoice := choices[0].(map[string]interface{})
	message := firstChoice["message"].(map[string]interface{})
	content := message["content"].(string)

	assert.NotEmpty(t, content)
}
