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
	DisplayName string   `yaml:"display_name"`
	Description string   `yaml:"description"`
	AuthURL     string   `yaml:"auth_url"`
	TokenURL    string   `yaml:"token_url"`
	Scopes      []string `yaml:"scopes"`
	RedirectURL string   `yaml:"redirect_url"`
	// UsePKCE controls whether PKCE (S256) is used. Defaults to true.
	// Set to false for providers that do not support code_challenge (e.g. Slack).
	UsePKCE *bool `yaml:"use_pkce"`
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
		interpolated[id] = p
	}
	cfg.Providers = interpolated

	return &cfg, nil
}
