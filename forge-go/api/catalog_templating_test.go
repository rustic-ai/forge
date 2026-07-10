package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

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
		body, _ := json.Marshal(LaunchGuildFromBlueprintRequest{
			GuildName:     "Launched Guild",
			UserID:        "user-1",
			OrgID:         "org-1",
			Configuration: cfg,
		})
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

	t.Run("ill-typed configuration override is rejected", func(t *testing.T) {
		body, _ := json.Marshal(LaunchGuildFromBlueprintRequest{
			GuildName:     "Launched Guild",
			UserID:        "user-1",
			OrgID:         "org-1",
			Configuration: map[string]any{"agent1": 7}, // schema says string
		})
		lreq, _ := http.NewRequest("POST", "/catalog/blueprints/"+created.ID+"/guilds", bytes.NewBuffer(body))
		lreq.Header.Set("Content-Type", "application/json")
		lrr := httptest.NewRecorder()
		mux.ServeHTTP(lrr, lreq)
		if lrr.Code != http.StatusUnprocessableEntity {
			t.Errorf("ill-typed config: want 422, got %d: %s", lrr.Code, lrr.Body.String())
		}
	})
}

// TestLaunchBlueprint_ConfigValueHTMLEscaping documents a bug shared by the Go
// (cbroglie/mustache) and Python (chevron) renderers: because substitution runs
// over a JSON-serialized spec with default mustache HTML-escaping, a config
// value containing & < > is corrupted (e.g. "R&D" -> "R&amp;D"). Skipped until
// the escaping is fixed in BOTH renderers (fixing only one breaks Go/Python
// render parity). See guild.resolveTemplates and core builders._from_spec_dict.
func TestLaunchBlueprint_ConfigValueHTMLEscaping(t *testing.T) {
	t.Skip("known shared bug: mustache/chevron HTML-escape config values (& < >); must be fixed in both renderers for parity")

	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	if err := db.RegisterAgent(&store.CatalogAgentEntry{
		QualifiedClassName: "test.agents.SimpleAgentWithProps",
		AgentName:          "SimpleAgentWithProps",
		AgentPropsSchema:   store.JSONB{"type": "object"},
		MessageHandlers:    store.JSONB{},
	}); err != nil {
		t.Fatalf("register agent: %v", err)
	}
	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	createBody, _ := json.Marshal(BlueprintCreateRequest{
		Name:     "vars bp",
		Exposure: store.ExposurePublic,
		AuthorID: "author-1",
		Spec:     varBlueprintSpec(),
	})
	req, _ := http.NewRequest("POST", "/catalog/blueprints", bytes.NewBuffer(createBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var created struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(rr.Body).Decode(&created)

	body, _ := json.Marshal(LaunchGuildFromBlueprintRequest{
		GuildName:     "Launched Guild",
		UserID:        "user-1",
		OrgID:         "org-1",
		Configuration: map[string]any{"agent1": "R&D <team>", "model_id": "m", "model_version": 1},
	})
	lreq, _ := http.NewRequest("POST", "/catalog/blueprints/"+created.ID+"/guilds", bytes.NewBuffer(body))
	lreq.Header.Set("Content-Type", "application/json")
	lrr := httptest.NewRecorder()
	mux.ServeHTTP(lrr, lreq)
	var launched struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(lrr.Body).Decode(&launched)
	gm, _ := db.GetGuild(launched.ID)
	gs := store.ToGuildSpec(gm)

	// The value must survive verbatim, NOT HTML-escaped to "R&amp;D &lt;team&gt;".
	if gs.Agents[0].Name != "R&D <team>" {
		t.Errorf("agent name = %q, want verbatim %q (HTML-escaping corruption)", gs.Agents[0].Name, "R&D <team>")
	}
}
