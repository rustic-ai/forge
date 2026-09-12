package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// varBlueprintSpec mirrors the rustic-ai Python API test
// (api/tests/catalog/test_blueprint_with_vars.py): a guild spec carrying a
// configuration bag + schema and {{ }} mustache placeholders in the agent
// name/properties and in a routing step. Launching it must substitute the
// configuration values into those placeholders.
func varBlueprintSpec() store.JSONB {
	return store.JSONB{
		"name":        "vars_guild",
		"description": "guild with templated agents",
		"configuration_schema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent1":        map[string]any{"type": "string"},
				"model_id":      map[string]any{"type": "string"},
				"model_version": map[string]any{"type": "number"},
			},
		},
		"configuration": map[string]any{
			"agent1":        "asdf",
			"model_id":      "custom/model_1",
			"model_version": 2,
		},
		"agents": []any{
			map[string]any{
				"name":        "{{ agent1 }}",
				"description": "A simple agent with variable properties",
				"class_name":  "test.agents.SimpleAgentWithProps",
				"properties": map[string]any{
					"prop1": "{{ model_id }}",
					"prop2": "{{ model_version }}",
				},
			},
		},
		"routes": map[string]any{
			"steps": []any{
				map[string]any{
					"agent":         map[string]any{"name": "{{ agent1 }}"},
					"origin_filter": map[string]any{"origin_message_format": "__main__.FilteringMessage"},
				},
			},
		},
	}
}

// TestLaunchBlueprint_RendersConfiguration replicates the Python blueprint-vars
// test and expands it to the aspects that test never asserted: it reads the
// launched guild back and checks that the {{ }} placeholders were actually
// rendered in the agent name, agent properties, AND the routing step (the
// Python test only checked HTTP status). It also covers a per-launch config
// override and rejection of an ill-typed configuration value.
func TestLaunchBlueprint_RendersConfiguration(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}

	// Blueprint create validation requires the agent class to exist in the catalog.
	if err := db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "test.agents.SimpleAgentWithProps",
		AgentName:          "SimpleAgentWithProps",
		AgentDoc:           ptrString("simple agent with props"),
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
	}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	registryPath := filepath.Join(t.TempDir(), "agent-registry.yaml")
	if err := os.WriteFile(registryPath, []byte(`entries:
  - id: SimpleAgentWithProps
    class_name: test.agents.SimpleAgentWithProps
    runtime: binary
    executable: test-agent
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_AGENT_REGISTRY", registryPath)

	mux := http.NewServeMux()
	authority := RegisterCatalogRoutes(mux, db)

	// --- create the blueprint via the HTTP endpoint ---
	createBody, _ := json.Marshal(BlueprintCreateRequest{
		Name:        "vars bp",
		Description: "templated",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author-1",
		Spec:        varBlueprintSpec(),
	})
	req, _ := http.NewRequest("POST", "/catalog/blueprints", bytes.NewBuffer(createBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create blueprint: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// The blueprint is a template: it must be stored RAW, placeholders intact.
	storedBP, err := db.GetBlueprint(created.ID)
	if err != nil {
		t.Fatalf("get blueprint: %v", err)
	}
	if raw, _ := json.Marshal(storedBP.Spec); !strings.Contains(string(raw), "{{ agent1 }}") {
		t.Errorf("stored blueprint should keep raw placeholder {{ agent1 }}, got: %s", raw)
	}

	// launch performs a blueprint launch with the given per-request configuration
	// override and returns the launched guild spec read back from the store.
	launch := func(t *testing.T, cfg map[string]any) *protocol.GuildSpec {
		t.Helper()
		guildID := "guild-" + strings.ReplaceAll(t.Name(), "/", "-")
		launchRequest := LaunchGuildFromBlueprintRequest{
			GuildID:       &guildID,
			GuildName:     "Launched Guild",
			UserID:        "user-1",
			OrgID:         "org-1",
			Configuration: cfg,
		}
		body, _ := json.Marshal(launchRequest)
		preflightReq, _ := http.NewRequest("POST", "/catalog/blueprints/"+created.ID+"/guilds/preflight", bytes.NewBuffer(body))
		preflightReq.Header.Set("Content-Type", "application/json")
		preflightRecorder := httptest.NewRecorder()
		mux.ServeHTTP(preflightRecorder, preflightReq)
		if preflightRecorder.Code != http.StatusOK {
			t.Fatalf("preflight: want 200, got %d: %s", preflightRecorder.Code, preflightRecorder.Body.String())
		}
		var preflight LaunchPreflightResponse
		if err := json.NewDecoder(preflightRecorder.Body).Decode(&preflight); err != nil {
			t.Fatal(err)
		}
		launchRequest.PreflightID = preflight.ID
		launchRequest.Fingerprint = preflight.Fingerprint
		authorizePreparedLaunchForTest(authority, created.ID, &launchRequest)
		body, _ = json.Marshal(launchRequest)
		lreq, _ := http.NewRequest("POST", "/catalog/blueprints/"+created.ID+"/guilds", bytes.NewBuffer(body))
		lreq.Header.Set("Content-Type", "application/json")
		lrr := httptest.NewRecorder()
		mux.ServeHTTP(lrr, lreq)
		if lrr.Code != http.StatusCreated {
			t.Fatalf("launch: want 201, got %d: %s", lrr.Code, lrr.Body.String())
		}
		var launched struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(lrr.Body).Decode(&launched); err != nil {
			t.Fatalf("decode launch response: %v", err)
		}
		gm, err := db.GetGuild(launched.ID)
		if err != nil {
			t.Fatalf("get launched guild: %v", err)
		}
		return store.ToGuildSpec(gm)
	}

	routeAgentName := func(t *testing.T, gs *protocol.GuildSpec) string {
		t.Helper()
		if gs.Routes == nil || len(gs.Routes.Steps) != 1 {
			t.Fatalf("want exactly one routing step, got %+v", gs.Routes)
		}
		ag := gs.Routes.Steps[0].Agent
		if ag == nil || ag.Name == nil {
			t.Fatalf("routing step is missing an agent name: %+v", gs.Routes.Steps[0])
		}
		return *ag.Name
	}

	t.Run("default configuration is rendered", func(t *testing.T) {
		gs := launch(t, nil)
		if len(gs.Agents) != 1 {
			t.Fatalf("want one agent, got %d", len(gs.Agents))
		}
		a := gs.Agents[0]
		if a.Name != "asdf" {
			t.Errorf("agent name = %q, want rendered %q (blueprint configuration not applied at launch)", a.Name, "asdf")
		}
		if a.Properties["prop1"] != "custom/model_1" {
			t.Errorf("prop1 = %v, want rendered %q", a.Properties["prop1"], "custom/model_1")
		}
		if s := fmt.Sprint(a.Properties["prop2"]); strings.Contains(s, "{{") {
			t.Errorf("prop2 = %q, want rendered value (placeholder still present)", s)
		}
		if got := routeAgentName(t, gs); got != "asdf" {
			t.Errorf("routing step agent name = %q, want rendered %q", got, "asdf")
		}
	})

	t.Run("per-launch override is rendered", func(t *testing.T) {
		gs := launch(t, map[string]any{
			"agent1":        "007",
			"model_id":      "custom/secret_model",
			"model_version": 3,
		})
		a := gs.Agents[0]
		if a.Name != "007" {
			t.Errorf("agent name = %q, want rendered %q", a.Name, "007")
		}
		if a.Properties["prop1"] != "custom/secret_model" {
			t.Errorf("prop1 = %v, want rendered %q", a.Properties["prop1"], "custom/secret_model")
		}
		if got := routeAgentName(t, gs); got != "007" {
			t.Errorf("routing step agent name = %q, want rendered %q", got, "007")
		}
	})

	t.Run("configuration values use JSON rather than HTML escaping", func(t *testing.T) {
		prompt := "R&D <team> says \"quoted\" at C:\\models\\new\nNext line"
		gs := launch(t, map[string]any{
			"agent1":        prompt,
			"model_id":      "m",
			"model_version": 1,
		})
		if gs.Agents[0].Name != prompt {
			t.Errorf("agent name = %q, want verbatim %q", gs.Agents[0].Name, prompt)
		}
	})

	t.Run("ill-typed configuration override is rejected", func(t *testing.T) {
		guildID := "invalid-configuration"
		body, _ := json.Marshal(LaunchGuildFromBlueprintRequest{
			GuildID:       &guildID,
			GuildName:     "Launched Guild",
			UserID:        "user-1",
			OrgID:         "org-1",
			Configuration: map[string]any{"agent1": 7}, // schema says string
		})
		lreq, _ := http.NewRequest("POST", "/catalog/blueprints/"+created.ID+"/guilds/preflight", bytes.NewBuffer(body))
		lreq.Header.Set("Content-Type", "application/json")
		lrr := httptest.NewRecorder()
		mux.ServeHTTP(lrr, lreq)
		if lrr.Code != http.StatusUnprocessableEntity {
			t.Errorf("ill-typed config: want 422, got %d: %s", lrr.Code, lrr.Body.String())
		}
	})
}
