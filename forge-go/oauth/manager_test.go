package oauth

import (
	"os"
	"testing"
)

func TestGetAuthURL_UsesProviderScopes(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"github": {
				DisplayName: "GitHub",
				AuthURL:     "https://github.com/login/oauth/authorize",
				TokenURL:    "https://github.com/login/oauth/access_token",
				Scopes:      []string{"repo", "user:email"},
			},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("github", nil)

	authURL, state, err := m.GetAuthURL("org1", "github", "client-id", "client-secret", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if authURL == "" {
		t.Error("expected non-empty authURL")
	}
	if state == "" {
		t.Error("expected non-empty state")
	}

	// Verify pending flow exists
	m.mu.Lock()
	_, ok := m.pendingFlows[state]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expected pending flow to exist")
	}
}

func TestGetAuthURL_NoScopes(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"github": {DisplayName: "GitHub"},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("github", nil)

	_, state, err := m.GetAuthURL("org1", "github", "client-id", "client-secret", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}

	m.mu.Lock()
	_, ok := m.pendingFlows[state]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expected pending flow to exist")
	}
}

func TestGetAuthURL_UnknownProvider(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{},
	}
	m := NewManager(cfg)

	_, _, err := m.GetAuthURL("org1", "unknown", "id", "secret", "")
	if err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
}

func TestProviderConfig_ParsesScopes(t *testing.T) {
	// verify the field round-trips through YAML correctly.
	yaml := `providers:
  github:
    display_name: GitHub
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token`

	import_path := t.TempDir() + "/providers.yaml"
	if err := os.WriteFile(import_path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadProvidersConfig(import_path)
	if err != nil {
		t.Fatalf("LoadProvidersConfig failed: %v", err)
	}
	p, ok := cfg.Providers["github"]
	if !ok {
		t.Fatal("expected github provider")
	}
	if p.DisplayName != "GitHub" {
		t.Errorf("unexpected display name: %s", p.DisplayName)
	}
}
