package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// pendingFlow holds in-progress OAuth state between authorize and callback.
type pendingFlow struct {
	userID       string
	providerID   string
	clientID     string
	clientSecret string
	codeVerifier string
	redirectURL  string
	expiresAt    time.Time
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
}

// Manager handles OAuth2 flows and token storage for all configured providers.
type Manager struct {
	mu           sync.Mutex
	providers    map[string]ProviderConfig
	pendingFlows map[string]*pendingFlow
	store        TokenStore
}

// NewManager constructs a Manager using the default in-memory token store.
func NewManager(cfg *ProvidersConfig) *Manager {
	return NewManagerWithStore(cfg, NewInMemoryTokenStore())
}

// NewManagerWithStore constructs a Manager with a custom token store.
func NewManagerWithStore(cfg *ProvidersConfig, store TokenStore) *Manager {
	return &Manager{
		providers:    cfg.Providers,
		pendingFlows: make(map[string]*pendingFlow),
		store:        store,
	}
}

// GetAuthURL begins an OAuth2 PKCE flow for the given provider. It returns the
// authorization URL to open in the browser and the opaque state token.
func (m *Manager) GetAuthURL(userID, providerID, clientID, clientSecret, redirectURL string) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.providers[providerID]
	if !ok {
		return "", "", fmt.Errorf("unknown provider: %s", providerID)
	}

	endpoint, err := resolveEndpoint(providerID, cfg)
	if err != nil {
		return "", "", err
	}

	if redirectURL == "" {
		redirectURL = cfg.RedirectURL
	}

	state, err := randomString(32)
	if err != nil {
		return "", "", fmt.Errorf("generating state: %w", err)
	}

	var verifier string
	if cfg.pkce() {
		verifier = oauth2.GenerateVerifier()
	}

	m.cleanExpiredFlows()
	m.pendingFlows[state] = &pendingFlow{
		userID:       userID,
		providerID:   providerID,
		clientID:     clientID,
		clientSecret: clientSecret,
		codeVerifier: verifier,
		redirectURL:  redirectURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}

	oc := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     endpoint,
		Scopes:       cfg.Scopes,
		RedirectURL:  redirectURL,
	}

	var authOpts []oauth2.AuthCodeOption
	if cfg.pkce() {
		authOpts = append(authOpts, oauth2.S256ChallengeOption(verifier))
	}
	return oc.AuthCodeURL(state, authOpts...), state, nil
}

// ExchangeCode validates the state from a callback and exchanges the code for tokens.
func (m *Manager) ExchangeCode(ctx context.Context, code, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	flow, ok := m.pendingFlows[state]
	if !ok {
		return fmt.Errorf("unknown or expired state")
	}
	delete(m.pendingFlows, state)

	if time.Now().After(flow.expiresAt) {
		return fmt.Errorf("auth flow expired")
	}

	cfg, ok := m.providers[flow.providerID]
	if !ok {
		return fmt.Errorf("unknown provider: %s", flow.providerID)
	}

	endpoint, err := resolveEndpoint(flow.providerID, cfg)
	if err != nil {
		return err
	}

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
		return fmt.Errorf("exchanging code: %w", err)
	}

	return m.store.Save(flow.userID, flow.providerID, &tokenEntry{
		token:        token,
		clientID:     flow.clientID,
		clientSecret: flow.clientSecret,
		endpoint:     endpoint,
		scopes:       cfg.Scopes,
	})
}

// GetAccessToken returns a valid access token for the provider, refreshing it
// if it will expire within 60 seconds.
func (m *Manager) GetAccessToken(ctx context.Context, userID, providerID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.store.Load(userID, providerID)
	if !ok {
		return "", fmt.Errorf("provider %q not connected for user %q", providerID, userID)
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
	_ = m.store.Save(userID, providerID, entry)
	return newToken.AccessToken, nil
}

// Disconnect removes stored tokens for the provider. Returns true if the
// provider was connected.
func (m *Manager) Disconnect(userID, providerID string) bool {
	return m.store.Delete(userID, providerID)
}

// ListProviders returns the status of all configured providers for the given
// user. callbackBaseURL is used to compute the per-provider redirect URL shown
// when registering the app with the third-party provider.
func (m *Manager) ListProviders(userID, callbackBaseURL string) []ProviderStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	userTokens := m.store.LoadAllForUser(userID)
	out := make([]ProviderStatus, 0, len(m.providers))
	for id, cfg := range m.providers {
		entry, connected := userTokens[id]
		ps := ProviderStatus{
			ID:          id,
			DisplayName: cfg.DisplayName,
			Description: cfg.Description,
			Connected:   connected,
			Scopes:      cfg.Scopes,
			CallbackURL: callbackBaseURL + "/oauth/providers/" + id + "/callback",
		}
		if connected && !entry.token.Expiry.IsZero() {
			exp := entry.token.Expiry
			ps.ExpiresAt = &exp
		}
		out = append(out, ps)
	}
	return out
}

// IsConnected reports whether the user has a stored token for the provider.
func (m *Manager) IsConnected(userID, providerID string) bool {
	_, ok := m.store.Load(userID, providerID)
	return ok
}

// ProviderExists reports whether providerID is in the config.
func (m *Manager) ProviderExists(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.providers[id]
	return ok
}

// ProviderDisplayName returns the display name for a provider.
func (m *Manager) ProviderDisplayName(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providers[id].DisplayName
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
