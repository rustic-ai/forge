package store_test

import (
	"encoding/json"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestStore_GuildSpecLifecycle_DBParity(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	spec := buildRichLifecycleSpec("lifecycle-guild-1")
	model := store.FromGuildSpec(spec, "org-lifecycle")

	if err := db.CreateGuild(model); err != nil {
		t.Fatalf("create guild: %v", err)
	}
	defer func() {
		if err := db.PurgeGuild(model); err != nil {
			t.Fatalf("purge guild: %v", err)
		}
	}()

	gotModel, err := db.GetGuild(spec.ID)
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}
	got := store.ToGuildSpec(gotModel)

	assertSpecJSONEqual(t, spec, got)
}

func TestStore_GuildSpecLifecycle_EmptyCollectionsStayNonNil(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	listenToDefault := true
	actOnlyWhenTagged := false
	spec := &protocol.GuildSpec{
		ID:          "lifecycle-guild-empty",
		Name:        "Lifecycle Empty",
		Description: "Ensure []/{} are stable",
		Properties: map[string]interface{}{
			"execution_engine": "rustic_ai.forge.execution_engine.ForgeExecutionEngine",
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		DependencyMap: map[string]protocol.DependencySpec{},
		Agents: []protocol.AgentSpec{
			{
				ID:                     "lifecycle-guild-empty#a-0",
				Name:                   "Empty Agent",
				Description:            "Empty collections",
				ClassName:              "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				AdditionalTopics:       []string{},
				Properties:             map[string]interface{}{},
				ListenToDefaultTopic:   &listenToDefault,
				ActOnlyWhenTagged:      &actOnlyWhenTagged,
				Predicates:             map[string]protocol.RuntimePredicate{},
				DependencyMap:          map[string]protocol.DependencySpec{},
				AdditionalDependencies: []string{},
			},
		},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{
				{
					AgentType:  pointerStr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: pointerStr("unwrap_and_forward_message"),
					Destination: &protocol.RoutingDestination{
						Topics: protocol.TopicsFromSlice([]string{"echo_topic"}),
					},
					RouteTimes: pointerInt(1),
				},
			},
		},
	}

	model := store.FromGuildSpec(spec, "org-lifecycle")
	if err := db.CreateGuild(model); err != nil {
		t.Fatalf("create guild: %v", err)
	}
	defer func() {
		if err := db.PurgeGuild(model); err != nil {
			t.Fatalf("purge guild: %v", err)
		}
	}()

	gotModel, err := db.GetGuild(spec.ID)
	if err != nil {
		t.Fatalf("get guild: %v", err)
	}
	got := store.ToGuildSpec(gotModel)

	if got.DependencyMap == nil {
		t.Fatalf("expected non-nil dependency_map")
	}
	if len(got.Agents) != 1 {
		t.Fatalf("expected one agent, got %d", len(got.Agents))
	}
	agent := got.Agents[0]
	if agent.Properties == nil {
		t.Fatalf("expected non-nil agent properties")
	}
	if agent.AdditionalTopics == nil {
		t.Fatalf("expected non-nil additional_topics")
	}
	if agent.DependencyMap == nil {
		t.Fatalf("expected non-nil agent dependency_map")
	}
	if agent.AdditionalDependencies == nil {
		t.Fatalf("expected non-nil additional_dependencies")
	}
	if agent.Predicates == nil {
		t.Fatalf("expected non-nil predicates")
	}
	if got.Routes == nil || len(got.Routes.Steps) != 1 {
		t.Fatalf("expected one routing step")
	}
}

func buildRichLifecycleSpec(guildID string) *protocol.GuildSpec {
	listenToDefault := false
	actOnlyWhenTagged := true
	routeTimes := 2
	priority := 4
	processStatus := protocol.ProcessStatusCompleted

	return &protocol.GuildSpec{
		ID:          guildID,
		Name:        "Lifecycle Guild",
		Description: "GORM/DTO lifecycle parity",
		Properties: map[string]interface{}{
			"execution_engine": "rustic_ai.forge.execution_engine.ForgeExecutionEngine",
			"messaging": map[string]interface{}{
				"backend_module": "rustic_ai.redis.messaging.backend",
				"backend_class":  "RedisMessagingBackend",
				"backend_config": map[string]interface{}{
					"redis_client": map[string]interface{}{"host": "redis", "port": "6379", "db": 0},
				},
			},
		},
		DependencyMap: map[string]protocol.DependencySpec{
			"llm": {
				ClassName:  "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver",
				Properties: map[string]interface{}{"model": "gpt-4o-mini"},
			},
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"protocol": "file",
					"path":     "/tmp",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:                     guildID + "#a-0",
				Name:                   "Echo Agent",
				Description:            "Echo",
				ClassName:              "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
				AdditionalTopics:       []string{"echo_topic"},
				Properties:             map[string]interface{}{"temperature": 0.2},
				ListenToDefaultTopic:   &listenToDefault,
				ActOnlyWhenTagged:      &actOnlyWhenTagged,
				Predicates:             map[string]protocol.RuntimePredicate{"only_ping": {PredicateType: protocol.PredicateJSONata, Expression: pointerStr("ping")}},
				DependencyMap:          map[string]protocol.DependencySpec{"llm": {ClassName: "rustic_ai.litellm.agent_ext.llm.LiteLLMResolver", Properties: map[string]interface{}{"model": "gpt-4o-mini"}}},
				AdditionalDependencies: []string{"forge-python"},
			},
		},
		Routes: &protocol.RoutingSlip{
			Steps: []protocol.RoutingRule{
				{
					AgentType:  pointerStr("rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"),
					MethodName: pointerStr("unwrap_and_forward_message"),
					Destination: &protocol.RoutingDestination{
						Topics:        protocol.TopicsFromSlice([]string{"echo_topic"}),
						RecipientList: []protocol.AgentTag{{Name: pointerStr("Echo Agent")}},
						Priority:      &priority,
					},
					RouteTimes:       &routeTimes,
					Transformer:      protocol.RawJSON(`{"style":"simple","expression_type":"jsonata","expression":"$.payload"}`),
					AgentStateUpdate: protocol.RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":"{\"k\":\"v\"}"}`),
					GuildStateUpdate: protocol.RawJSON(`{"expression_type":"jsonata","update_format":"json-merge-patch","state_update":"{\"ready\":true}"}`),
					ProcessStatus:    &processStatus,
				},
			},
		},
	}
}

func assertSpecJSONEqual(t *testing.T, want *protocol.GuildSpec, got *protocol.GuildSpec) {
	t.Helper()

	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}

	var wantObj map[string]interface{}
	var gotObj map[string]interface{}
	if err := json.Unmarshal(wantJSON, &wantObj); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if err := json.Unmarshal(gotJSON, &gotObj); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}

	if !deepEqualJSON(wantObj, gotObj) {
		t.Fatalf("guild spec mismatch after DB lifecycle.\nwant=%s\ngot=%s", string(wantJSON), string(gotJSON))
	}
}

func deepEqualJSON(a, b map[string]interface{}) bool {
	aBytes, _ := json.Marshal(a)
	bBytes, _ := json.Marshal(b)
	return string(aBytes) == string(bBytes)
}
