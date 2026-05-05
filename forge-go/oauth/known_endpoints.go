package oauth

import (
	"fmt"

	"golang.org/x/oauth2"
)

var knownEndpoints = map[string]oauth2.Endpoint{
	"github": {
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
	},
	"google": {
		AuthURL:  "https://accounts.google.com/o/oauth2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
	},
	"google-drive": {
		AuthURL:  "https://accounts.google.com/o/oauth2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
	},
	"slack": {
		AuthURL:  "https://slack.com/oauth/v2/authorize",
		TokenURL: "https://slack.com/api/oauth.v2.access",
	},
	"microsoft": {
		AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	},
	"notion": {
		AuthURL:  "https://api.notion.com/v1/oauth/authorize",
		TokenURL: "https://api.notion.com/v1/oauth/token",
	},
}

func resolveEndpoint(providerID string, cfg ProviderConfig) (oauth2.Endpoint, error) {
	if cfg.AuthURL != "" && cfg.TokenURL != "" {
		return oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL}, nil
	}
	if ep, ok := knownEndpoints[providerID]; ok {
		return ep, nil
	}
	return oauth2.Endpoint{}, fmt.Errorf(
		"provider %q has no known endpoints; set auth_url and token_url in config", providerID,
	)
}
