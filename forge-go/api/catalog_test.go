package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
)

func TestCreateBlueprintHTTP(t *testing.T) {
	// Setup in-memory SQLite store
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	// Valid payload
	reqBody := BlueprintCreateRequest{
		Spec: map[string]interface{}{
			"name":        "Agent Test",
			"description": "Just testing",
		},
		Tags: []string{"web", "search"},
	}

	b, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "/catalog/blueprints", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusCreated {
		t.Errorf("handler returned wrong status code: got %v want %v",
			status, http.StatusCreated)
	}

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if parsed.ID == "" {
		t.Errorf("expected ID, got empty")
	}
}

func TestGetBlueprintHTTP(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	tags, _ := db.CreateOrGetTags([]string{"test-tag"})
	bp := &store.Blueprint{
		Name:        "API Test BP",
		Description: "A description",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author1",
		Tags:        tags,
	}
	created, _ := db.CreateBlueprint(bp)

	req, _ := http.NewRequest("GET", "/catalog/blueprints/"+created.ID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected 200, got %v: %s", status, rr.Body.String())
	}

	var parsed BlueprintDetailsResponse
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if parsed.Name != "API Test BP" {
		t.Errorf("expected name 'API Test BP', got %s", parsed.Name)
	}
	if len(parsed.Tags) != 1 || parsed.Tags[0] != "test-tag" {
		t.Errorf("expected tag 'test-tag', got %v", parsed.Tags)
	}
}

func TestGetBlueprintHTTP_EmptyCollectionsAreArrays(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	created, _ := db.CreateBlueprint(&store.Blueprint{
		Name:        "Empty Collections BP",
		Description: "no tags/commands/prompts",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author-empty",
	})

	req, _ := http.NewRequest("GET", "/catalog/blueprints/"+created.ID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected 200, got %v: %s", status, rr.Body.String())
	}

	var raw map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := raw["starter_prompts"].([]any); !ok {
		t.Fatalf("expected starter_prompts to be JSON array, got %T (%v)", raw["starter_prompts"], raw["starter_prompts"])
	}
	if _, ok := raw["tags"].([]any); !ok {
		t.Fatalf("expected tags to be JSON array, got %T (%v)", raw["tags"], raw["tags"])
	}
	if _, ok := raw["commands"].([]any); !ok {
		t.Fatalf("expected commands to be JSON array, got %T (%v)", raw["commands"], raw["commands"])
	}
}

func TestGetBlueprintForGuildHTTP_ReturnsDetailsShape(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	bp, err := db.CreateBlueprint(&store.Blueprint{
		Name:        "Guild BP",
		Description: "for guild mapping",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author-guild",
	})
	if err != nil {
		t.Fatalf("failed to create blueprint: %v", err)
	}

	guildID := "guild-1"
	if err := db.CreateGuild(&store.GuildModel{
		ID:             guildID,
		Name:           "Guild One",
		Description:    "Guild for blueprint lookup",
		OrganizationID: "org-1",
		Status:         store.GuildStatusRunning,
		DependencyMap:  store.JSONB{},
	}); err != nil {
		t.Fatalf("failed to create guild: %v", err)
	}

	if err := db.AddGuildToBlueprint(bp.ID, guildID); err != nil {
		t.Fatalf("failed to map guild to blueprint: %v", err)
	}

	req, _ := http.NewRequest("GET", "/catalog/guilds/"+guildID+"/blueprints", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected 200, got %v: %s", status, rr.Body.String())
	}

	var parsed BlueprintDetailsResponse
	if err := json.NewDecoder(rr.Body).Decode(&parsed); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if parsed.ID != bp.ID {
		t.Fatalf("expected blueprint id %s, got %s", bp.ID, parsed.ID)
	}
	if parsed.StarterPrompts == nil || parsed.Tags == nil || parsed.Commands == nil {
		t.Fatalf("expected non-nil array fields, got starter_prompts=%v tags=%v commands=%v", parsed.StarterPrompts, parsed.Tags, parsed.Commands)
	}
}

func TestGetAccessibleBlueprintsHTTP(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	// Create some blueprints
	orgID := "org_1"
	_, _ = db.CreateBlueprint(&store.Blueprint{Name: "BP Public", Exposure: store.ExposurePublic, AuthorID: "user_x"})
	_, _ = db.CreateBlueprint(&store.Blueprint{Name: "BP Private U1", Exposure: store.ExposurePrivate, AuthorID: "user_1"})
	_, _ = db.CreateBlueprint(&store.Blueprint{Name: "BP Org 1", Exposure: store.ExposureOrganization, AuthorID: "user_x", OrganizationID: &orgID})

	t.Run("without org_id", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/blueprints/accessible", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Fatalf("expected 200, got %v", status)
		}

		var bps []AccessibleBlueprintResponse
		if err := json.NewDecoder(rr.Body).Decode(&bps); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Should see BP Public and BP Private U1 (2 total)
		if len(bps) != 2 {
			t.Errorf("expected 2 accessible blueprints, got %d", len(bps))
		}

		for _, bp := range bps {
			if bp.AccessOrganizationID != nil {
				t.Errorf("expected access_organization_id to be nil for %s, got %v", bp.Name, *bp.AccessOrganizationID)
			}
		}
	})

	t.Run("with org_id", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/blueprints/accessible?org_id=org_1", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if status := rr.Code; status != http.StatusOK {
			t.Fatalf("expected 200, got %v", status)
		}

		var bps []AccessibleBlueprintResponse
		if err := json.NewDecoder(rr.Body).Decode(&bps); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Should see BP Public, BP Private U1, and BP Org 1 (3 total)
		if len(bps) != 3 {
			t.Errorf("expected 3 accessible blueprints, got %d", len(bps))
		}

		for _, bp := range bps {
			switch bp.Exposure {
			case store.ExposureOrganization:
				if bp.AccessOrganizationID == nil || *bp.AccessOrganizationID != "org_1" {
					t.Errorf("expected access_organization_id=org_1 for %s, got %v", bp.Name, bp.AccessOrganizationID)
				}
			default:
				if bp.AccessOrganizationID != nil {
					t.Errorf("expected access_organization_id=nil for %s, got %v", bp.Name, *bp.AccessOrganizationID)
				}
			}
		}
	})
}

func TestGetGuildsForUserWithStatuses(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	// Create guilds with different statuses
	if err := db.CreateGuild(&store.GuildModel{ID: "g1", Name: "Running Guild", OrganizationID: "org_1", Status: store.GuildStatusRunning}); err != nil {
		t.Fatalf("failed to create g1: %v", err)
	}
	if err := db.CreateGuild(&store.GuildModel{ID: "g2", Name: "Stopped Guild", OrganizationID: "org_1", Status: store.GuildStatusStopped}); err != nil {
		t.Fatalf("failed to create g2: %v", err)
	}
	if err := db.CreateGuild(&store.GuildModel{ID: "g3", Name: "Error Guild", OrganizationID: "org_1", Status: store.GuildStatusError}); err != nil {
		t.Fatalf("failed to create g3: %v", err)
	}

	if err := db.AddUserToGuild("g1", "user_1"); err != nil {
		t.Fatalf("failed to add user_1 to g1: %v", err)
	}
	if err := db.AddUserToGuild("g2", "user_1"); err != nil {
		t.Fatalf("failed to add user_1 to g2: %v", err)
	}
	if err := db.AddUserToGuild("g3", "user_1"); err != nil {
		t.Fatalf("failed to add user_1 to g3: %v", err)
	}

	t.Run("no filter returns all", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/guilds", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var results []map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 guilds, got %d", len(results))
		}
	})

	t.Run("filter by single status", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/guilds?statuses=running", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var results []map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 guild, got %d", len(results))
		}
		if len(results) > 0 && results[0]["name"] != "Running Guild" {
			t.Errorf("expected Running Guild, got %v", results[0]["name"])
		}
	})

	t.Run("filter by multiple statuses", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/guilds?statuses=running,stopped", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}

		var results []map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 guilds, got %d", len(results))
		}
	})

	t.Run("invalid status returns 400", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/users/user_1/guilds?statuses=bogus", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func TestGetGuildsForOrgWithStatuses(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	if err := db.CreateGuild(&store.GuildModel{ID: "g1", Name: "Running", OrganizationID: "org_1", Status: store.GuildStatusRunning}); err != nil {
		t.Fatalf("failed to create g1: %v", err)
	}
	if err := db.CreateGuild(&store.GuildModel{ID: "g2", Name: "Stopped", OrganizationID: "org_1", Status: store.GuildStatusStopped}); err != nil {
		t.Fatalf("failed to create g2: %v", err)
	}

	t.Run("no filter returns all", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/organizations/org_1/guilds", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}

		var results []map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 guilds, got %d", len(results))
		}
	})

	t.Run("filter by status", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/organizations/org_1/guilds?statuses=stopped", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}

		var results []map[string]interface{}
		if err := json.NewDecoder(rr.Body).Decode(&results); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("expected 1 guild, got %d", len(results))
		}
	})

	t.Run("invalid status returns 400", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/catalog/organizations/org_1/guilds?statuses=invalid", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", rr.Code)
		}
	})
}

func TestListBlueprintsHTTP(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)

	_, _ = db.CreateBlueprint(&store.Blueprint{
		Name:        "BP One",
		Description: "desc one",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author_1",
	})
	_, _ = db.CreateBlueprint(&store.Blueprint{
		Name:        "BP Two",
		Description: "desc two",
		Exposure:    store.ExposurePrivate,
		AuthorID:    "author_2",
	})

	req, _ := http.NewRequest("GET", "/catalog/blueprints", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected 200, got %v: %s", status, rr.Body.String())
	}

	var resp []BlueprintInfoResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp) != 2 {
		t.Fatalf("expected 2 blueprints, got %d", len(resp))
	}
}

func TestListBlueprintsHTTP_TrailingSlashPathNormalization(t *testing.T) {
	db, err := store.NewGormStore("sqlite", "file::memory:")
	if err != nil {
		t.Fatalf("failed to init db: %v", err)
	}

	mux := http.NewServeMux()
	RegisterCatalogRoutes(mux, db)
	handler := WithPathNormalization(mux)

	_, _ = db.CreateBlueprint(&store.Blueprint{
		Name:        "BP One",
		Description: "desc one",
		Exposure:    store.ExposurePublic,
		AuthorID:    "author_1",
	})

	req, _ := http.NewRequest("GET", "/catalog/blueprints/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Fatalf("expected 200, got %v: %s", status, rr.Body.String())
	}
}
