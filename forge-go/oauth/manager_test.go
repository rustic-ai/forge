package oauth

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
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

	authURL, state, err := m.GetAuthURL(context.Background(), "org1", "github", "client-id", "client-secret", "https://example.com/callback")
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

	_, state, err := m.GetAuthURL(context.Background(), "org1", "github", "client-id", "client-secret", "https://example.com/callback")
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

// seedDiscovery pre-populates the discoverer cache so GetAuthURL resolves
// endpoints without network I/O.
func (m *Manager) seedDiscovery(resourceURL string, r *resolvedProvider) {
	r.fetchedAt = time.Now()
	m.disco.mu.Lock()
	m.disco.cache[resourceURL] = r
	m.disco.mu.Unlock()
}

func TestGetAuthURL_DCRDiscoversAndUsesRegisteredClient(t *testing.T) {
	// use_pkce is unset; a discovered public client must force PKCE on.
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"mcp": {
				DisplayName: "MCP",
				ResourceURL: "https://mcp.example.com/mcp",
				UseDCRP:     true,
			},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("mcp", nil)
	// Seed discovery and stored client credentials so GetAuthURL performs no
	// network I/O: resolve() hits the cache and registerIfNeeded() finds creds.
	m.seedDiscovery("https://mcp.example.com/mcp", &resolvedProvider{
		endpoint:    oauth2.Endpoint{AuthURL: "https://as.example.com/authorize", TokenURL: "https://as.example.com/token"},
		authMethods: []string{"none"}, // public client
	})
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})

	authURL, state, err := m.GetAuthURL(context.Background(), "org1", "mcp", "", "", "https://example.com/callback")
	if err != nil {
		t.Fatalf("GetAuthURL failed: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://as.example.com/authorize") {
		t.Errorf("expected discovered auth endpoint, got %s", authURL)
	}

	m.mu.Lock()
	flow := m.pendingFlows[state]
	m.mu.Unlock()
	if flow == nil {
		t.Fatal("expected pending flow to exist")
	}
	if flow.clientID != "registered-id" {
		t.Errorf("expected registered client id, got %q", flow.clientID)
	}
	// Public client must use PKCE regardless of the (unset) use_pkce default.
	if flow.codeVerifier == "" || !strings.Contains(authURL, "code_challenge") {
		t.Error("expected PKCE to be forced on for a public DCR client")
	}
}

func TestCallbackURL(t *testing.T) {
	base := "https://forge.example.com/api"
	// A single constant callback for every provider (flow identified by state).
	if got := callbackURL(base); got != base+"/oauth/callback" {
		t.Errorf("callback = %q", got)
	}
}

func TestDisconnect_KeepsGlobalDCRClient(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{
			"mcp": {DisplayName: "MCP", ResourceURL: "https://mcp.example.com/mcp", UseDCRP: true},
		},
	}
	m := NewManager(cfg)
	m.CheckAndUpdateProvider("mcp", nil)

	// A registered (global) client and an org's token both exist.
	_ = m.credStore.SaveCredentials("mcp", &clientCredentials{ClientID: "registered-id"})
	m.SeedToken("org1", "mcp", "tok")

	if !m.Disconnect("org1", "mcp") {
		t.Fatal("expected Disconnect to report a removed token")
	}
	// Token is gone...
	if m.IsConnected("org1", "mcp") {
		t.Error("expected org token to be removed after Disconnect")
	}
	// ...but the deployment-global client is retained for other orgs.
	if _, ok := m.credStore.LoadCredentials("mcp"); !ok {
		t.Error("expected DCR client credentials to survive Disconnect")
	}
}

func TestLoadProvidersConfig_DCRResourceURLPairing(t *testing.T) {
	cases := map[string]string{
		"dcr without resource_url": `providers:
  broken:
    use_dcrp: true`,
		"resource_url without dcr": `providers:
  broken:
    resource_url: https://mcp.example.com/mcp`,
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir() + "/providers.yaml"
			if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProvidersConfig(path); err == nil {
				t.Errorf("expected validation error for %q, got nil", name)
			}
		})
	}
}

func TestWithDynamicClient(t *testing.T) {
	cfg := &ProvidersConfig{Providers: map[string]ProviderConfig{}}

	// Defaults: name "Forge", uri empty (omitted from registration).
	def := NewManager(cfg)
	if def.clientName != "Forge" || def.clientURI != "" {
		t.Errorf("defaults = (%q, %q), want (%q, %q)", def.clientName, def.clientURI, "Forge", "")
	}

	// Both set.
	set := NewManagerWithStore(cfg, NewInMemoryTokenStore(), WithDynamicClient("Acme", "https://acme.example.com"))
	if set.clientName != "Acme" || set.clientURI != "https://acme.example.com" {
		t.Errorf("set = (%q, %q)", set.clientName, set.clientURI)
	}

	// Empty values are ignored independently: name keeps default, uri stays empty.
	empty := NewManagerWithStore(cfg, NewInMemoryTokenStore(), WithDynamicClient("", ""))
	if empty.clientName != "Forge" || empty.clientURI != "" {
		t.Errorf("empty = (%q, %q), want (%q, %q)", empty.clientName, empty.clientURI, "Forge", "")
	}
}

func TestResolveEndpoint(t *testing.T) {
	// Built-in providers resolve to library endpoints.
	slack, err := resolveEndpoint("slack", ProviderConfig{})
	if err != nil {
		t.Fatalf("slack: %v", err)
	}
	if slack.AuthURL != "https://slack.com/oauth/v2/authorize" {
		t.Errorf("unexpected slack auth URL: %s", slack.AuthURL)
	}

	// Microsoft must be the Azure AD v2.0 common endpoint, not the legacy
	// consumer endpoint.
	ms, err := resolveEndpoint("microsoft", ProviderConfig{})
	if err != nil {
		t.Fatalf("microsoft: %v", err)
	}
	if ms.AuthURL != "https://login.microsoftonline.com/common/oauth2/v2.0/authorize" {
		t.Errorf("unexpected microsoft auth URL: %s", ms.AuthURL)
	}

	// Explicit config overrides the built-in.
	custom, err := resolveEndpoint("github", ProviderConfig{AuthURL: "https://x/a", TokenURL: "https://x/t"})
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if custom.AuthURL != "https://x/a" || custom.TokenURL != "https://x/t" {
		t.Errorf("expected config override, got %+v", custom)
	}

	// Unknown provider with no config is an error.
	if _, err := resolveEndpoint("mystery", ProviderConfig{}); err == nil {
		t.Error("expected error for unknown provider without endpoints")
	}
}

func TestGetAuthURL_UnknownProvider(t *testing.T) {
	cfg := &ProvidersConfig{
		Providers: map[string]ProviderConfig{},
	}
	m := NewManager(cfg)

	_, _, err := m.GetAuthURL(context.Background(), "org1", "unknown", "id", "secret", "")
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
