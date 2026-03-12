package guild

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestBootstrap_Flow_PersistsRoutesAndEnqueuesSpawn(t *testing.T) {
	ctx := context.Background()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer db.Close()

	routeTimes := 1
	spec := &protocol.GuildSpec{
		ID:          "bootstrap-flow-guild",
		Name:        "Bootstrap Flow",
		Description: "Verifies bootstrap lifecycle",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "bootstrap-flow-guild#a-0",
				Name:        "Echo Agent",
				Description: "Echo",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
			},
		},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{
				{
					AgentType:  strPtr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: strPtr("unwrap_and_forward_message"),
					Destination: &protocol.RoutingDestination{
						Topics: protocol.TopicsFromSlice([]string{"echo_topic"}),
					},
					RouteTimes: &routeTimes,
				},
			},
		},
	}

	_, err = Bootstrap(ctx, db, rdb, spec, "org-bootstrap", filepath.Join(t.TempDir(), "missing-agent-deps.yaml"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	guildModel, err := db.GetGuild("bootstrap-flow-guild")
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}
	if len(guildModel.Routes) != 1 {
		t.Fatalf("expected 1 persisted route, got %d", len(guildModel.Routes))
	}

	raw, err := rdb.RPop(ctx, "forge:control:requests").Result()
	if err != nil {
		t.Fatalf("pop control request: %v", err)
	}

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("decode control wrapper: %v", err)
	}

	if wrapper.Command != "spawn" {
		t.Fatalf("expected spawn command, got %q", wrapper.Command)
	}
	if wrapper.Payload.AgentSpec.ID != "bootstrap-flow-guild#manager_agent" {
		t.Fatalf("unexpected manager id: %s", wrapper.Payload.AgentSpec.ID)
	}
	if wrapper.Payload.AgentSpec.ClassName != GuildManagerClassName {
		t.Fatalf("unexpected manager class: %s", wrapper.Payload.AgentSpec.ClassName)
	}
	if _, ok := wrapper.Payload.AgentSpec.Properties["database_url"]; ok {
		t.Fatalf("manager spawn payload should not include database_url")
	}
	if _, ok := wrapper.Payload.AgentSpec.Properties["manager_api_base_url"]; !ok {
		t.Fatalf("manager spawn payload missing manager_api_base_url")
	}

	rawGuildSpec, ok := wrapper.Payload.ClientProperties["guild_spec"].(string)
	if !ok || rawGuildSpec == "" {
		t.Fatalf("expected client_properties.guild_spec string in spawn payload")
	}

	var spawnedSpec protocol.GuildSpec
	if err := json.Unmarshal([]byte(rawGuildSpec), &spawnedSpec); err != nil {
		t.Fatalf("decode spawn guild_spec: %v", err)
	}
	if spawnedSpec.Routes == nil || len(spawnedSpec.Routes.Steps) != 1 {
		t.Fatalf("expected spawned guild_spec to include one route")
	}
}

func TestBootstrap_Flow_NormalizesSpawnedGuildSpecIDs(t *testing.T) {
	ctx := context.Background()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer db.Close()

	spec := &protocol.GuildSpec{
		Name:        "Bootstrap ID Normalize",
		Description: "Verifies runtime spec id propagation",
		Properties: map[string]interface{}{
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				Name:        "Echo Agent",
				Description: "Echo",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
			},
		},
	}

	guildModel, err := Bootstrap(ctx, db, rdb, spec, "org-bootstrap", filepath.Join(t.TempDir(), "missing-agent-deps.yaml"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	raw, err := rdb.RPop(ctx, "forge:control:requests").Result()
	if err != nil {
		t.Fatalf("pop control request: %v", err)
	}

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("decode control wrapper: %v", err)
	}

	rawGuildSpec, ok := wrapper.Payload.ClientProperties["guild_spec"].(string)
	if !ok || rawGuildSpec == "" {
		t.Fatalf("expected client_properties.guild_spec string in spawn payload")
	}

	var spawnedSpec protocol.GuildSpec
	if err := json.Unmarshal([]byte(rawGuildSpec), &spawnedSpec); err != nil {
		t.Fatalf("decode spawn guild_spec: %v", err)
	}

	if spawnedSpec.ID != guildModel.ID {
		t.Fatalf("expected spawned guild spec id %q, got %q", guildModel.ID, spawnedSpec.ID)
	}
	if len(spawnedSpec.Agents) != 1 {
		t.Fatalf("expected one agent in spawned guild spec")
	}
	if spawnedSpec.Agents[0].ID != guildModel.ID+"#a-0" {
		t.Fatalf("expected spawned agent id %q, got %q", guildModel.ID+"#a-0", spawnedSpec.Agents[0].ID)
	}

	agentModel, err := db.GetAgent(guildModel.ID, guildModel.ID+"#a-0")
	if err != nil {
		t.Fatalf("get persisted agent: %v", err)
	}
	if agentModel.GuildID == nil || *agentModel.GuildID != guildModel.ID {
		t.Fatalf("persisted agent guild id mismatch")
	}
}

func TestBootstrap_Flow_PersistsResolvedFilesystemPathBase(t *testing.T) {
	ctx := context.Background()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer db.Close()

	globalRoot := filepath.Join(t.TempDir(), "workspaces")
	t.Setenv(forgeFilesystemGlobalRootEnv, globalRoot)

	spec := &protocol.GuildSpec{
		ID:          "bootstrap-fs-guild",
		Name:        "Bootstrap FS",
		Description: "Verifies filesystem path propagation",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
					"protocol":  "file",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "bootstrap-fs-guild#a-0",
				Name:        "Worker",
				Description: "worker",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	_, err = Bootstrap(ctx, db, rdb, spec, "org-bootstrap", filepath.Join(t.TempDir(), "missing-agent-deps.yaml"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	guildModel, err := db.GetGuild("bootstrap-fs-guild")
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}

	storedSpec := store.ToGuildSpec(guildModel)
	fsDep, ok := storedSpec.DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected persisted filesystem dependency")
	}
	if got, _ := fsDep.Properties["path_base"].(string); got != filepath.Join(globalRoot, "uploads") {
		t.Fatalf("expected persisted path_base %q, got %q", filepath.Join(globalRoot, "uploads"), got)
	}
	if len(storedSpec.Agents) != 1 {
		t.Fatalf("expected one persisted agent")
	}
	agentDep, ok := storedSpec.Agents[0].DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected persisted agent filesystem dependency")
	}
	if got, _ := agentDep.Properties["path_base"].(string); got != filepath.Join(globalRoot, "private") {
		t.Fatalf("expected persisted agent path_base %q, got %q", filepath.Join(globalRoot, "private"), got)
	}

	raw, err := rdb.RPop(ctx, "forge:control:requests").Result()
	if err != nil {
		t.Fatalf("pop control request: %v", err)
	}

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("decode control wrapper: %v", err)
	}

	rawGuildSpec, ok := wrapper.Payload.ClientProperties["guild_spec"].(string)
	if !ok || rawGuildSpec == "" {
		t.Fatalf("expected client_properties.guild_spec string in spawn payload")
	}

	var spawnedSpec protocol.GuildSpec
	if err := json.Unmarshal([]byte(rawGuildSpec), &spawnedSpec); err != nil {
		t.Fatalf("decode spawn guild_spec: %v", err)
	}

	spawnedDep, ok := spawnedSpec.DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected spawned filesystem dependency")
	}
	if got, _ := spawnedDep.Properties["path_base"].(string); got != filepath.Join(globalRoot, "uploads") {
		t.Fatalf("expected spawned path_base %q, got %q", filepath.Join(globalRoot, "uploads"), got)
	}
	if len(spawnedSpec.Agents) != 1 {
		t.Fatalf("expected one spawned agent")
	}
	spawnedAgentDep, ok := spawnedSpec.Agents[0].DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected spawned agent filesystem dependency")
	}
	if got, _ := spawnedAgentDep.Properties["path_base"].(string); got != filepath.Join(globalRoot, "private") {
		t.Fatalf("expected spawned agent path_base %q, got %q", filepath.Join(globalRoot, "private"), got)
	}
}

func TestBootstrap_Flow_PersistsResolvedS3FilesystemPathBase(t *testing.T) {
	ctx := context.Background()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	defer db.Close()

	t.Setenv(forgeFilesystemGlobalRootEnv, "s3://forge-bucket/root")

	spec := &protocol.GuildSpec{
		ID:          "bootstrap-s3-guild",
		Name:        "Bootstrap S3",
		Description: "Verifies object store filesystem path propagation",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "bootstrap-s3-guild#a-0",
				Name:        "Worker",
				Description: "worker",
				ClassName:   "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	_, err = Bootstrap(ctx, db, rdb, spec, "org-bootstrap", filepath.Join(t.TempDir(), "missing-agent-deps.yaml"))
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	guildModel, err := db.GetGuild("bootstrap-s3-guild")
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}

	storedSpec := store.ToGuildSpec(guildModel)
	fsDep, ok := storedSpec.DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected persisted filesystem dependency")
	}
	if got, _ := fsDep.Properties["protocol"].(string); got != "s3" {
		t.Fatalf("expected persisted protocol %q, got %q", "s3", got)
	}
	if got, _ := fsDep.Properties["path_base"].(string); got != "s3://forge-bucket/root/uploads" {
		t.Fatalf("expected persisted path_base %q, got %q", "s3://forge-bucket/root/uploads", got)
	}
	if len(storedSpec.Agents) != 1 {
		t.Fatalf("expected one persisted agent")
	}
	agentDep, ok := storedSpec.Agents[0].DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected persisted agent filesystem dependency")
	}
	if got, _ := agentDep.Properties["protocol"].(string); got != "s3" {
		t.Fatalf("expected persisted agent protocol %q, got %q", "s3", got)
	}
	if got, _ := agentDep.Properties["path_base"].(string); got != "s3://forge-bucket/root/private" {
		t.Fatalf("expected persisted agent path_base %q, got %q", "s3://forge-bucket/root/private", got)
	}

	raw, err := rdb.RPop(ctx, "forge:control:requests").Result()
	if err != nil {
		t.Fatalf("pop control request: %v", err)
	}

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("decode control wrapper: %v", err)
	}

	rawGuildSpec, ok := wrapper.Payload.ClientProperties["guild_spec"].(string)
	if !ok || rawGuildSpec == "" {
		t.Fatalf("expected client_properties.guild_spec string in spawn payload")
	}

	var spawnedSpec protocol.GuildSpec
	if err := json.Unmarshal([]byte(rawGuildSpec), &spawnedSpec); err != nil {
		t.Fatalf("decode spawn guild_spec: %v", err)
	}

	spawnedDep, ok := spawnedSpec.DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected spawned filesystem dependency")
	}
	if got, _ := spawnedDep.Properties["protocol"].(string); got != "s3" {
		t.Fatalf("expected spawned protocol %q, got %q", "s3", got)
	}
	if got, _ := spawnedDep.Properties["path_base"].(string); got != "s3://forge-bucket/root/uploads" {
		t.Fatalf("expected spawned path_base %q, got %q", "s3://forge-bucket/root/uploads", got)
	}
	if len(spawnedSpec.Agents) != 1 {
		t.Fatalf("expected one spawned agent")
	}
	spawnedAgentDep, ok := spawnedSpec.Agents[0].DependencyMap["filesystem"]
	if !ok {
		t.Fatalf("expected spawned agent filesystem dependency")
	}
	if got, _ := spawnedAgentDep.Properties["protocol"].(string); got != "s3" {
		t.Fatalf("expected spawned agent protocol %q, got %q", "s3", got)
	}
	if got, _ := spawnedAgentDep.Properties["path_base"].(string); got != "s3://forge-bucket/root/private" {
		t.Fatalf("expected spawned agent path_base %q, got %q", "s3://forge-bucket/root/private", got)
	}
}
