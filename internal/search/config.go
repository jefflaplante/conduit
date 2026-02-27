package search

import (
	"errors"
	"os"
	"time"
)

// SearchConfig configures the search system
type SearchConfig struct {
	Enabled         bool                            `json:"enabled"`
	DefaultProvider string                          `json:"default_provider"`
	CacheTTLMinutes int                             `json:"cache_ttl_minutes"`
	CacheEnabled    bool                            `json:"cache_enabled"`
	Providers       map[string]ProviderSearchConfig `json:"providers"`
	// Router-specific settings
	EnableFallback         bool `json:"enable_fallback"`
	FallbackTimeoutSeconds int  `json:"fallback_timeout_seconds"`
	MetricsEnabled         bool `json:"metrics_enabled"`
}

// ProviderSearchConfig configures a specific search provider
type ProviderSearchConfig struct {
	Enabled        bool   `json:"enabled"`
	APIKey         string `json:"api_key"`
	Endpoint       string `json:"endpoint,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxRetries     int    `json:"max_retries,omitempty"`
	DefaultResults int    `json:"default_results,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
}

// DefaultSearchConfig returns the default search configuration
func DefaultSearchConfig() SearchConfig {
	return SearchConfig{
		Enabled:                true,
		DefaultProvider:        "brave",
		CacheTTLMinutes:        15,
		CacheEnabled:           true,
		EnableFallback:         true,
		FallbackTimeoutSeconds: 60,
		MetricsEnabled:         true,
		Providers: map[string]ProviderSearchConfig{
			"brave": {
				Enabled:        true,
				APIKey:         os.Getenv("BRAVE_API_KEY"),
				TimeoutSeconds: 30,
				MaxRetries:     3,
				DefaultResults: 5,
				MaxResults:     10,
			},
			"anthropic": {
				Enabled:        true,
				APIKey:         "", // Will be populated from request context/OAuth
				TimeoutSeconds: 60,
				MaxRetries:     2,
				DefaultResults: 5,
				MaxResults:     10,
			},
		},
	}
}

// GetBraveConfig converts the configuration to Brave-specific config
func (c *SearchConfig) GetBraveConfig() BraveSearchConfig {
	braveProvider, exists := c.Providers["brave"]
	if !exists {
		braveProvider = DefaultSearchConfig().Providers["brave"]
	}

	config := DefaultBraveConfig()
	config.APIKey = braveProvider.APIKey

	if braveProvider.Endpoint != "" {
		config.Endpoint = braveProvider.Endpoint
	}
	if braveProvider.TimeoutSeconds > 0 {
		config.Timeout = time.Duration(braveProvider.TimeoutSeconds) * time.Second
	}
	if braveProvider.MaxRetries > 0 {
		config.MaxRetries = braveProvider.MaxRetries
	}
	if braveProvider.DefaultResults > 0 {
		config.DefaultResults = braveProvider.DefaultResults
	}
	if braveProvider.MaxResults > 0 {
		config.MaxResults = braveProvider.MaxResults
	}

	return config
}

// GetCacheConfig converts the configuration to cache config
func (c *SearchConfig) GetCacheConfig() CacheConfig {
	return CacheConfig{
		TTLMinutes: c.CacheTTLMinutes,
		Enabled:    c.CacheEnabled,
	}
}

// GetAnthropicConfig converts the configuration to Anthropic-specific config
func (c *SearchConfig) GetAnthropicConfig() AnthropicNativeSearchConfig {
	anthProvider, exists := c.Providers["anthropic"]
	if !exists {
		anthProvider = DefaultSearchConfig().Providers["anthropic"]
	}

	config := AnthropicNativeSearchConfig{
		APIKey:       anthProvider.APIKey,
		Model:        "claude-3-5-sonnet-20241022", // Default model
		BaseURL:      "https://api.anthropic.com",
		UserLocation: "Seattle, WA, US",
		MaxUses:      5,
	}

	return config
}

// GetRouterConfig converts the search config to a router config
func (c *SearchConfig) GetRouterConfig() RouterConfig {
	config := RouterConfig{
		SearchConfig:           *c,
		EnableFallback:         c.EnableFallback,
		FallbackTimeoutSeconds: c.FallbackTimeoutSeconds,
		MetricsEnabled:         c.MetricsEnabled,
	}

	// Add Brave config if enabled
	if c.IsBraveEnabled() {
		braveConfig := c.GetBraveConfig()
		config.BraveConfig = &braveConfig
	}

	// Add Anthropic config if enabled
	if c.IsAnthropicEnabled() {
		anthConfig := c.GetAnthropicConfig()
		config.AnthropicConfig = &anthConfig
	}

	return config
}

// IsBraveEnabled checks if Brave search is enabled and configured
func (c *SearchConfig) IsBraveEnabled() bool {
	if !c.Enabled {
		return false
	}

	braveProvider, exists := c.Providers["brave"]
	if !exists {
		return false
	}

	return braveProvider.Enabled && braveProvider.APIKey != ""
}

// IsAnthropicEnabled checks if Anthropic search is enabled
func (c *SearchConfig) IsAnthropicEnabled() bool {
	if !c.Enabled {
		return false
	}

	anthProvider, exists := c.Providers["anthropic"]
	if !exists {
		return false
	}

	// Note: API key may be set dynamically from OAuth, so we don't require it here
	return anthProvider.Enabled
}

// LoadSearchConfigFromEnv loads search configuration from environment variables
func LoadSearchConfigFromEnv() SearchConfig {
	config := DefaultSearchConfig()

	// Override from environment
	if apiKey := os.Getenv("BRAVE_API_KEY"); apiKey != "" {
		if _, exists := config.Providers["brave"]; !exists {
			config.Providers["brave"] = ProviderSearchConfig{}
		}
		braveConfig := config.Providers["brave"]
		braveConfig.APIKey = apiKey
		config.Providers["brave"] = braveConfig
	}

	return config
}

// Validate checks if the search configuration is valid
func (c *SearchConfig) Validate() error {
	if !c.Enabled {
		return nil // Skip validation if disabled
	}

	if c.DefaultProvider == "" {
		c.DefaultProvider = "brave"
	}

	if c.CacheTTLMinutes <= 0 {
		c.CacheTTLMinutes = 15
	}

	// Validate that the default provider exists and is enabled
	if provider, exists := c.Providers[c.DefaultProvider]; !exists || !provider.Enabled {
		return ErrInvalidSearchConfig
	}

	// Validate Brave provider if it exists
	if braveProvider, exists := c.Providers["brave"]; exists && braveProvider.Enabled {
		if braveProvider.APIKey == "" {
			return ErrBraveAPIKeyMissing
		}
	}

	return nil
}

// Configuration errors
var (
	ErrInvalidSearchConfig = errors.New("invalid search configuration")
	ErrBraveAPIKeyMissing  = errors.New("Brave API key is required when Brave provider is enabled")
)
