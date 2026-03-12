package guild

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"log/slog"

	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/idgen"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

func Bootstrap(ctx context.Context, db store.Store, redisClient *redis.Client, spec *protocol.GuildSpec, orgID string, dependencyConfigPath string) (*store.GuildModel, error) {
	applyDefaults(spec)

	if err := mergeDependencies(spec, dependencyConfigPath); err != nil {
		return nil, fmt.Errorf("failed to merge dependencies: %w", err)
	}
	if err := ApplyFilesystemGlobalRoot(spec, strings.TrimSpace(os.Getenv(forgeFilesystemGlobalRootEnv))); err != nil {
		return nil, fmt.Errorf("failed to normalize filesystem dependency: %w", err)
	}

	guildModel, agentModels := buildModels(spec, orgID)
	normalizeRuntimeSpecIDs(spec, guildModel.ID)
	normalizeAgentModelIDs(agentModels, guildModel.ID)

	if err := db.CreateGuildWithAgents(guildModel, agentModels); err != nil {
		return nil, fmt.Errorf("failed to persist guild and agents: %w", err)
	}

	if err := EnqueueGuildManagerSpawn(ctx, redisClient, spec, orgID); err != nil {
		return nil, fmt.Errorf("failed to enqueue GMA spawn request: %w", err)
	}

	slog.Info("Guild bootstrap complete. Enqueued GuildManagerAgent.", "guild_id", guildModel.ID)

	return guildModel, nil
}

func EnqueueGuildManagerSpawn(ctx context.Context, redisClient *redis.Client, spec *protocol.GuildSpec, orgID string) error {
	if spec == nil {
		return fmt.Errorf("guild spec is required")
	}
	if spec.ID == "" {
		return fmt.Errorf("guild spec id is required")
	}
	normalizeRuntimeSpecIDs(spec, spec.ID)

	specBytes, _ := json.Marshal(spec)
	managerAPIBaseURL := strings.TrimSpace(os.Getenv("FORGE_MANAGER_API_BASE_URL"))
	if managerAPIBaseURL == "" {
		managerAPIBaseURL = "http://127.0.0.1:9090"
	}
	managerAPIToken := strings.TrimSpace(os.Getenv("FORGE_MANAGER_API_TOKEN"))

	spawnReq := protocol.SpawnRequest{
		RequestID: "bootstrap-" + spec.ID,
		GuildID:   spec.ID,
		AgentSpec: protocol.AgentSpec{
			ID:          spec.ID + "#manager_agent",
			Name:        spec.Name + " Manager",
			Description: "System agent for guild lifecycle orchestration",
			ClassName:   GuildManagerClassName,
			AdditionalTopics: []string{
				"system_topic",
				"heartbeat_topic",
				"guild_status_topic",
			},
			ListenToDefaultTopic: boolPtr(false),
			Properties: map[string]interface{}{
				"guild_spec":           spec,
				"manager_api_base_url": managerAPIBaseURL,
				"organization_id":      orgID,
				"manager_api_token":    managerAPIToken,
			},
		},
		ClientType: "forge",
		ClientProperties: protocol.JSONB{
			"guild_spec":           string(specBytes),
			"manager_api_base_url": managerAPIBaseURL,
			"organization_id":      orgID,
		},
	}

	return protocol.PushSpawnRequest(ctx, redisClient, spawnReq)
}

func normalizeRuntimeSpecIDs(spec *protocol.GuildSpec, guildID string) {
	if spec.ID == "" {
		spec.ID = guildID
	}
	for i := range spec.Agents {
		defaultID := fmt.Sprintf("a-%d", i)
		if spec.Agents[i].ID == "" || spec.Agents[i].ID == defaultID {
			spec.Agents[i].ID = fmt.Sprintf("%s#a-%d", guildID, i)
		}
	}
}

func normalizeAgentModelIDs(agentModels []store.AgentModel, guildID string) {
	for i := range agentModels {
		defaultID := fmt.Sprintf("a-%d", i)
		if agentModels[i].ID == "" || agentModels[i].ID == defaultID {
			agentModels[i].ID = fmt.Sprintf("%s#a-%d", guildID, i)
		}
	}
}

func applyDefaults(spec *protocol.GuildSpec) {
	if spec.Properties == nil {
		spec.Properties = make(map[string]interface{})
	}
	for i := range spec.Agents {
		if spec.Agents[i].ID == "" {
			if spec.ID != "" {
				spec.Agents[i].ID = fmt.Sprintf("%s#a-%d", spec.ID, i)
			} else {
				spec.Agents[i].ID = fmt.Sprintf("a-%d", i)
			}
		}
		if spec.Agents[i].Properties == nil {
			spec.Agents[i].Properties = map[string]interface{}{}
		}
		if spec.Agents[i].AdditionalTopics == nil {
			spec.Agents[i].AdditionalTopics = []string{}
		}
		if spec.Agents[i].AdditionalDependencies == nil {
			spec.Agents[i].AdditionalDependencies = []string{}
		}
		if spec.Agents[i].DependencyMap == nil {
			spec.Agents[i].DependencyMap = map[string]protocol.DependencySpec{}
		}
		if spec.Agents[i].Predicates == nil {
			spec.Agents[i].Predicates = map[string]protocol.RuntimePredicate{}
		}
	}

	// Execution engine default (env var override supported)
	if spec.Properties["execution_engine"] == nil {
		ee := os.Getenv("RUSTIC_AI_EXECUTION_ENGINE")
		if ee == "" {
			ee = "rustic_ai.forge.execution_engine.ForgeExecutionEngine"
		}
		spec.Properties["execution_engine"] = ee
	}

	// Messaging default (env var overrides supported)
	if spec.Properties["messaging"] == nil {
		backendModule := os.Getenv("RUSTIC_AI_MESSAGING_MODULE")
		if backendModule == "" {
			backendModule = "rustic_ai.redis.messaging.backend"
		}
		backendClass := os.Getenv("RUSTIC_AI_MESSAGING_CLASS")
		if backendClass == "" {
			backendClass = "RedisMessagingBackend"
		}
		var backendConfig map[string]interface{}
		if raw := os.Getenv("RUSTIC_AI_MESSAGING_BACKEND_CONFIG"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &backendConfig)
		}
		if backendConfig == nil {
			redisHost := os.Getenv("REDIS_HOST")
			if redisHost == "" {
				redisHost = "localhost"
			}
			redisPort := os.Getenv("REDIS_PORT")
			if redisPort == "" {
				redisPort = "6379"
			}
			backendConfig = map[string]interface{}{
				"redis_client": map[string]interface{}{
					"host": redisHost,
					"port": redisPort,
					"db":   0,
				},
			}
		}
		spec.Properties["messaging"] = map[string]interface{}{
			"backend_module": backendModule,
			"backend_class":  backendClass,
			"backend_config": backendConfig,
		}
	}
}

func mergeDependencies(spec *protocol.GuildSpec, configPath string) error {
	fileData, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dependency config: %w", err)
	}

	var fileDeps map[string]protocol.DependencySpec
	if err := yaml.Unmarshal(fileData, &fileDeps); err != nil {
		return fmt.Errorf("parse dependency config: %w", err)
	}

	if spec.DependencyMap == nil {
		spec.DependencyMap = make(map[string]protocol.DependencySpec)
	}

	for key, fileDef := range fileDeps {
		if _, exists := spec.DependencyMap[key]; !exists {
			spec.DependencyMap[key] = fileDef
		}
	}

	return nil
}

func buildModels(spec *protocol.GuildSpec, orgID string) (*store.GuildModel, []store.AgentModel) {
	execEngine := "rustic_ai.core.guild.execution.sync.sync_exec_engine.SyncExecutionEngine"
	if custom, ok := spec.Properties["execution_engine"].(string); ok {
		execEngine = custom
	}

	guildID := os.Getenv("FORGE_STATIC_GUILD_ID")
	if guildID == "" {
		guildID = spec.ID
	}
	if guildID == "" {
		guildID = idgen.NewShortUUID()
	}

	gm := &store.GuildModel{
		ID:              guildID,
		Name:            spec.Name,
		Description:     spec.Description,
		ExecutionEngine: execEngine,
		OrganizationID:  orgID,
		BackendConfig:   store.JSONB{},
		DependencyMap:   dependencySpecsToJSONB(spec.DependencyMap),
		Status:          store.GuildStatusRequested,
	}

	if spec.Routes != nil {
		for _, rSpec := range spec.Routes.Steps {
			rModel := store.FromRoutingRule(gm.ID, &rSpec)
			gm.Routes = append(gm.Routes, *rModel)
		}
	}

	if msgConfigMap, ok := spec.Properties["messaging"].(map[string]interface{}); ok {
		if m, ok := msgConfigMap["backend_module"].(string); ok {
			gm.BackendModule = m
		}
		if c, ok := msgConfigMap["backend_class"].(string); ok {
			gm.BackendClass = c
		}
		if bc, ok := msgConfigMap["backend_config"].(map[string]interface{}); ok && bc != nil {
			gm.BackendConfig = store.JSONB(bc)
		}
	}

	var am []store.AgentModel
	for i, aSpec := range spec.Agents {
		agentID := aSpec.ID
		if agentID == "" {
			agentID = fmt.Sprintf("%s#a-%d", gm.ID, i)
		}
		am = append(am, store.AgentModel{
			ID:                     agentID,
			GuildID:                &gm.ID,
			Name:                   aSpec.Name,
			Description:            aSpec.Description,
			ClassName:              aSpec.ClassName,
			Properties:             store.JSONB(aSpec.Properties),
			AdditionalTopics:       store.JSONBStringList(aSpec.AdditionalTopics),
			DependencyMap:          dependencySpecsToJSONB(aSpec.DependencyMap),
			AdditionalDependencies: store.JSONBStringList(aSpec.AdditionalDependencies),
			Predicates:             runtimePredicatesToJSONB(aSpec.Predicates),
			Status:                 store.AgentStatusPendingLaunch,
		})
	}

	return gm, am
}

func dependencySpecsToJSONB(specs map[string]protocol.DependencySpec) store.JSONB {
	out := store.JSONB{}
	for k, v := range specs {
		v.Normalize()
		out[k] = map[string]interface{}{
			"class_name": v.ClassName,
			"properties": v.Properties,
		}
	}
	return out
}

func runtimePredicatesToJSONB(predicates map[string]protocol.RuntimePredicate) store.JSONB {
	out := store.JSONB{}
	for k, v := range predicates {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		out[k] = m
	}
	return out
}
