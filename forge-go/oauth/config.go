package oauth

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ProviderConfig defines a named OAuth2 provider. Scopes and endpoint URLs are
// set here; credentials are supplied per-request by the caller.
type ProviderConfig struct {
	DisplayName string   `yaml:"display_name" json:"displayName,omitempty"`
	Description string   `yaml:"description" json:"description,omitempty"`
	AuthURL     string   `yaml:"auth_url" json:"authUrl,omitempty"`
	TokenURL    string   `yaml:"token_url" json:"tokenUrl,omitempty"`
	Scopes      []string `yaml:"scopes" json:"scopes,omitempty"`
	RedirectURL string   `yaml:"redirect_url" json:"redirectUrl,omitempty"`
	// UsePKCE controls whether PKCE (S256) is used. Defaults to true.
	// Set to false for providers that do not support it.
	UsePKCE *bool `yaml:"use_pkce" json:"usePkce,omitempty"`

	// ResourceURL is the OAuth2 protected-resource URL (e.g. an MCP server
	// endpoint) used by the Dynamic Client Registration flow to discover the
	// auth/token/registration endpoints (RFC 9728 + RFC 8414). It is only used
	// when UseDCRP is set; traditional providers should configure AuthURL and
	// TokenURL (or rely on the built-in endpoints) instead.
	ResourceURL string `yaml:"resource_url" json:"resourceUrl,omitempty"`
	// UseDCRP enables Dynamic Client Registration (RFC 7591): the endpoints are
	// discovered from ResourceURL and the client_id/client_secret are registered
	// with the provider on demand instead of being supplied by the caller.
	// Requires ResourceURL. Defaults to false.
	UseDCRP bool `yaml:"use_dcrp" json:"useDcrp,omitempty"`
}

// RequiresClientCredentials reports whether the caller must supply a client_id
// and client_secret when starting an auth flow. Providers using Dynamic Client
// Registration register their own credentials and require none.
func (p ProviderConfig) RequiresClientCredentials() bool {
	return !p.UseDCRP
}

// Validate reports configuration errors that would otherwise surface only when
// an auth flow is started. resource_url and use_dcrp go together: DCR relies on
// the endpoints advertised by the resource, and resource_url is meaningless
// outside the DCR flow (it is not general endpoint discovery for traditional
// providers).
func (p ProviderConfig) Validate(id string) error {
	if p.UseDCRP && p.ResourceURL == "" {
		return fmt.Errorf("provider %q: use_dcrp requires resource_url", id)
	}
	if p.ResourceURL != "" && !p.UseDCRP {
		return fmt.Errorf("provider %q: resource_url is only used with use_dcrp", id)
	}
	return nil
}

// pkce returns whether PKCE should be used for this provider (default: true).
func (p ProviderConfig) pkce() bool {
	return p.UsePKCE == nil || *p.UsePKCE
}

// ProvidersConfig is the top-level structure of the oauth-providers.yaml file.
type ProvidersConfig struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
}

var envPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func interpolateEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
}

// LoadProvidersConfig reads and parses the YAML file at path. If the file does
// not exist, an empty config is returned without error.
func LoadProvidersConfig(path string) (*ProvidersConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ProvidersConfig{Providers: map[string]ProviderConfig{}}, nil
		}
		return nil, fmt.Errorf("reading oauth providers config: %w", err)
	}

	var cfg ProvidersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing oauth providers config: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}

	interpolated := make(map[string]ProviderConfig, len(cfg.Providers))
	for id, p := range cfg.Providers {
		p.AuthURL = interpolateEnv(p.AuthURL)
		p.TokenURL = interpolateEnv(p.TokenURL)
		p.RedirectURL = interpolateEnv(p.RedirectURL)
		p.ResourceURL = interpolateEnv(p.ResourceURL)
		if err := p.Validate(id); err != nil {
			return nil, fmt.Errorf("parsing oauth providers config: %w", err)
		}
		interpolated[id] = p
	}
	cfg.Providers = interpolated

	return &cfg, nil
}
