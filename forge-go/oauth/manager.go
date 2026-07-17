package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// builtinEndpoints maps provider IDs to endpoints published by
// golang.org/x/oauth2 so we don't maintain our own copies of these URLs.
//
// These endpoints are hardcoded (not auto-discovered) on purpose. Some of these
// providers publish no OAuth metadata at all; others (e.g. Slack) publish an
// OpenID Connect discovery document whose endpoints are the *login* surface
// (openid/profile/email only) and differ from the OAuth API endpoints used here
// (chat:write, channels:read, ...). Discovering them would resolve the wrong,
// narrower surface. Providers not listed here must set auth_url/token_url
// explicitly, or use resource_url for RFC 9728/8414 auto-discovery.
var builtinEndpoints = map[string]oauth2.Endpoint{
	"github":       endpoints.GitHub,
	"google":       endpoints.Google,
	"google-drive": endpoints.Google,
	"slack":        endpoints.Slack,
	// AzureAD("common") is the Azure AD v2.0 endpoint that handles both work and
	// personal accounts — not the legacy consumer endpoints.Microsoft.
	"microsoft": endpoints.AzureAD("common"),
}

// resolveEndpoint returns the OAuth2 endpoint for a static (non-discovery)
// provider: an explicit auth_url/token_url pair from config takes precedence,
// otherwise a built-in endpoint is used if the provider ID is known.
func resolveEndpoint(providerID string, cfg ProviderConfig) (oauth2.Endpoint, error) {
	if cfg.AuthURL != "" && cfg.TokenURL != "" {
		return oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL}, nil
	}
	if ep, ok := builtinEndpoints[providerID]; ok {
		return ep, nil
	}
	return oauth2.Endpoint{}, fmt.Errorf(
		"provider %q has no known endpoints; set auth_url and token_url, or resource_url for auto-discovery", providerID,
	)
}

// ErrNotConnected is returned by GetAccessToken when no token exists for the
// given (orgID, providerID) pair.
var ErrNotConnected = errors.New("oauth: provider not connected")

// pendingFlow holds in-progress OAuth state between authorize and callback.
type pendingFlow struct {
	orgID        string
	providerID   string
	clientID     string
	clientSecret string
	codeVerifier string
	redirectURL  string
	// endpoint is captured at authorize time so the callback exchange uses the
	// same (possibly discovered) endpoints without re-resolving.
	endpoint  oauth2.Endpoint
	expiresAt time.Time
}

// tokenEntry stores a connected provider's live tokens.
type tokenEntry struct {
	token        *oauth2.Token
	clientID     string
	clientSecret string
	endpoint     oauth2.Endpoint
	scopes       []string
}

// ProviderStatus is the public view of a provider's connection state.
type ProviderStatus struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	Description string     `json:"description,omitempty"`
	Connected   bool       `json:"isConnected"`
	Scopes      []string   `json:"scopes"`
	CallbackURL string     `json:"callbackUrl"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	// RequiresClientCredentials tells the UI whether to prompt for a client_id
	// and client_secret. False for providers using Dynamic Client Registration.
	RequiresClientCredentials bool `json:"requiresClientCredentials"`
}

// Manager handles OAuth2 flows and token storage for all configured providers.
type Manager struct {
	mu              sync.Mutex
	providers       map[string]ProviderConfig
	activeProviders map[string]struct{}
	pendingFlows    map[string]*pendingFlow
	store           TokenStore
	credStore       ClientCredentialsStore
	disco           *discoverer
	httpClient      *http.Client
	// clientName and clientURI are sent as client_name and client_uri during
	// Dynamic Client Registration.
	clientName string
	clientURI  string
}

// NewManager constructs a Manager using the default in-memory token store.
func NewManager(cfg *ProvidersConfig) *Manager {
	return NewManagerWithStore(cfg, NewInMemoryTokenStore())
}

// NewManagerWithStore constructs a Manager with a custom token store and the
// default in-memory client credentials store.
func NewManagerWithStore(cfg *ProvidersConfig, store TokenStore, opts ...ManagerOption) *Manager {
	return NewManagerWithStores(cfg, store, NewInMemoryClientCredentialsStore(), opts...)
}

// ManagerOption configures optional Manager behaviour.
type ManagerOption func(*Manager)

// WithDynamicClient sets the client_name and client_uri sent to the provider
// during Dynamic Client Registration (RFC 7591). client_uri is a URL of a page
// with information about the client, which providers may show on the consent
// screen. Empty values are ignored: the name keeps its default ("Forge") and
// the uri stays omitted from the registration request.
func WithDynamicClient(name, uri string) ManagerOption {
	return func(m *Manager) {
		if name != "" {
			m.clientName = name
		}
		if uri != "" {
			m.clientURI = uri
		}
	}
}

// NewManagerWithStores constructs a Manager with custom token and client
// credentials stores.
func NewManagerWithStores(cfg *ProvidersConfig, store TokenStore, credStore ClientCredentialsStore, opts ...ManagerOption) *Manager {
	hc := &http.Client{Timeout: 30 * time.Second}
	m := &Manager{
		providers:       cfg.Providers,
		activeProviders: make(map[string]struct{}),
		pendingFlows:    make(map[string]*pendingFlow),
		store:           store,
		credStore:       credStore,
		disco:           newDiscoverer(hc),
		httpClient:      hc,
		clientName:      "Forge",
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// GetAuthURL begins an OAuth2 PKCE flow for the given provider. It returns the
// authorization URL to open in the browser and the opaque state token.
//
// For providers using Dynamic Client Registration (UseDCRP), clientID and
// clientSecret may be empty: endpoints are discovered from the provider's
// resource URL and a client is registered on demand.
func (m *Manager) GetAuthURL(ctx context.Context, orgID, providerID, clientID, clientSecret, redirectURL string) (string, string, error) {
	m.mu.Lock()
	_, active := m.activeProviders[providerID]
	cfg := m.providers[providerID]
	m.mu.Unlock()

	if !active {
		return "", "", fmt.Errorf("unknown provider: %s", providerID)
	}

	if redirectURL == "" {
		redirectURL = cfg.RedirectURL
	}

	// Resolve endpoints and, for DCR providers, obtain client credentials.
	// Network I/O happens outside the lock.
	//
	// Endpoint discovery is the Dynamic Client Registration flow: it relies on
	// the provider advertising RFC 9728 protected-resource metadata (in practice
	// an OAuth-protected resource such as a remote MCP server), so resource_url
	// is only used when UseDCRP is set. Everything else resolves static endpoints.
	var endpoint oauth2.Endpoint
	usePKCE := cfg.pkce()

	if cfg.UseDCRP {
		res, err := m.disco.resolve(ctx, cfg.ResourceURL)
		if err != nil {
			return "", "", fmt.Errorf("discovering provider %q: %w", providerID, err)
		}
		endpoint = res.endpoint

		clientID, clientSecret, err = m.registerIfNeeded(ctx, providerID, cfg, res, redirectURL)
		if err != nil {
			return "", "", fmt.Errorf("registering client for %q: %w", providerID, err)
		}
		// A dynamically registered public client has no client_secret, so PKCE is
		// the only thing binding the code to this client: force it on.
		if res.supportsPublicClient() {
			usePKCE = true
		}
	} else {
		ep, err := resolveEndpoint(providerID, cfg)
		if err != nil {
			return "", "", err
		}
		endpoint = ep
	}

	state, err := randomString(32)
	if err != nil {
		return "", "", fmt.Errorf("generating state: %w", err)
	}

	var verifier string
	if usePKCE {
		verifier = oauth2.GenerateVerifier()
	}

	m.mu.Lock()
	m.cleanExpiredFlows()
	m.pendingFlows[state] = &pendingFlow{
		orgID:        orgID,
		providerID:   providerID,
		clientID:     clientID,
		clientSecret: clientSecret,
		codeVerifier: verifier,
		redirectURL:  redirectURL,
		endpoint:     endpoint,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	m.mu.Unlock()

	oc := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoint,
		Scopes:       cfg.Scopes,
		RedirectURL:  redirectURL,
	}

	var authOpts []oauth2.AuthCodeOption
	if usePKCE {
		authOpts = append(authOpts, oauth2.S256ChallengeOption(verifier))
	}
	return oc.AuthCodeURL(state, authOpts...), state, nil
}

// registerIfNeeded returns the deployment-global client credentials for the
// provider, registering a new client via Dynamic Client Registration (RFC 7591)
// when none exist or the stored client_secret has expired. The client is shared
// across all orgs, so it is keyed by providerID only.
func (m *Manager) registerIfNeeded(ctx context.Context, providerID string, cfg ProviderConfig, res *resolvedProvider, redirectURL string) (string, string, error) {
	if creds, ok := m.credStore.LoadCredentials(providerID); ok && !creds.expired() {
		return creds.ClientID, creds.ClientSecret, nil
	}
	if res.registrationEndpoint == "" {
		return "", "", fmt.Errorf("provider %q does not advertise a registration endpoint", providerID)
	}

	meta := &oauthex.ClientRegistrationMetadata{
		RedirectURIs:            []string{redirectURL},
		TokenEndpointAuthMethod: res.tokenEndpointAuthMethod(),
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		ClientName:              m.clientName,
		ClientURI:               m.clientURI,
		Scope:                   strings.Join(cfg.Scopes, " "),
	}
	resp, err := oauthex.RegisterClient(ctx, res.registrationEndpoint, meta, m.httpClient)
	if err != nil {
		return "", "", err
	}

	creds := &clientCredentials{
		ClientID:        resp.ClientID,
		ClientSecret:    resp.ClientSecret,
		SecretExpiresAt: resp.ClientSecretExpiresAt,
	}
	if err := m.credStore.SaveCredentials(providerID, creds); err != nil {
		return "", "", fmt.Errorf("persisting client credentials: %w", err)
	}
	return resp.ClientID, resp.ClientSecret, nil
}

// ExchangeCode validates the state from a callback and exchanges the code for
// tokens. It returns the provider the state resolved to (usable for display even
// on some errors), so the callback need not carry the provider in its URL.
func (m *Manager) ExchangeCode(ctx context.Context, code, state string) (providerID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	flow, ok := m.pendingFlows[state]
	if !ok {
		return "", fmt.Errorf("unknown or expired state")
	}
	delete(m.pendingFlows, state)
	providerID = flow.providerID

	if time.Now().After(flow.expiresAt) {
		return providerID, fmt.Errorf("auth flow expired")
	}

	if _, active := m.activeProviders[flow.providerID]; !active {
		return providerID, fmt.Errorf("unknown provider: %s", flow.providerID)
	}
	cfg := m.providers[flow.providerID]

	// Reuse the endpoint captured at authorize time so DCR-discovered endpoints
	// don't need to be re-resolved here.
	endpoint := flow.endpoint

	oc := &oauth2.Config{
		ClientID:     flow.clientID,
		ClientSecret: flow.clientSecret,
		Endpoint:     endpoint,
		Scopes:       cfg.Scopes,
		RedirectURL:  flow.redirectURL,
	}

	var exchangeOpts []oauth2.AuthCodeOption
	if flow.codeVerifier != "" {
		exchangeOpts = append(exchangeOpts, oauth2.VerifierOption(flow.codeVerifier))
	}
	token, err := oc.Exchange(ctx, code, exchangeOpts...)
	if err != nil {
		return providerID, fmt.Errorf("exchanging code: %w", err)
	}

	return providerID, m.store.Save(flow.orgID, flow.providerID, &tokenEntry{
		token:        token,
		clientID:     flow.clientID,
		clientSecret: flow.clientSecret,
		endpoint:     endpoint,
		scopes:       cfg.Scopes,
	})
}

// GetAccessToken returns a valid access token for the provider, refreshing it
// if it expires within 60 seconds.
func (m *Manager) GetAccessToken(ctx context.Context, orgID, providerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.store.Load(orgID, providerID)
	if !ok {
		return "", fmt.Errorf("provider %q not connected for org %q: %w", providerID, orgID, ErrNotConnected)
	}

	if entry.token.Valid() && time.Until(entry.token.Expiry) > 60*time.Second {
		return entry.token.AccessToken, nil
	}

	ts := (&oauth2.Config{
		ClientID:     entry.clientID,
		ClientSecret: entry.clientSecret,
		Endpoint:     entry.endpoint,
	}).TokenSource(ctx, entry.token)

	newToken, err := ts.Token()
	if err != nil {
		return "", fmt.Errorf("refreshing token: %w", err)
	}
	entry.token = newToken
	if err := m.store.Save(orgID, providerID, entry); err != nil {
		return "", fmt.Errorf("persisting refreshed token: %w", err)
	}
	return newToken.AccessToken, nil
}

// SeedToken stores a non-expiring access token directly, bypassing the OAuth
// flow. Intended for tests and local development only.
func (m *Manager) SeedToken(orgID, providerID, accessToken string) {
	_ = m.store.Save(orgID, providerID, &tokenEntry{
		token: &oauth2.Token{
			AccessToken: accessToken,
			Expiry:      time.Now().Add(24 * time.Hour),
		},
	})
}

// Disconnect removes the org's stored tokens for the provider. It returns true
// if the provider had a stored token.
//
// For providers using Dynamic Client Registration the registered client is
// deployment-global (shared across all orgs), so it is NOT discarded here —
// disconnecting one org must not break others. The client persists for the
// deployment's lifetime and is re-registered automatically only when its
// client_secret expires (see registerIfNeeded).
func (m *Manager) Disconnect(orgID, providerID string) bool {
	return m.store.Delete(orgID, providerID)
}

// ListProviders returns the status of all configured providers for the given
// org. callbackBaseURL is used to compute the per-provider redirect URL shown
// when registering the app with the third-party provider.
// Only providers that have been activated via CheckAndUpdateProvider are returned.
func (m *Manager) ListProviders(orgID, callbackBaseURL string) []ProviderStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]ProviderStatus, 0, len(m.activeProviders))
	for id := range m.activeProviders {
		cfg := m.providers[id]
		entry, connected := m.store.Load(orgID, id)
		ps := ProviderStatus{
			ID:                        id,
			DisplayName:               cfg.DisplayName,
			Description:               cfg.Description,
			Connected:                 connected,
			Scopes:                    cfg.Scopes,
			CallbackURL:               callbackURL(callbackBaseURL),
			RequiresClientCredentials: cfg.RequiresClientCredentials(),
		}
		if connected && !entry.token.Expiry.IsZero() {
			exp := entry.token.Expiry
			ps.ExpiresAt = &exp
		}
		out = append(out, ps)
	}
	return out
}

// IsConnected reports whether the org has a stored token for the provider.
func (m *Manager) IsConnected(orgID, providerID string) bool {
	_, ok := m.store.Load(orgID, providerID)
	return ok
}

// ProviderExists reports whether providerID is active (i.e. registered via CheckAndUpdateProvider).
func (m *Manager) ProviderExists(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.activeProviders[id]
	return ok
}

// CheckAndUpdateProvider checks whether providerID exists, merges the given scopes into
// the provider's scope list, marks the provider as active, and returns true.
// Returns false if the provider is unknown (no state is updated in that case).
func (m *Manager) CheckAndUpdateProvider(providerID string, scopes []string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, ok := m.providers[providerID]
	if !ok {
		return false
	}
	existing := make(map[string]struct{}, len(cfg.Scopes))
	for _, s := range cfg.Scopes {
		existing[s] = struct{}{}
	}
	for _, s := range scopes {
		if _, found := existing[s]; !found {
			cfg.Scopes = append(cfg.Scopes, s)
			existing[s] = struct{}{}
		}
	}
	m.providers[providerID] = cfg
	m.activeProviders[providerID] = struct{}{}
	return true
}

// ProviderDisplayName returns the display name for a provider.
func (m *Manager) ProviderDisplayName(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providers[id].DisplayName
}

// RequiresClientCredentials reports whether the caller must supply a client_id
// and client_secret to start an auth flow for this provider. Providers using
// Dynamic Client Registration require none.
func (m *Manager) RequiresClientCredentials(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providers[id].RequiresClientCredentials()
}

func (m *Manager) cleanExpiredFlows() {
	now := time.Now()
	for k, f := range m.pendingFlows {
		if now.After(f.expiresAt) {
			delete(m.pendingFlows, k)
		}
	}
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
