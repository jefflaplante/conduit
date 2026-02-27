package oauthflow

import "os"

// Default OAuth endpoints for Anthropic.
const (
	DefaultAuthorizeURL = "https://console.anthropic.com/oauth/authorize"
	DefaultTokenURL     = "https://console.anthropic.com/v1/oauth/token"
	DefaultScopes       = "user:inference"
)

// OAuthEndpoints holds the OAuth endpoint configuration.
type OAuthEndpoints struct {
	AuthorizeURL string
	TokenURL     string
	ClientID     string
	Scopes       string
}

// DefaultEndpoints returns OAuthEndpoints with built-in defaults,
// overridden by environment variables if set.
func DefaultEndpoints() OAuthEndpoints {
	ep := OAuthEndpoints{
		AuthorizeURL: DefaultAuthorizeURL,
		TokenURL:     DefaultTokenURL,
		Scopes:       DefaultScopes,
	}

	if v := os.Getenv("ANTHROPIC_OAUTH_AUTHORIZE_URL"); v != "" {
		ep.AuthorizeURL = v
	}
	if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN_URL"); v != "" {
		ep.TokenURL = v
	}
	if v := os.Getenv("ANTHROPIC_OAUTH_CLIENT_ID"); v != "" {
		ep.ClientID = v
	}
	if v := os.Getenv("ANTHROPIC_OAUTH_SCOPES"); v != "" {
		ep.Scopes = v
	}

	return ep
}
