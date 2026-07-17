package secrets

import (
	"errors"
	"sync"
)

// ErrSecretExists is returned by Manager.Set when a secret with the same name
// already exists for the org.
var ErrSecretExists = errors.New("secret already exists")

// Manager provides org-scoped secret CRUD on top of a SecretStore. It holds no
// state of its own — the store is the single source of truth, so there is no
// separate name index to maintain. Its mutex only serializes the
// exists-then-write sequence in Set/Update against concurrent callers.
//
// The Manager deliberately exposes no method that returns a secret value:
// values are read only by a SecretProvider during resolution, never through the
// management API, so they cannot be leaked via an HTTP handler.
type Manager struct {
	mu    sync.Mutex
	store SecretStore
}

// NewManager constructs a Manager backed by the given SecretStore.
func NewManager(store SecretStore) *Manager {
	return &Manager{store: store}
}

// Set creates a new secret for the org. It returns ErrSecretExists if a secret
// with the same name already exists (use Update to change an existing value).
func (m *Manager) Set(orgID, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store.Exists(orgID, name) {
		return ErrSecretExists
	}
	return m.store.Save(orgID, name, value)
}

// Update replaces the value of an existing secret. It returns ErrSecretNotFound
// if no secret with that name exists for the org.
func (m *Manager) Update(orgID, name, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.store.Exists(orgID, name) {
		return ErrSecretNotFound
	}
	return m.store.Save(orgID, name, value)
}

// Delete removes a secret. It returns false if no secret with that name exists
// for the org.
func (m *Manager) Delete(orgID, name string) bool {
	return m.store.Delete(orgID, name)
}

// List returns the names of the org's secrets, sorted. It never returns values.
func (m *Manager) List(orgID string) ([]string, error) {
	return m.store.List(orgID)
}

// Exists reports whether a secret with that name exists for the org.
func (m *Manager) Exists(orgID, name string) bool {
	return m.store.Exists(orgID, name)
}
