package envvars

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

type mockSecretProvider struct {
	secrets map[string]string
}

func (m *mockSecretProvider) Resolve(ctx context.Context, key string) (string, error) {
	val, ok := m.secrets[key]
	if !ok {
		return "", secrets.ErrSecretNotFound
	}
	return val, nil
}

func TestBuildAgentEnv(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{
		ID:   "test-org/test-guild",
		Name: "Test Guild",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_class": "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"host": "localhost",
					"port": 6379,
				},
			},
		},
	}

	agentSpec := &protocol.AgentSpec{
		ID:        "AgentA",
		Name:      "Agent A",
		ClassName: "test.AgentA",
		Resources: protocol.ResourceSpec{
			Secrets: []string{"API_KEY", "DB_PASS"},
		},
	}

	provider := &mockSecretProvider{
		secrets: map[string]string{
			"API_KEY": "secret123",
			"DB_PASS": "passWORD",
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, provider)
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Verify secrets
	if envMap["API_KEY"] != "secret123" {
		t.Errorf("Expected API_KEY=secret123, got %s", envMap["API_KEY"])
	}
	if envMap["DB_PASS"] != "passWORD" {
		t.Errorf("Expected DB_PASS=passWORD, got %s", envMap["DB_PASS"])
	}

	// Verify structural config
	if envMap["FORGE_CLIENT_TYPE"] != "RedisMessagingBackend" {
		t.Errorf("Expected FORGE_CLIENT_TYPE=RedisMessagingBackend, got %s", envMap["FORGE_CLIENT_TYPE"])
	}

	var parsedBackend map[string]interface{}
	if err := json.Unmarshal([]byte(envMap["FORGE_CLIENT_PROPERTIES_JSON"]), &parsedBackend); err != nil {
		t.Fatalf("Failed to parse FORGE_CLIENT_PROPERTIES_JSON: %v", err)
	}
	if parsedBackend["host"] != "localhost" || parsedBackend["port"].(float64) != 6379 {
		t.Errorf("Unexpected FORGE_CLIENT_PROPERTIES_JSON: %v", parsedBackend)
	}

	// Verify missing secret is skipped gracefully
	agentSpec.Resources.Secrets = append(agentSpec.Resources.Secrets, "MISSING_KEY")
	envSlice, err = BuildAgentEnv(ctx, guildSpec, agentSpec, nil, provider)
	if err != nil {
		t.Fatalf("Expected BuildAgentEnv to succeed despite missing secret, got: %v", err)
	}

	for _, e := range envSlice {
		if strings.HasPrefix(e, "MISSING_KEY=") {
			t.Errorf("MISSING_KEY should not be in environment, but found: %s", e)
		}
	}
}

func TestBuildAgentEnv_RedisOSOverride(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{
		ID:   "test-override",
		Name: "Test Override",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_class": "RedisMessagingBackend",
			},
		},
	}

	agentSpec := &protocol.AgentSpec{
		ID:        "AgentB",
		Name:      "Agent B",
		ClassName: "test.AgentB",
	}

	// Simulate OS environment variables set by StartLocal
	t.Setenv("FORGE_CLIENT_PROPERTIES_JSON", `{"redis_client": {"host": "127.0.0.1", "port": "45629", "db": 0}}`)

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}})
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Verify structural config was merged from the OS environment and prevented the localhost fallback
	if envMap["FORGE_CLIENT_TYPE"] != "RedisMessagingBackend" {
		t.Errorf("Expected FORGE_CLIENT_TYPE=RedisMessagingBackend, got %s", envMap["FORGE_CLIENT_TYPE"])
	}

	var parsedBackend map[string]interface{}
	if err := json.Unmarshal([]byte(envMap["FORGE_CLIENT_PROPERTIES_JSON"]), &parsedBackend); err != nil {
		t.Fatalf("Failed to parse FORGE_CLIENT_PROPERTIES_JSON: %v", err)
	}

	rc, ok := parsedBackend["redis_client"].(map[string]interface{})
	if !ok {
		t.Fatalf("redis_client dictionary missing from FORGE_CLIENT_PROPERTIES_JSON: %v", parsedBackend)
	}

	if rc["host"] != "127.0.0.1" || rc["port"] != "45629" {
		t.Errorf("Unexpected redis_client inner map: %v", rc)
	}
}

func TestBuildAgentEnv_SerializesAgentDependencyMap(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{
		ID:   "test-agent-config",
		Name: "Test Agent Config",
	}

	agentSpec := &protocol.AgentSpec{
		ID:        "AgentC",
		Name:      "Agent C",
		ClassName: "test.AgentC",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"protocol":  "s3",
					"path_base": "s3://forge-bucket/root/private",
				},
			},
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}})
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	var parsedAgent map[string]interface{}
	if err := json.Unmarshal([]byte(envMap["FORGE_AGENT_CONFIG_JSON"]), &parsedAgent); err != nil {
		t.Fatalf("Failed to parse FORGE_AGENT_CONFIG_JSON: %v", err)
	}

	depMap, ok := parsedAgent["dependency_map"].(map[string]interface{})
	if !ok {
		t.Fatalf("dependency_map missing from FORGE_AGENT_CONFIG_JSON: %v", parsedAgent)
	}
	fsDep, ok := depMap["filesystem"].(map[string]interface{})
	if !ok {
		t.Fatalf("filesystem dependency missing from FORGE_AGENT_CONFIG_JSON: %v", depMap)
	}
	props, ok := fsDep["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("filesystem properties missing from FORGE_AGENT_CONFIG_JSON: %v", fsDep)
	}
	if props["protocol"] != "s3" || props["path_base"] != "s3://forge-bucket/root/private" {
		t.Errorf("unexpected filesystem properties in FORGE_AGENT_CONFIG_JSON: %v", props)
	}
}
