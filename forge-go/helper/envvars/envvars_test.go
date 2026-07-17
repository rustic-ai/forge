package envvars

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
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

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, provider, "")
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
	envSlice, err = BuildAgentEnv(ctx, guildSpec, agentSpec, nil, provider, "")
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

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}}, "")
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

func TestBuildAgentEnv_NATSAutoInjection(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{
		ID:   "test-nats",
		Name: "Test NATS",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.nats.messaging.backend",
				"backend_class":  "NATSMessagingBackend",
			},
		},
	}

	agentSpec := &protocol.AgentSpec{
		ID:        "AgentNATS",
		Name:      "NATS Agent",
		ClassName: "test.AgentNATS",
	}

	// Case 1: NATS_URL env var set — should inject that URL.
	t.Setenv("NATS_URL", "nats://nats.example.com:4222")

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}}, "")
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

	if envMap["FORGE_CLIENT_TYPE"] != "NATSMessagingBackend" {
		t.Errorf("Expected FORGE_CLIENT_TYPE=NATSMessagingBackend, got %s", envMap["FORGE_CLIENT_TYPE"])
	}

	var parsedBackend map[string]interface{}
	if err := json.Unmarshal([]byte(envMap["FORGE_CLIENT_PROPERTIES_JSON"]), &parsedBackend); err != nil {
		t.Fatalf("Failed to parse FORGE_CLIENT_PROPERTIES_JSON: %v", err)
	}
	natsClient, ok := parsedBackend["nats_client"].(map[string]interface{})
	if !ok {
		t.Fatalf("nats_client missing from FORGE_CLIENT_PROPERTIES_JSON: %v", parsedBackend)
	}
	servers, ok := natsClient["servers"].([]interface{})
	if !ok || len(servers) == 0 {
		t.Fatalf("nats_client.servers missing or empty: %v", natsClient)
	}
	if servers[0] != "nats://nats.example.com:4222" {
		t.Errorf("Expected nats://nats.example.com:4222, got %v", servers[0])
	}

	// NATS_URL should also be forwarded in the env
	if envMap["NATS_URL"] != "nats://nats.example.com:4222" {
		t.Errorf("Expected NATS_URL forwarded, got %s", envMap["NATS_URL"])
	}

	// Case 2: No NATS_URL set — should fall back to nats://localhost:4222.
	t.Setenv("NATS_URL", "")

	envSlice2, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}}, "")
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap2 := make(map[string]string)
	for _, e := range envSlice2 {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap2[parts[0]] = parts[1]
		}
	}

	var parsedBackend2 map[string]interface{}
	if err := json.Unmarshal([]byte(envMap2["FORGE_CLIENT_PROPERTIES_JSON"]), &parsedBackend2); err != nil {
		t.Fatalf("Failed to parse FORGE_CLIENT_PROPERTIES_JSON: %v", err)
	}
	natsClient2, ok := parsedBackend2["nats_client"].(map[string]interface{})
	if !ok {
		t.Fatalf("nats_client missing from FORGE_CLIENT_PROPERTIES_JSON (fallback): %v", parsedBackend2)
	}
	servers2, ok := natsClient2["servers"].([]interface{})
	if !ok || len(servers2) == 0 {
		t.Fatalf("nats_client.servers missing or empty (fallback): %v", natsClient2)
	}
	if servers2[0] != "nats://localhost:4222" {
		t.Errorf("Expected fallback nats://localhost:4222, got %v", servers2[0])
	}
}

func TestBuildAgentEnv_RegistrySecretLabel(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentLabel", ClassName: "test.AgentLabel"}

	regEntry := &registry.AgentRegistryEntry{
		Secrets: []protocol.SecretNeed{
			{Key: "OPENAI_API_KEY", Label: "OPENAI_TOKEN"},
		},
	}

	provider := &mockSecretProvider{
		secrets: map[string]string{
			"OPENAI_API_KEY": "sk-test123",
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, provider, "")
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["OPENAI_TOKEN"] != "sk-test123" {
		t.Errorf("expected OPENAI_TOKEN=sk-test123, got %q", envMap["OPENAI_TOKEN"])
	}
	if _, found := envMap["OPENAI_API_KEY"]; found {
		t.Errorf("OPENAI_API_KEY should not appear in env (label overrides key)")
	}
}

func TestBuildAgentEnv_RegistrySecretKeyEqualsLabelWhenOmitted(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentLabel", ClassName: "test.AgentLabel"}

	// JSON with no "label" field — label should default to key via Normalize()
	var s protocol.SecretNeed
	if err := json.Unmarshal([]byte(`{"key":"MY_SECRET"}`), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	regEntry := &registry.AgentRegistryEntry{Secrets: []protocol.SecretNeed{s}}
	provider := &mockSecretProvider{secrets: map[string]string{"MY_SECRET": "value123"}}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, provider, "")
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["MY_SECRET"] != "value123" {
		t.Errorf("expected MY_SECRET=value123 (label defaulted to key), got %q", envMap["MY_SECRET"])
	}
}

func TestBuildAgentEnv_OAuthTokenLabelDefaultsFromProvider(t *testing.T) {
	ctx := context.Background()

	orgID := "acme"
	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentOAuth", ClassName: "test.AgentOAuth"}

	// JSON with no "label" field — label should default to GITHUB_TOKEN via Normalize()
	var o protocol.OAuthNeed
	if err := json.Unmarshal([]byte(`{"provider":"github"}`), &o); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	regEntry := &registry.AgentRegistryEntry{OAuth: []protocol.OAuthNeed{o}}
	provider := &mockSecretProvider{
		secrets: map[string]string{
			oauth.StoreKey(orgID, "github"): "ghp_default",
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, provider, orgID)
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["GITHUB_TOKEN"] != "ghp_default" {
		t.Errorf("expected GITHUB_TOKEN=ghp_default (label defaulted from provider), got %q", envMap["GITHUB_TOKEN"])
	}
}

func TestBuildAgentEnv_OAuthToken(t *testing.T) {
	ctx := context.Background()

	orgID := "acme"
	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentOAuth", ClassName: "test.AgentOAuth"}

	regEntry := &registry.AgentRegistryEntry{
		OAuth: []protocol.OAuthNeed{
			{Provider: "github", Label: "GITHUB_TOKEN"},
		},
	}

	provider := &mockSecretProvider{
		secrets: map[string]string{
			oauth.StoreKey(orgID, "github"): "ghp_test456",
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, provider, orgID)
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["GITHUB_TOKEN"] != "ghp_test456" {
		t.Errorf("expected GITHUB_TOKEN=ghp_test456, got %q", envMap["GITHUB_TOKEN"])
	}
}

func TestBuildAgentEnv_RegistrySecret_RequiredMissing(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentSec", ClassName: "test.AgentSec"}

	optFalse := false
	regEntry := &registry.AgentRegistryEntry{
		Secrets: []protocol.SecretNeed{
			{Key: "MISSING_KEY", Label: "MISSING_KEY", Optional: &optFalse},
		},
	}

	_, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, &mockSecretProvider{secrets: map[string]string{}}, "")
	if err == nil {
		t.Fatal("expected error for missing required secret, got nil")
	}
}

func TestBuildAgentEnv_RegistrySecret_OptionalMissing(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentSec", ClassName: "test.AgentSec"}

	regEntry := &registry.AgentRegistryEntry{
		Secrets: []protocol.SecretNeed{
			protocol.NewSecretNeed("OPTIONAL_KEY"), // Optional=nil by default (skip silently)
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, &mockSecretProvider{secrets: map[string]string{}}, "")
	if err != nil {
		t.Fatalf("optional missing secret should be skipped, got: %v", err)
	}
	for _, e := range envSlice {
		if strings.HasPrefix(e, "OPTIONAL_KEY=") {
			t.Errorf("OPTIONAL_KEY should not appear when secret is missing: %s", e)
		}
	}
}

func TestBuildAgentEnv_OAuthToken_RequiredMissing(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentOAuth", ClassName: "test.AgentOAuth"}

	optFalse := false
	regEntry := &registry.AgentRegistryEntry{
		OAuth: []protocol.OAuthNeed{
			{Provider: "github", Label: "GITHUB_TOKEN", Optional: &optFalse},
		},
	}

	_, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, &mockSecretProvider{secrets: map[string]string{}}, "")
	if err == nil {
		t.Fatal("expected error for missing required OAuth token, got nil")
	}
}

func TestBuildAgentEnv_OAuthToken_OptionalMissing(t *testing.T) {
	ctx := context.Background()

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentOAuth", ClassName: "test.AgentOAuth"}

	regEntry := &registry.AgentRegistryEntry{
		OAuth: []protocol.OAuthNeed{
			protocol.NewOAuthNeed("github"), // Optional=nil by default (skip silently)
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, &mockSecretProvider{secrets: map[string]string{}}, "")
	if err != nil {
		t.Fatalf("optional missing OAuth token should be skipped, got: %v", err)
	}
	for _, e := range envSlice {
		if strings.HasPrefix(e, "GITHUB_TOKEN=") {
			t.Errorf("GITHUB_TOKEN should not appear when token is missing: %s", e)
		}
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

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, nil, &mockSecretProvider{secrets: map[string]string{}}, "")
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

func TestBuildAgentEnv_RegistrySecret_WithOrgId(t *testing.T) {
	ctx := context.Background()
	orgID := "acme"

	guildSpec := &protocol.GuildSpec{ID: "test/guild", Name: "Test"}
	agentSpec := &protocol.AgentSpec{ID: "AgentSec", ClassName: "test.AgentSec"}

	regEntry := &registry.AgentRegistryEntry{
		Secrets: []protocol.SecretNeed{
			protocol.NewSecretNeed("TEST_GLOBAL_KEY"),
			protocol.NewSecretNeed("ORG_KEY"),
			protocol.NewSecretNeed("USER_NAME"),
		},
	}

	provider := &mockSecretProvider{
		secrets: map[string]string{
			"TEST_GLOBAL_KEY":                          "myGlobalKey",
			"USER_NAME":                                "globalUserName",
			secrets.SecretStoreKey(orgID, "ORG_KEY"):   "myOrgKey",
			secrets.SecretStoreKey(orgID, "USER_NAME"): "orgUserName",
		},
	}

	envSlice, err := BuildAgentEnv(ctx, guildSpec, agentSpec, regEntry, provider, orgID)
	if err != nil {
		t.Fatalf("BuildAgentEnv failed: %v", err)
	}

	envMap := make(map[string]string)
	for _, e := range envSlice {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["TEST_GLOBAL_KEY"] != "myGlobalKey" {
		t.Errorf("expected TEST_GLOBAL_KEY=myGlobalKey, got %q", envMap["TEST_GLOBAL_KEY"])
	}
	if envMap["USER_NAME"] != "orgUserName" {
		t.Errorf("expected USER_NAME=orgUserName, got %q", envMap["USER_NAME"])
	}
	if envMap["ORG_KEY"] != "myOrgKey" {
		t.Errorf("expected ORG_KEY=myOrgKey, got %q", envMap["ORG_KEY"])
	}
}
