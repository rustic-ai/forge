// Package keychain provides a secrets.SecretProvider backed by the OS keychain
// (macOS Keychain, Windows Credential Manager, Linux Secret Service).
//
// If an OAuth manager is registered via SetOAuthManager, Resolve delegates
// to it (with automatic token refresh). Otherwise it reads the raw keychain
// value
package keychain

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/oauth"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/zalando/go-keyring"
)

// oauthMgr is the OAuth manager used to refresh expired tokens. Set via SetOAuthManager.
var oauthMgr atomic.Pointer[oauth.Manager]

// SetOAuthManager registers the OAuth manager for token refresh. Call once at server startup.
func SetOAuthManager(m *oauth.Manager) {
	oauthMgr.Store(m)
}

// SecretProvider resolves secrets from the OS keychain. The key is used
// directly as the keychain account name.
type SecretProvider struct {
	service string
}

func NewSecretProvider() *SecretProvider {
	return &SecretProvider{service: forgepath.KeychainService()}
}

func (p *SecretProvider) Resolve(ctx context.Context, key string) (string, error) {
	if orgID, providerID, ok := oauth.ParseOAuthKey(key); ok {
		if mgr := oauthMgr.Load(); mgr != nil {
			// live path: manager handles validity check and token refresh
			token, err := mgr.GetAccessToken(ctx, orgID, providerID)
			if errors.Is(err, oauth.ErrNotConnected) {
				return "", secrets.ErrSecretNotFound
			}
			return token, err
		}
		// fallback: no manager registered; extract access_token from stored JSON
		return p.oauthTokenFromKeychain(key)
	}

	raw, err := keyring.Get(p.service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", secrets.ErrSecretNotFound
		}
		return "", err
	}
	return raw, nil
}

func (p *SecretProvider) oauthTokenFromKeychain(key string) (string, error) {
	raw, err := keyring.Get(p.service, key)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", secrets.ErrSecretNotFound
		}
		return "", err
	}
	var entry struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal([]byte(raw), &entry) == nil && entry.AccessToken != "" {
		return entry.AccessToken, nil
	}
	return "", secrets.ErrSecretNotFound
}

func init() {
	secrets.RegisterProvider("keychain", func() secrets.SecretProvider {
		return NewSecretProvider()
	})
}
