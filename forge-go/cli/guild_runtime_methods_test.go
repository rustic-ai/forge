package cli

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

// --- redis-backed methods (miniredis) ---

func TestGetAgentStatuses(t *testing.T) {
	r, mr := newMiniredisRuntime(t)
	guildID := "g1"
	seedStatus(t, mr, statusKey(guildID, "g1#manager_agent"), `{"state":"running","pid":42}`)
	seedStatus(t, mr, statusKey(guildID, "EchoAgent-abc"), `{"state":"starting","pid":7}`)
	seedStatus(t, mr, statusKey(guildID, "broken"), `not-json`)
	// A key for a different guild must not be returned.
	seedStatus(t, mr, statusKey("other", "x"), `{"state":"running","pid":1}`)

	statuses, err := r.GetAgentStatuses(guildID)
	if err != nil {
		t.Fatalf("GetAgentStatuses: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 valid statuses, got %d (%v)", len(statuses), statuses)
	}
	if s := statuses["g1#manager_agent"]; s.State != "running" || s.PID != 42 {
		t.Errorf("manager status = %+v, want running/42", s)
	}
	if s := statuses["EchoAgent-abc"]; s.State != "starting" || s.PID != 7 {
		t.Errorf("echo status = %+v, want starting/7", s)
	}
}

func TestBuildAgentNameMap_UniqueMatchAndManager(t *testing.T) {
	r, mr := newMiniredisRuntime(t)
	guildID := "g1"
	spec := &protocol.GuildSpec{Name: "Echo", Agents: []protocol.AgentSpec{{Name: "EchoAgent"}}}
	seedStatus(t, mr, statusKey(guildID, guildID+"#manager_agent"), `{"state":"running","pid":1}`)
	seedStatus(t, mr, statusKey(guildID, "EchoAgent-xyz"), `{"state":"running","pid":2}`)

	r.buildAgentNameMap(guildID, spec)

	if got := r.GetAgentName(guildID + "#manager_agent"); got != "Echo Manager" {
		t.Errorf("manager name = %q, want %q", got, "Echo Manager")
	}
	if got := r.GetAgentName("EchoAgent-xyz"); got != "EchoAgent" {
		t.Errorf("agent name = %q, want EchoAgent (unique substring match)", got)
	}
}

func TestBuildAgentNameMap_AmbiguousFallsBackToID(t *testing.T) {
	r, mr := newMiniredisRuntime(t)
	guildID := "g1"
	// Both agent names are substrings of the runtime ID -> ambiguous -> no mapping.
	spec := &protocol.GuildSpec{Name: "G", Agents: []protocol.AgentSpec{{Name: "Foo"}, {Name: "Bar"}}}
	seedStatus(t, mr, statusKey(guildID, "FooBar-1"), `{"state":"running","pid":1}`)

	r.buildAgentNameMap(guildID, spec)

	if got := r.GetAgentName("FooBar-1"); got != "FooBar-1" {
		t.Errorf("ambiguous agent should fall back to raw ID, got %q", got)
	}
}

func TestGetAgentName_Fallback(t *testing.T) {
	r, _ := newMiniredisRuntime(t)
	r.agentNames["known"] = "Friendly"
	if got := r.GetAgentName("known"); got != "Friendly" {
		t.Errorf("got %q, want Friendly", got)
	}
	if got := r.GetAgentName("unknown"); got != "unknown" {
		t.Errorf("got %q, want raw id unknown", got)
	}
}

func TestWaitForGuildRunning(t *testing.T) {
	r, mr := newMiniredisRuntime(t)
	guildID := "g1"
	seedStatus(t, mr, statusKey(guildID, "a1"), `{"state":"running","pid":1}`)

	if err := r.waitForGuildRunning(guildID, 2*time.Second); err != nil {
		t.Errorf("expected running guild to succeed, got %v", err)
	}

	// No statuses + an already-elapsed deadline -> immediate timeout error.
	if err := r.waitForGuildRunning("empty", time.Nanosecond); err == nil {
		t.Error("expected timeout error for a guild with no running agents")
	}
}

func TestPublishMessage(t *testing.T) {
	r, _ := newMiniredisRuntime(t)
	msg, err := BuildChatMessage("u", "U", "hi", "t")
	if err != nil {
		t.Fatalf("BuildChatMessage: %v", err)
	}
	if err := r.PublishMessage("g1", "default_topic", msg); err != nil {
		t.Errorf("PublishMessage: %v", err)
	}
}

func TestGetMessagingBackend(t *testing.T) {
	r, _ := newMiniredisRuntime(t)
	backend, err := r.getMessagingBackend()
	if err != nil {
		t.Fatalf("getMessagingBackend: %v", err)
	}
	if backend == nil {
		t.Error("expected non-nil messaging backend")
	}
}

// --- HTTP-backed methods (httptest) ---

func TestPostJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	})
	mux.HandleFunc("/bad", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`boom`))
	})
	r := newHTTPRuntime(t, mux)

	var result map[string]any
	if err := r.postJSON(r.rusticBase+"/ok", map[string]any{"x": 1}, &result); err != nil {
		t.Fatalf("postJSON /ok: %v", err)
	}
	if result["id"] != "abc" {
		t.Errorf("decoded result = %v, want id=abc", result)
	}

	err := r.postJSON(r.rusticBase+"/bad", map[string]any{}, nil)
	if err == nil || !contains(err.Error(), "HTTP 400") {
		t.Errorf("expected HTTP 400 error, got %v", err)
	}

	// Transport error: nothing listening on this address.
	if err := r.postJSON("http://127.0.0.1:1/x", map[string]any{}, nil); err == nil {
		t.Error("expected transport error for unreachable host")
	}
}

func TestCreateAndLaunchBlueprint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/blueprints/", func(w http.ResponseWriter, req *http.Request) {
		// createBlueprint POSTs to /catalog/blueprints/ ; launch POSTs to
		// /catalog/blueprints/<id>/guilds — both handled by this prefix.
		if req.URL.Path == "/catalog/blueprints/" {
			_, _ = w.Write([]byte(`{"id":"bp-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"guild-1"}`))
	})
	r := newHTTPRuntime(t, mux)

	bp, err := r.createBlueprint(&protocol.GuildSpec{Name: "N", Description: "d"})
	if err != nil {
		t.Fatalf("createBlueprint: %v", err)
	}
	if bp["id"] != "bp-1" {
		t.Fatalf("blueprint id = %v, want bp-1", bp["id"])
	}

	guildID, err := r.launchFromBlueprint("bp-1", "N")
	if err != nil {
		t.Fatalf("launchFromBlueprint: %v", err)
	}
	if guildID != "guild-1" {
		t.Errorf("guild id = %q, want guild-1", guildID)
	}
}

func TestLaunchGuild(t *testing.T) {
	// LaunchGuild needs both HTTP (blueprint create/launch) and redis
	// (waitForGuildRunning / buildAgentNameMap), so wire a runtime with both.
	mr, cancel := newMiniredis(t)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/blueprints/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/catalog/blueprints/" {
			_, _ = w.Write([]byte(`{"id":"bp-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"guild-1"}`))
	})
	r := newHTTPRuntime(t, mux)
	r.redisClient = newRedisClient(t, mr)
	// Pre-seed the launched guild's agent as running so waitForGuildRunning
	// returns immediately.
	seedStatus(t, mr, statusKey("guild-1", "guild-1#manager_agent"), `{"state":"running","pid":1}`)

	spec := &protocol.GuildSpec{Name: "Echo", Agents: []protocol.AgentSpec{{Name: "EchoAgent"}}}
	guildID, err := r.LaunchGuild(spec)
	if err != nil {
		t.Fatalf("LaunchGuild: %v", err)
	}
	if guildID != "guild-1" {
		t.Errorf("guild id = %q, want guild-1", guildID)
	}
}

func TestSeedAgentRegistry(t *testing.T) {
	var posted int
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/agents", func(w http.ResponseWriter, _ *http.Request) {
		posted++
		w.WriteHeader(http.StatusCreated)
	})
	r := newHTTPRuntime(t, mux)

	registry := "entries:\n" +
		"  - id: AgentA\n    class_name: pkg.AgentA\n    description: a\n" +
		"  - id: AgentB\n    class_name: pkg.AgentB\n    description: b\n" +
		"  - id: NoClass\n    description: skipped\n" // no class_name -> skipped
	r.config.AgentRegistry = writeTemp(t, "registry.yaml", registry)

	if err := r.seedAgentRegistry(); err != nil {
		t.Fatalf("seedAgentRegistry: %v", err)
	}
	if posted != 2 {
		t.Errorf("posted %d agents, want 2 (entry without class_name skipped)", posted)
	}
}

func TestSeedAgentRegistry_DuplicateTolerated(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/agents", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict) // 409 -> tolerated, not an error
	})
	r := newHTTPRuntime(t, mux)
	r.config.AgentRegistry = writeTemp(t, "registry.yaml",
		"entries:\n  - id: A\n    class_name: pkg.A\n    description: a\n")

	if err := r.seedAgentRegistry(); err != nil {
		t.Errorf("409 conflicts should be tolerated, got %v", err)
	}
}

func TestWaitForReady(t *testing.T) {
	r := newHTTPRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/readyz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	if err := r.waitForReady(2 * time.Second); err != nil {
		t.Errorf("waitForReady: %v", err)
	}
}

func TestWaitForReady_Timeout(t *testing.T) {
	r := newHTTPRuntime(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	if err := r.waitForReady(250 * time.Millisecond); err == nil {
		t.Error("expected timeout error when server never reports ready")
	}
}

// --- pure / filesystem methods ---

func TestNewGuildRuntime_Defaults(t *testing.T) {
	r, err := NewGuildRuntime(RuntimeConfig{})
	if err != nil {
		t.Fatalf("NewGuildRuntime: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	if r.config.Backend != "nats" || r.config.OrgID != "local-dev" ||
		r.config.UserID != "test-user" || r.config.SupervisorType != "process" {
		t.Errorf("defaults not applied: %+v", r.config)
	}
	if _, err := os.Stat(r.dataDir); err != nil {
		t.Errorf("data dir not created: %v", err)
	}
}

func TestReserveLocalAddr(t *testing.T) {
	a, err := reserveLocalAddr()
	if err != nil {
		t.Fatalf("reserveLocalAddr: %v", err)
	}
	b, err := reserveLocalAddr()
	if err != nil {
		t.Fatalf("reserveLocalAddr: %v", err)
	}
	if a == "" || b == "" {
		t.Error("expected non-empty addresses")
	}
	if a == b {
		t.Errorf("expected distinct addresses, both = %s", a)
	}
}

func TestShutdown_NoServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir, err := os.MkdirTemp("", "shutdown-test-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	r := &GuildRuntime{ctx: ctx, cancel: cancel, tempDir: dir}

	if err := r.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected temp dir removed, stat err = %v", err)
	}
}

// contains reports whether substr is within s (avoids importing strings just
// for this file).
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return len(substr) == 0
}
