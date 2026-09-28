package config

// AuthTokenConfig holds configuration for the token authentication system
type AuthTokenConfig struct {
	// TokenSecret is the HMAC key used for hashing tokens (hex-encoded, 32 bytes).
	// Supports ${ENV_VAR} expansion. If empty, CONDUIT_TOKEN_SECRET is used,
	// then a key persisted at {data_dir}/auth/token_secret (generated once,
	// 0600). See auth.ResolveTokenStore (conduit-31jg.3).
	TokenSecret string `json:"token_secret,omitempty" cfg:"env"`
}

// SSHServerConfig holds configuration for the integrated SSH server
type SSHServerConfig struct {
	Enabled            bool   `json:"enabled"`
	ListenAddr         string `json:"listen_addr,omitempty"`
	HostKeyPath        string `json:"host_key_path,omitempty" cfg:"path"`
	AuthorizedKeysPath string `json:"authorized_keys_path,omitempty" cfg:"path"`
}

// WebSocketConfig holds configuration for WebSocket connections
type WebSocketConfig struct {
	// MaxMessageSize is the maximum size in bytes of incoming WebSocket messages.
	// Messages exceeding this limit will be rejected with a close error.
	// Default: 1048576 (1MB). Set to 0 to use default.
	MaxMessageSize int64 `json:"max_message_size,omitempty"`
}

// DefaultWebSocketConfig returns sensible defaults for WebSocket configuration
func DefaultWebSocketConfig() WebSocketConfig {
	return WebSocketConfig{
		MaxMessageSize: 1048576, // 1MB default
	}
}

// GetMaxMessageSize returns the configured max message size, or the default if not set
func (c *WebSocketConfig) GetMaxMessageSize() int64 {
	if c.MaxMessageSize <= 0 {
		return 1048576 // 1MB default
	}
	return c.MaxMessageSize
}

// TUIConfig holds configuration for the TUI shell escape feature
type TUIConfig struct {
	ShellEscape ShellEscapeConfig `json:"shell_escape,omitempty"`
}

// ChannelConfig contains settings for channel adapters
type ChannelConfig struct {
	Name    string                 `json:"name"`
	Type    string                 `json:"type"`
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config"`
}

// RateLimitingConfig contains rate limiting settings
type RateLimitingConfig struct {
	Enabled                bool                `json:"enabled"`
	Anonymous              RateLimitTierConfig `json:"anonymous"`
	Authenticated          RateLimitTierConfig `json:"authenticated"`
	CleanupIntervalSeconds int                 `json:"cleanupIntervalSeconds"`
	// TrustProxy controls how X-Forwarded-For headers are handled for IP extraction.
	// When false (default), only the direct connection IP (RemoteAddr) is used.
	// When true, the rightmost non-private IP from X-Forwarded-For is used.
	// Only enable this when running behind a trusted reverse proxy (nginx, Cloudflare, etc).
	TrustProxy bool `json:"trustProxy"`
}

// RateLimitTierConfig defines rate limiting for a specific tier (anonymous vs authenticated)
type RateLimitTierConfig struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

// DiagnosticsConfig contains settings for diagnostic endpoints security
type DiagnosticsConfig struct {
	// RequireAuth controls whether diagnostic endpoints require authentication.
	// When true (default), /metrics, /diagnostics, /prometheus require auth.
	// The /health endpoint has its own HealthPublic setting.
	RequireAuth bool `json:"require_auth"`

	// HealthPublic controls whether /health is accessible without authentication.
	// Default: true (public) for load balancer compatibility.
	// Set to false to require auth for /health as well.
	HealthPublic *bool `json:"health_public,omitempty"`
}

// IsHealthPublic returns whether the /health endpoint should be public.
// Defaults to true for load balancer compatibility.
func (d *DiagnosticsConfig) IsHealthPublic() bool {
	if d.HealthPublic == nil {
		return true
	}
	return *d.HealthPublic
}

// DefaultDiagnosticsConfig returns secure defaults for diagnostics config
func DefaultDiagnosticsConfig() DiagnosticsConfig {
	return DiagnosticsConfig{
		RequireAuth:  true, // Require auth for /metrics, /diagnostics, /prometheus
		HealthPublic: nil,  // nil means default to true (public /health)
	}
}
