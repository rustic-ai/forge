package secrets

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// secretPrefix namespaces org-scoped secret entries so they do not collide with
// OAuth token entries ("oauth:") or DCR client credentials ("oauth-client:").
const secretPrefix = "secret:"

// SecretStoreKey builds the storage key for an org-scoped secret. It mirrors
// oauth.StoreKey: "secret:orgID|name".
func SecretStoreKey(orgID, name string) string {
	return secretPrefix + orgID + "|" + name
}

// ParseSecretKey parses a "secret:orgID|name" key. Returns ok=false for keys
// that are not org-scoped secrets.
func ParseSecretKey(key string) (orgID, name string, ok bool) {
	rest, found := strings.CutPrefix(key, secretPrefix)
	if !found {
		return "", "", false
	}
	idx := strings.Index(rest, "|")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// SecretStore persists org-scoped secret values keyed by (orgID, name) and is
// the single source of truth for which secrets exist. Implement this interface
// to swap in keychain, encrypted DB, or other backends.
//
// It deliberately exposes no method that returns a stored value: values are read
// only by a SecretProvider during resolution (which owns its own backend access),
// never through the management path, so the CRUD API cannot leak them.
type SecretStore interface {
	// Save creates or overwrites the secret's value. Create/update semantics
	// (conflict vs not-found) are enforced by the Manager via Exists.
	Save(orgID, name, value string) error
	Delete(orgID, name string) bool
	Exists(orgID, name string) bool
	// List returns the names of the org's secrets, sorted. The error return lets
	// backends report enumeration failures; current backends do not fail.
	List(orgID string) ([]string, error)
}

// NewSecretStore creates a SecretStore by name. Supported backends:
//   - "memory" (default): in-process store, secrets lost on restart.
//   - "keychain": OS keychain (macOS Keychain, Windows Credential Manager,
//     Linux Secret Service). Set FORGE_SECRET_STORE=keychain to activate.
func NewSecretStore(kind string) (SecretStore, error) {
	switch kind {
	case "", "memory":
		return NewInMemorySecretStore(), nil
	case "keychain":
		return NewKeychainSecretStore(), nil
	default:
		return nil, fmt.Errorf("unknown secret store %q; supported: memory, keychain", kind)
	}
}

// InMemorySecretStore is the default in-process secret store. Values are lost on
// server restart. Its map is the single source of truth for listing.
type InMemorySecretStore struct {
	mu      sync.Mutex
	secrets map[string]string // key: "SecretStoreKey(orgID, name)"
}

func NewInMemorySecretStore() *InMemorySecretStore {
	return &InMemorySecretStore{secrets: make(map[string]string)}
}

func (s *InMemorySecretStore) Save(orgID, name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[SecretStoreKey(orgID, name)] = value
	return nil
}

func (s *InMemorySecretStore) Delete(orgID, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := SecretStoreKey(orgID, name)
	_, ok := s.secrets[key]
	delete(s.secrets, key)
	return ok
}

func (s *InMemorySecretStore) Exists(orgID, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.secrets[SecretStoreKey(orgID, name)]
	return ok
}

func (s *InMemorySecretStore) List(orgID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0)
	for key := range s.secrets {
		if kOrg, name, ok := ParseSecretKey(key); ok && kOrg == orgID {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
