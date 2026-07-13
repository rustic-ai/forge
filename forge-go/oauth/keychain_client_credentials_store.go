package oauth

import (
	"encoding/json"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/zalando/go-keyring"
)

// KeychainClientCredentialsStore persists DCR-issued client credentials in the
// OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service
// via libsecret), namespaced separately from OAuth tokens.
//
// It is selected independently of the token store: set
// FORGE_OAUTH_CLIENT_STORE=keychain.
type KeychainClientCredentialsStore struct {
	service string
}

func NewKeychainClientCredentialsStore() *KeychainClientCredentialsStore {
	return NewKeychainClientCredentialsStoreWithService(forgepath.KeychainService())
}

func NewKeychainClientCredentialsStoreWithService(service string) *KeychainClientCredentialsStore {
	return &KeychainClientCredentialsStore{service: service}
}

func (s *KeychainClientCredentialsStore) SaveCredentials(providerID string, c *clientCredentials) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling client credentials: %w", err)
	}
	if err := keyring.Set(s.service, clientStoreKey(providerID), string(data)); err != nil {
		return fmt.Errorf("saving client credentials to keychain: %w", err)
	}
	return nil
}

func (s *KeychainClientCredentialsStore) LoadCredentials(providerID string) (*clientCredentials, bool) {
	data, err := keyring.Get(s.service, clientStoreKey(providerID))
	if err != nil {
		return nil, false
	}
	var c clientCredentials
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, false
	}
	return &c, true
}

func (s *KeychainClientCredentialsStore) DeleteCredentials(providerID string) bool {
	return keyring.Delete(s.service, clientStoreKey(providerID)) == nil
}
