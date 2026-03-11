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

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/rustic-ai/forge/forge-go/testutil/probe"
)

func TestLevel2_FileDependencyIntegration(t *testing.T) {

	// 1. Setup Miniredis
	s, err := miniredis.Run()
	require.NoError(t, err)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	// 2. Setup a temporary directory for the dependency
	tempDir := t.TempDir()

	// Create required structure the Python FileSystemResolver expects based on
	// rustic-ai's core/tests/guild/agent_ext/depends/test_di_filesystem.py
	guildID := "test-dep-guild"
	orgID := "test-org"
	agentID := "file_manager"

	// Ensure the specific path exists for the dependency mapping, as
	// FileSystem constructs paths dynamically based on guild context
	agentFsPath := filepath.Join(tempDir, orgID, guildID, agentID)
	err = os.MkdirAll(agentFsPath, 0755)
	require.NoError(t, err)

	// 3. Craft the Guild and Agent Specs manually with the FileSystem Dependency
	agentSpecJSON := `{
		"id": "file_manager",
		"name": "file_manager",
		"class_name": "rustic_ai.forge.testutils.file_manager_agent.FileManagerAgent",
		"description": "Reads and writes files",
		"listen_to_default_topic": true,
		"act_only_when_tagged": false,
		"properties": {},
		"additional_topics": [],
		"dependency_map": {
			"filesystem": {
				"class_name": "rustic_ai.core.guild.agent_ext.depends.filesystem.filesystem.FileSystemResolver",
				"properties": {
					"path_base": "%s",
					"protocol": "file",
					"storage_options": {
						"auto_mkdir": true
					}
				}
			}
		}
	}`

	agentSpecJSONStr := fmt.Sprintf(agentSpecJSON, tempDir)

	var agentSpec protocol.AgentSpec
	err = json.Unmarshal([]byte(agentSpecJSONStr), &agentSpec)
	require.NoError(t, err, "Failed to parse agent spec JSON")

	guildSpec := &protocol.GuildSpec{
		ID:          guildID,
		Name:        "Test Filesystem Guild",
		Description: "A guild for testing filesystem dependencies",
		Agents:      []protocol.AgentSpec{agentSpec},
		Properties:  map[string]interface{}{},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{},
		},
	}
	guildSpecJSON, _ := json.Marshal(guildSpec)

	// 4. Resolve Environment Variables
	env := os.Environ()
	env = append(env, fmt.Sprintf("FORGE_GUILD_JSON=%s", string(guildSpecJSON)))
	env = append(env, fmt.Sprintf("FORGE_AGENT_CONFIG_JSON=%s", agentSpecJSONStr))

	// Set the Messaging variables that envvars uses
	env = append(env, "FORGE_CLIENT_MODULE=rustic_ai.redis.messaging.backend")
	env = append(env, "FORGE_CLIENT_TYPE=RedisMessagingBackend")
	env = append(env, fmt.Sprintf(`FORGE_CLIENT_PROPERTIES_JSON={"organization_id": "%s", "redis_client": {"host": "%s", "port": %s, "db": 0}}`, orgID, s.Host(), s.Port()))

	// Important for tests: force unbuffered python IO
	env = append(env, "PYTHONUNBUFFERED=1")

	// 5. Look up the Agent in the mock Registry logic with local path override
	pwd, _ := os.Getwd()
	forgePythonPath := filepath.Join(pwd, "..", "..", "forge-python")
	t.Setenv("FORGE_PYTHON_PKG", forgePythonPath)

	// Use an e2e-local registry fixture that explicitly includes FileManagerAgent.
	regConfPath := filepath.Join(t.TempDir(), "registry.yaml")
	registryYAML := `entries:
  - id: FileManagerAgent
    class_name: rustic_ai.forge.testutils.file_manager_agent.FileManagerAgent
    description: Filesystem DI test agent
    runtime: uvx
`
	require.NoError(t, os.WriteFile(regConfPath, []byte(registryYAML), 0o644))
	r, err := registry.Load(regConfPath)
	require.NoError(t, err, "Failed to load registry yaml")

	// 6. Launch via Local Process Supervisor
	sup := supervisor.NewProcessSupervisor(rdb, supervisor.WithWorkDirBase(t.TempDir()))
	defer func() {
		if err := sup.StopAll(context.Background()); err != nil {
			t.Logf("failed to stop all agents: %v", err)
		}
	}()
	agentCtx, cancelAgent := context.WithCancel(ctx)
	defer cancelAgent()

	go func() {
		err := sup.Launch(agentCtx, guildSpec.ID, &agentSpec, r, env)
		if err != nil && !strings.Contains(err.Error(), "killed") {
			t.Logf("Agent process exited with error: %v", err)
		}
	}()

	// 7. Verify via ProbeAgent that files can be written and read
	t.Logf("Waiting for File Manager to wake up...")

	// Construct fully qualified topic strings for Redis pub/sub routing
	topicIn := fmt.Sprintf("%s:default_topic", guildSpec.ID)
	topicOut := fmt.Sprintf("%s:default_topic", guildSpec.ID)

	p := probe.NewProbeAgent(rdb)

	// Create the WriteToFile pydantic model directly via test payload
	writeAction := map[string]interface{}{
		"filename":   "test_data.txt",
		"content":    "dependency injection rules",
		"guild_path": false,
	}

	reqMsg := probe.DefaultMessage(1, "TestProbe", writeAction)
	reqMsg.Format = "rustic_ai.forge.testutils.file_manager_agent.WriteToFile"
	reqMsg.Topics = []string{topicIn}
	reqMsg.TopicPublishedTo = topicIn

	// Background routine to continually ping the agent until it finishes uvx boots and subscribes
	done := make(chan struct{})
	go func() {
		counter := int64(0)
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Second):
				counter++
				reqMsg.ID = counter
				_ = p.Publish(ctx, guildSpec.ID, topicIn, reqMsg)
			}
		}
	}()

	var respMsg *probe.Message
	ch := p.Subscribe(ctx, topicOut)

	// Use a 30s manual timeout
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	defer cancelTimeout()

	for {
		select {
		case msg := <-ch:
			if msg != nil && (msg.Sender.Name == nil || *msg.Sender.Name != "TestProbe") {
				respMsg = msg
				goto DoneWaiting
			}
		case <-timeoutCtx.Done():
			t.Fatal("Agent did not respond in time")
		}
	}
DoneWaiting:
	close(done)

	assert.Equal(t, "success", respMsg.Payload["result"])
	assert.Equal(t, "test_data.txt", respMsg.Payload["filename"])

	// 8. Physical disk verification - Assert Go sees the file Python created
	targetFile := filepath.Join(agentFsPath, "test_data.txt")
	content, err := os.ReadFile(targetFile)
	require.NoError(t, err, "File should exist on disk")
	assert.Equal(t, "dependency injection rules", string(content))

	// Cancel the context to signify completion
	cancelAgent()
}
