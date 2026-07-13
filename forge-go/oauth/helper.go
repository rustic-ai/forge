package oauth

import (
	"strings"
)

const prefix = "oauth:"

func StoreKey(orgID, providerID string) string {
	return prefix + orgID + "|" + providerID
}

// callbackPath is the single, provider- and org-agnostic OAuth2 redirect/callback
// path. The flow is fully identified by the opaque state at callback time, so
// neither the provider nor the org belongs in the URL. A single stable redirect
// URI also keeps the door open for OAuth Client ID Metadata Documents, where the
// client publishes one metadata document listing its redirect URIs.
const callbackPath = "/oauth/callback"

// callbackURL is the full redirect/callback URL. base already includes the OAuth
// route prefix.
func callbackURL(base string) string {
	return base + callbackPath
}

// ParseOAuthKey parses an "oauth:orgID|providerID" key. Returns ok=false for non-OAuth keys.
func ParseOAuthKey(key string) (orgID, providerID string, ok bool) {
	rest, found := strings.CutPrefix(key, prefix)
	if !found {
		return "", "", false
	}
	idx := strings.Index(rest, "|")
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}
