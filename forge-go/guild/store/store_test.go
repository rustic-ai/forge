package store_test

import (
	"encoding/json"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
)

func setupTestDB(t *testing.T) store.Store {
	// Use an in-memory SQLite database for fast unit testing
	db, err := store.NewGormStore(store.DriverSQLite, "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to initialize test db: %v", err)
	}
	return db
}

func pointerStr(s string) *string {
	return &s
}

func pointerInt(i int) *int {
	return &i
}

func TestStore_GuildLifecycle(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	// 1. Create Guild
	guild := &store.GuildModel{
		ID:              "guild-123",
		Name:            "Test Guild",
		OrganizationID:  "org-1",
		ExecutionEngine: "test_engine",
		BackendConfig:   store.JSONB{"host": "localhost"},
	}

	if err := db.CreateGuild(guild); err != nil {
		t.Fatalf("Failed to create guild: %v", err)
	}

	// 2. Read Guild
	fetchedGuild, err := db.GetGuild("guild-123")
	if err != nil {
		t.Fatalf("Failed to get guild: %v", err)
	}
	if fetchedGuild.Name != "Test Guild" {
		t.Errorf("Expected name 'Test Guild', got %s", fetchedGuild.Name)
	}
	if fetchedGuild.BackendConfig["host"] != "localhost" {
		t.Errorf("JSONB unmarshaling failed for BackendConfig")
	}

	// 3. Update Status
	if err := db.UpdateGuildStatus("guild-123", store.GuildStatusRunning); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}
	fetchedGuild, _ = db.GetGuild("guild-123")
	if fetchedGuild.Status != store.GuildStatusRunning {
		t.Errorf("Status did not update, currently %v", fetchedGuild.Status)
	}

	// 4. List
	guilds, err := db.ListGuilds()
	if err != nil || len(guilds) != 1 {
		t.Fatalf("List returned incorrect results")
	}

	// 5. Delete
	if err := db.DeleteGuild("guild-123"); err != nil {
		t.Fatalf("Failed to delete guild: %v", err)
	}
	_, err = db.GetGuild("guild-123")
	if err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound after delete, got %v", err)
	}
}

func TestStore_AgentLifecycle(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guild := &store.GuildModel{ID: "guild-abc", OrganizationID: "org-1"}
	if err := db.CreateGuild(guild); err != nil {
		t.Fatalf("failed to create guild: %v", err)
	}

	agent := &store.AgentModel{
		ID:         "agent-xyz",
		GuildID:    pointerStr("guild-abc"),
		Name:       "Worker",
		ClassName:  "worker_class",
		Properties: store.JSONB{"max_retries": 5.0}, // Note unmarshaled JSON nums are float64 typically
	}

	if err := db.CreateAgent(agent); err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}

	fetched, err := db.GetAgent("guild-abc", "agent-xyz")
	if err != nil {
		t.Fatalf("Failed to fetch agent: %v", err)
	}

	if fetched.Name != "Worker" {
		t.Errorf("Name mismatch")
	}

	// SQLite returns float64 for numeric json
	if val, ok := fetched.Properties["max_retries"].(float64); !ok || val != 5.0 {
		t.Errorf("JSONB Properties mismatch or type error: %v %T", fetched.Properties["max_retries"], fetched.Properties["max_retries"])
	}

	agents, _ := db.ListAgentsByGuild("guild-abc")
	if len(agents) != 1 {
		t.Errorf("List agents returned incorrectly")
	}
}

func TestStore_GuildWithRoutesAndAgents(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guild := &store.GuildModel{
		ID:             "guild-complex",
		OrganizationID: "org-1",
		Routes: []store.GuildRoutes{
			{
				ID:                "route-1",
				AgentName:         pointerStr("agent-a"),
				DestinationTopics: store.JSONBStringList{"topicX", "topicY"},
				Transformer:       store.RawJSON(`{"type":"function"}`),
			},
		},
		Agents: []store.AgentModel{
			{
				ID:        "agent-a",
				Name:      "Agent A",
				ClassName: "class-a",
			},
		},
	}

	if err := db.CreateGuild(guild); err != nil {
		t.Fatalf("Failed to create complex guild: %v", err)
	}

	fetched, err := db.GetGuild("guild-complex")
	if err != nil {
		t.Fatalf("Failed to fetch complex guild: %v", err)
	}

	if len(fetched.Agents) != 1 || fetched.Agents[0].Name != "Agent A" {
		t.Errorf("Agents relationship not loaded/saved correctly")
	}

	if len(fetched.Routes) != 1 || fetched.Routes[0].ID != "route-1" {
		t.Errorf("Routes relationship not loaded/saved correctly")
	}

	if len(fetched.Routes[0].DestinationTopics) != 2 || fetched.Routes[0].DestinationTopics[1] != "topicY" {
		t.Errorf("JSONBStringList unmarshaling failed")
	}

	var transformer map[string]interface{}
	if err := json.Unmarshal([]byte(fetched.Routes[0].Transformer), &transformer); err != nil {
		t.Fatalf("expected transformer to be valid json: %v", err)
	}
	if transformer["type"] != "function" {
		t.Errorf("JSONB transformer unmarshaling failed")
	}
}

func TestStore_NotFoundCases(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	if _, err := db.GetGuild("missing-guild"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound fetching missing guild, got: %v", err)
	}

	if _, err := db.GetGuildByName("missing-name"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound fetching missing guild by name, got: %v", err)
	}

	if err := db.UpdateGuildStatus("missing-guild", store.GuildStatusRunning); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound updating missing guild status, got: %v", err)
	}

	if err := db.DeleteGuild("missing-guild"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound deleting missing guild, got: %v", err)
	}

	if _, err := db.GetAgent("guild-1", "agent-1"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound fetching missing agent, got: %v", err)
	}

	if err := db.UpdateAgentStatus("guild-1", "agent-1", store.AgentStatusRunning); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound updating missing agent status, got: %v", err)
	}

	if err := db.DeleteAgent("guild-1", "agent-1"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound deleting missing agent, got: %v", err)
	}
}

func TestStore_AgentDeleteLifecycle(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guild := &store.GuildModel{ID: "guild-xyz", OrganizationID: "org-1"}
	if err := db.CreateGuild(guild); err != nil {
		t.Fatalf("failed to create guild: %v", err)
	}

	agent := &store.AgentModel{
		ID:        "agent-del",
		GuildID:   pointerStr("guild-xyz"),
		Name:      "Doomed Worker",
		ClassName: "worker_class",
	}

	if err := db.CreateAgent(agent); err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}

	// Double-check it exists
	if _, err := db.GetAgent("guild-xyz", "agent-del"); err != nil {
		t.Fatalf("Failed to fetch agent: %v", err)
	}

	// Delete it
	if err := db.DeleteAgent("guild-xyz", "agent-del"); err != nil {
		t.Fatalf("Failed to delete agent: %v", err)
	}

	// Verify it's gone
	if _, err := db.GetAgent("guild-xyz", "agent-del"); err != store.ErrNotFound {
		t.Errorf("Expected ErrNotFound after Agent deletion, got: %v", err)
	}

	// Verify list is empty
	agents, err := db.ListAgentsByGuild("guild-xyz")
	if err != nil {
		t.Fatalf("Expected no error listing agents, got: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("Expected empty agents list after deletion")
	}
}

func TestStore_ModelDefaults_NoNilCollections(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guild := &store.GuildModel{
		ID:             "guild-defaults",
		Name:           "Defaults Guild",
		OrganizationID: "org-defaults",
	}
	if err := db.CreateGuild(guild); err != nil {
		t.Fatalf("Failed to create guild: %v", err)
	}

	agent := &store.AgentModel{
		ID:        "agent-defaults",
		GuildID:   pointerStr("guild-defaults"),
		Name:      "Defaults Agent",
		ClassName: "test.defaults.Agent",
	}
	if err := db.CreateAgent(agent); err != nil {
		t.Fatalf("Failed to create agent: %v", err)
	}

	fetchedGuild, err := db.GetGuild("guild-defaults")
	if err != nil {
		t.Fatalf("Failed to fetch guild: %v", err)
	}
	if fetchedGuild.BackendConfig == nil {
		t.Fatalf("expected non-nil backend_config map")
	}
	if fetchedGuild.DependencyMap == nil {
		t.Fatalf("expected non-nil dependency_map map")
	}
	if fetchedGuild.Routes == nil {
		t.Fatalf("expected non-nil routes slice")
	}
	if fetchedGuild.Agents == nil {
		t.Fatalf("expected non-nil agents slice")
	}

	fetchedAgent, err := db.GetAgent("guild-defaults", "agent-defaults")
	if err != nil {
		t.Fatalf("Failed to fetch agent: %v", err)
	}
	if fetchedAgent.Properties == nil {
		t.Fatalf("expected non-nil properties map")
	}
	if fetchedAgent.AdditionalTopics == nil {
		t.Fatalf("expected non-nil additional_topics slice")
	}
	if fetchedAgent.DependencyMap == nil {
		t.Fatalf("expected non-nil dependency_map map")
	}
	if fetchedAgent.AdditionalDependencies == nil {
		t.Fatalf("expected non-nil additional_dependencies slice")
	}
	if fetchedAgent.Predicates == nil {
		t.Fatalf("expected non-nil predicates map")
	}
}
