package oauth

import (
	"fmt"
	"strings"
	"sync"
)

// TokenStore persists OAuth token entries keyed by (userID, providerID).
// Implement this interface to swap in keychain, encrypted DB, or other backends.
type TokenStore interface {
	Save(userID, providerID string, entry *tokenEntry) error
	Load(userID, providerID string) (*tokenEntry, bool)
	Delete(userID, providerID string) bool
	LoadAllForUser(userID string) map[string]*tokenEntry
}

// NewTokenStore creates a TokenStore by name. Currently only "memory" (the
// default) is supported. Future backends (e.g. "keychain", "encrypted-db")
// add a case here.
func NewTokenStore(kind string) (TokenStore, error) {
	switch kind {
	case "", "memory":
		return NewInMemoryTokenStore(), nil
	default:
		return nil, fmt.Errorf("unknown oauth token store %q; supported: memory", kind)
	}
}

// InMemoryTokenStore is the default in-process token store. Tokens are lost
// on server restart.
type InMemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]*tokenEntry // key: "userID:providerID"
}

func NewInMemoryTokenStore() *InMemoryTokenStore {
	return &InMemoryTokenStore{tokens: make(map[string]*tokenEntry)}
}

func (s *InMemoryTokenStore) Save(userID, providerID string, entry *tokenEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[storeKey(userID, providerID)] = entry
	return nil
}

func (s *InMemoryTokenStore) Load(userID, providerID string) (*tokenEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tokens[storeKey(userID, providerID)]
	return e, ok
}

func (s *InMemoryTokenStore) Delete(userID, providerID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := storeKey(userID, providerID)
	_, ok := s.tokens[key]
	delete(s.tokens, key)
	return ok
}

func (s *InMemoryTokenStore) LoadAllForUser(userID string) map[string]*tokenEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := userID + ":"
	out := make(map[string]*tokenEntry)
	for k, v := range s.tokens {
		if strings.HasPrefix(k, prefix) {
			out[k[len(prefix):]] = v
		}
	}
	return out
}

func storeKey(userID, providerID string) string {
	return userID + ":" + providerID
}
