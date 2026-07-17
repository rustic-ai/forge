package secrets

import (
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/zalando/go-keyring"
)

// KeychainSecretStore persists org-scoped secret values in the OS keychain

type KeychainSecretStore struct {
	service string
}

func NewKeychainSecretStore() *KeychainSecretStore {
	return NewKeychainSecretStoreWithService(forgepath.KeychainService())
}

func NewKeychainSecretStoreWithService(service string) *KeychainSecretStore {
	return &KeychainSecretStore{service: service}
}

func (s *KeychainSecretStore) Save(orgID, name, value string) error {
	return keyring.Set(s.service, SecretStoreKey(orgID, name), value)
}

func (s *KeychainSecretStore) Delete(orgID, name string) bool {
	return keyring.Delete(s.service, SecretStoreKey(orgID, name)) == nil
}

func (s *KeychainSecretStore) Exists(orgID, name string) bool {
	_, err := keyring.Get(s.service, SecretStoreKey(orgID, name))
	return err == nil
}

func (s *KeychainSecretStore) List(orgID string) ([]string, error) {
	var filtered []string
	availableKeys, err := keyring.ListUsers(s.service)
	// using empty string for name since we want to list all secrets saved for the org
	prefix := SecretStoreKey(orgID, "")
	for _, key := range availableKeys {
		if strings.HasPrefix(key, prefix) {
			// Add only the part after the prefix
			stripped := strings.TrimPrefix(key, prefix)
			filtered = append(filtered, stripped)
		}
	}
	sort.Strings(filtered)
	return filtered, err
}
