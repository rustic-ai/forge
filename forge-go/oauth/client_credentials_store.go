package oauth

import (
	"fmt"
	"sync"
	"time"
)

// clientPrefix namespaces client-credential keychain entries so they do not
// collide with token entries (which use the "oauth:" prefix).
const clientPrefix = "oauth-client:"

func clientStoreKey(providerID string) string {
	return clientPrefix + providerID
}

// clientCredentials holds an OAuth2 client_id/client_secret pair. For providers
// using Dynamic Client Registration (RFC 7591) these are issued by the provider
// and persisted so they can be reused across reconnects and token refreshes.
type clientCredentials struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	// SecretExpiresAt is when the client_secret expires. Zero means it never
	// expires (RFC 7591 client_secret_expires_at of 0).
	SecretExpiresAt time.Time `json:"secret_expires_at"`
}

// expired reports whether the client_secret has passed its expiry.
func (c *clientCredentials) expired() bool {
	return !c.SecretExpiresAt.IsZero() && time.Now().After(c.SecretExpiresAt)
}

// ClientCredentialsStore persists DCR-issued client credentials keyed by
// providerID. The registered client is global to the deployment (shared across
// all orgs), so credentials are not scoped per org — org isolation lives in the
// token store. It mirrors TokenStore so backends can be swapped independently of
// the token backend.
type ClientCredentialsStore interface {
	SaveCredentials(providerID string, c *clientCredentials) error
	LoadCredentials(providerID string) (*clientCredentials, bool)
	DeleteCredentials(providerID string) bool
}

// NewClientCredentialsStore creates a ClientCredentialsStore by name, matching
// the backends supported by NewTokenStore ("memory" or "keychain").
func NewClientCredentialsStore(kind string) (ClientCredentialsStore, error) {
	switch kind {
	case "", "memory":
		return NewInMemoryClientCredentialsStore(), nil
	case "keychain":
		return NewKeychainClientCredentialsStore(), nil
	default:
		return nil, fmt.Errorf("unknown oauth client credentials store %q; supported: memory, keychain", kind)
	}
}

// InMemoryClientCredentialsStore is the default in-process credentials store.
// Credentials are lost on restart, forcing re-registration.
type InMemoryClientCredentialsStore struct {
	mu    sync.Mutex
	creds map[string]*clientCredentials
}

func NewInMemoryClientCredentialsStore() *InMemoryClientCredentialsStore {
	return &InMemoryClientCredentialsStore{creds: make(map[string]*clientCredentials)}
}

func (s *InMemoryClientCredentialsStore) SaveCredentials(providerID string, c *clientCredentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[clientStoreKey(providerID)] = c
	return nil
}

func (s *InMemoryClientCredentialsStore) LoadCredentials(providerID string) (*clientCredentials, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[clientStoreKey(providerID)]
	return c, ok
}

func (s *InMemoryClientCredentialsStore) DeleteCredentials(providerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := clientStoreKey(providerID)
	_, ok := s.creds[key]
	delete(s.creds, key)
	return ok
}
