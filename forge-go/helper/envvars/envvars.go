package envvars

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
)

// BuildAgentEnv constructs the full set of environment variables for a Forge agent process.
// It merges the parent process environment with Forge-specific configuration.
func BuildAgentEnv(
	ctx context.Context,
	guildSpec *protocol.GuildSpec,
	agentSpec *protocol.AgentSpec,
	regEntry *registry.AgentRegistryEntry,
	secretProvider secrets.SecretProvider,
	orgID string,
) ([]string, error) {

	envMap := make(map[string]string)

	guildBytes, err := json.Marshal(guildSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal guild spec: %w", err)
	}
	envMap["FORGE_GUILD_JSON"] = string(guildBytes)

	agentBytes, err := json.Marshal(agentSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal agent spec: %w", err)
	}
	envMap["FORGE_AGENT_CONFIG_JSON"] = string(agentBytes)

	backendModule := "rustic_ai.redis.messaging.backend"
	backendClass := "RedisMessagingBackend"
	backendConfig := map[string]interface{}{}
	if guildSpec.Properties != nil {
		if msgMap, ok := guildSpec.Properties["messaging"].(map[string]interface{}); ok {
			if bm, ok := msgMap["backend_module"].(string); ok {
				backendModule = bm
			}
			if bc, ok := msgMap["backend_class"].(string); ok {
				backendClass = bc
			}
			if bcfg, ok := msgMap["backend_config"].(map[string]interface{}); ok && bcfg != nil {
				backendConfig = bcfg
			}
		}
	}

	// Preserve existing FORGE_CLIENT_PROPERTIES_JSON if no backend config was found in the guild spec
	if osProps := os.Getenv("FORGE_CLIENT_PROPERTIES_JSON"); osProps != "" && len(backendConfig) == 0 {
		var osConfig map[string]interface{}
		if err := json.Unmarshal([]byte(osProps), &osConfig); err == nil {
			backendConfig = osConfig
		}
	}

	// Inject redis_client defaults if using Redis and not already configured
	if backendClass == "RedisMessagingBackend" {
		if _, exists := backendConfig["redis_client"]; !exists {
			host := os.Getenv("REDIS_HOST")
			if host == "" {
				host = "localhost"
			}
			port := os.Getenv("REDIS_PORT")
			if port == "" {
				port = "6379"
			}
			backendConfig["redis_client"] = map[string]interface{}{
				"host": host,
				"port": port,
				"db":   0,
			}
		}
	}

	// Inject nats_client defaults if using NATS and not already configured.
	// Matches the Python auto-injection in agent_runner.py.
	if backendClass == "NATSMessagingBackend" {
		if _, exists := backendConfig["nats_client"]; !exists {
			natsURL := os.Getenv("NATS_URL")
			if natsURL == "" {
				natsURL = "nats://localhost:4222"
			}
			backendConfig["nats_client"] = map[string]interface{}{
				"servers": []string{natsURL},
			}
		}
	}

	envMap["FORGE_CLIENT_MODULE"] = backendModule
	envMap["FORGE_CLIENT_TYPE"] = backendClass
	if len(backendConfig) > 0 {
		backendBytes, err := json.Marshal(backendConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal backend config: %w", err)
		}
		envMap["FORGE_CLIENT_PROPERTIES_JSON"] = string(backendBytes)
	} else {
		envMap["FORGE_CLIENT_PROPERTIES_JSON"] = "{}"
	}

	if err := resolveSecrets(ctx, agentSpec, regEntry, secretProvider, orgID, envMap); err != nil {
		return nil, err
	}

	uvCacheDir := os.Getenv("FORGE_UV_CACHE_DIR")
	if uvCacheDir == "" {
		uvCacheDir = forgepath.Resolve("uv_cache")
	}
	if uvCacheDir != "" {
		envMap["UV_CACHE_DIR"] = uvCacheDir
	}

	// Forward Redis connection env vars so spawned containers can find the correct Redis instance.
	// Without this, containers default to localhost:6379 instead of the intended Redis.
	for _, key := range []string{"REDIS_HOST", "REDIS_PORT", "REDIS_DB"} {
		if val := os.Getenv(key); val != "" {
			envMap[key] = val
		}
	}

	// Forward NATS URL so spawned agents can connect to the same NATS server.
	if val := os.Getenv("NATS_URL"); val != "" {
		envMap["NATS_URL"] = val
	}

	// Forward state manager and FORGE_HOME env vars so spawned agents inherit state store config.
	for _, key := range []string{"RUSTIC_AI_STATE_MANAGER", "FORGE_HOME"} {
		if val := os.Getenv(key); val != "" {
			envMap[key] = val
		}
	}

	var result []string
	for k, v := range envMap {
		result = append(result, fmt.Sprintf("%s=%s", k, v))
	}

	return result, nil
}

func resolveSecretWithFallback(
	ctx context.Context,
	secretProvider secrets.SecretProvider,
	orgID string,
	key string,
) (string, error) {
	// secrets api stores secrets by orgID
	secretKey := secrets.SecretStoreKey(orgID, key)
	val, err := secretProvider.Resolve(ctx, secretKey)
	if err != nil && errors.Is(err, secrets.ErrSecretNotFound) {
		// Try key directly - secret is not managed using secrets api
		val, err = secretProvider.Resolve(ctx, key)
		return val, err
	}
	return val, err
}

func resolveSecrets(
	ctx context.Context,
	agentSpec *protocol.AgentSpec,
	regEntry *registry.AgentRegistryEntry,
	secretProvider secrets.SecretProvider,
	orgID string,
	envMap map[string]string,
) error {
	for _, key := range agentSpec.Resources.Secrets {
		val, err := resolveSecretWithFallback(ctx, secretProvider, orgID, key)
		if err != nil {
			if errors.Is(err, secrets.ErrSecretNotFound) {
				continue
			}
			return fmt.Errorf("failed to resolve secret '%s' for agent '%s': %w", key, agentSpec.ID, err)
		}
		envMap[key] = val
	}

	if regEntry == nil {
		return nil
	}

	for _, s := range regEntry.Secrets {
		val, err := resolveSecretWithFallback(ctx, secretProvider, orgID, s.Key)
		if err != nil {
			if errors.Is(err, secrets.ErrSecretNotFound) && (s.Optional == nil || *s.Optional) {
				continue
			}
			return fmt.Errorf("failed to resolve secret '%s' for agent '%s': %w", s.Key, agentSpec.ID, err)
		}
		envMap[s.Label] = val
	}

	for _, o := range regEntry.OAuth {
		secretKey := oauth.StoreKey(orgID, o.Provider)
		val, err := secretProvider.Resolve(ctx, secretKey)
		if err != nil {
			if errors.Is(err, secrets.ErrSecretNotFound) && (o.Optional == nil || *o.Optional) {
				continue
			}
			return fmt.Errorf("failed to resolve OAuth token for provider '%s', agent '%s': %w", o.Provider, agentSpec.ID, err)
		}
		envMap[o.Label] = val
	}

	return nil
}
