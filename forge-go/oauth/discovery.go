package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// discoveryTTL is how long a resolved provider's endpoints are cached before
// being re-discovered. Authorization servers rarely rotate endpoints, but a
// bounded TTL lets changes propagate without a restart.
const discoveryTTL = 30 * time.Minute

// Well-known metadata path suffixes passed to wellKnownURL. These are the two
// documents the discovery chain fetches.
const (
	wkProtectedResource   = "oauth-protected-resource"   // RFC 9728
	wkAuthorizationServer = "oauth-authorization-server" // RFC 8414
)

// resolvedProvider holds the endpoints and capabilities discovered for a
// resource_url provider via RFC 9728 (protected resource metadata) and
// RFC 8414 (authorization server metadata).
type resolvedProvider struct {
	endpoint             oauth2.Endpoint
	registrationEndpoint string
	scopesSupported      []string
	authMethods          []string // token_endpoint_auth_methods_supported
	supportsS256         bool
	fetchedAt            time.Time
}

// supportsPublicClient reports whether the authorization server allows the
// "none" token endpoint auth method, i.e. registering a public client with no
// client_secret (paired with PKCE).
func (r *resolvedProvider) supportsPublicClient() bool {
	for _, m := range r.authMethods {
		if m == "none" {
			return true
		}
	}
	return false
}

// tokenEndpointAuthMethod picks the auth method to request during Dynamic
// Client Registration: prefer a public client ("none") when supported, else
// fall back to client_secret_post / client_secret_basic.
func (r *resolvedProvider) tokenEndpointAuthMethod() string {
	if r.supportsPublicClient() {
		return "none"
	}
	for _, m := range r.authMethods {
		if m == "client_secret_post" {
			return "client_secret_post"
		}
	}
	return "client_secret_basic"
}

// discoverer performs and caches OAuth2 endpoint discovery for resource_url
// providers. It is safe for concurrent use.
type discoverer struct {
	client *http.Client

	mu    sync.Mutex
	cache map[string]*resolvedProvider // key: resource URL
}

func newDiscoverer(client *http.Client) *discoverer {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &discoverer{client: client, cache: map[string]*resolvedProvider{}}
}

// resolve discovers the authorization/token/registration endpoints for the
// given resource URL, caching the result for discoveryTTL.
func (d *discoverer) resolve(ctx context.Context, resourceURL string) (*resolvedProvider, error) {
	resourceURL = strings.TrimSpace(resourceURL)
	if resourceURL == "" {
		return nil, fmt.Errorf("resolve: empty resource URL")
	}

	d.mu.Lock()
	if r, ok := d.cache[resourceURL]; ok && time.Since(r.fetchedAt) < discoveryTTL {
		d.mu.Unlock()
		return r, nil
	}
	d.mu.Unlock()

	r, err := d.discover(ctx, resourceURL)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	d.cache[resourceURL] = r
	d.mu.Unlock()
	return r, nil
}

func (d *discoverer) discover(ctx context.Context, resourceURL string) (*resolvedProvider, error) {
	// Step 1 (RFC 9728): fetch protected resource metadata to learn the
	// authorization server(s) backing this resource.
	prmURL, err := wellKnownURL(resourceURL, wkProtectedResource)
	if err != nil {
		return nil, fmt.Errorf("building protected-resource metadata URL: %w", err)
	}
	prm, err := oauthex.GetProtectedResourceMetadata(ctx, prmURL, resourceURL, d.client)
	if err != nil {
		return nil, fmt.Errorf("fetching protected resource metadata for %q: %w", resourceURL, err)
	}
	if prm == nil || len(prm.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("resource %q advertises no authorization servers", resourceURL)
	}
	issuer := prm.AuthorizationServers[0]

	// Step 2 (RFC 8414): fetch authorization server metadata to learn the
	// authorization, token, and registration endpoints.
	asURL, err := wellKnownURL(issuer, wkAuthorizationServer)
	if err != nil {
		return nil, fmt.Errorf("building authorization-server metadata URL: %w", err)
	}
	as, err := oauthex.GetAuthServerMeta(ctx, asURL, issuer, d.client)
	if err != nil {
		return nil, fmt.Errorf("fetching authorization server metadata for %q: %w", issuer, err)
	}
	if as == nil {
		return nil, fmt.Errorf("authorization server %q returned no metadata", issuer)
	}
	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, fmt.Errorf("authorization server %q is missing authorization or token endpoint", issuer)
	}

	s256 := false
	for _, m := range as.CodeChallengeMethodsSupported {
		if m == "S256" {
			s256 = true
			break
		}
	}

	return &resolvedProvider{
		endpoint: oauth2.Endpoint{
			AuthURL:  as.AuthorizationEndpoint,
			TokenURL: as.TokenEndpoint,
		},
		registrationEndpoint: as.RegistrationEndpoint,
		scopesSupported:      as.ScopesSupported,
		authMethods:          as.TokenEndpointAuthMethodsSupported,
		supportsS256:         s256,
		fetchedAt:            time.Now(),
	}, nil
}

// wellKnownURL builds an RFC 8414 / RFC 9728 well-known metadata URL by
// inserting "/.well-known/<suffix>" ahead of the base URL's path. For a base
// with no path, the well-known segment is simply appended.
//
//	https://host        + oauth-protected-resource -> https://host/.well-known/oauth-protected-resource
//	https://host/mcp    + oauth-protected-resource -> https://host/.well-known/oauth-protected-resource/mcp
func wellKnownURL(base, suffix string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", base)
	}
	path := strings.Trim(u.Path, "/")
	suffix = strings.TrimSpace(suffix)
	wk := "/.well-known/" + suffix
	if path != "" {
		wk += "/" + path
	}
	u.Path = wk
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
