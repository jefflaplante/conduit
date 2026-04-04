package config

import "fmt"

// DatadogConfig holds configuration for the optional Datadog integration.
type DatadogConfig struct {
	Enabled      bool    `json:"enabled"`
	APIKey       string  `json:"api_key,omitempty" cfg:"env"`
	AppKey       string  `json:"app_key,omitempty" cfg:"env"`
	Site         string  `json:"site,omitempty"`
	RateLimitRPS float64 `json:"rate_limit_rps,omitempty"`
}

// DefaultDatadogConfig returns sensible defaults for Datadog configuration.
func DefaultDatadogConfig() DatadogConfig {
	return DatadogConfig{
		Enabled:      false,
		Site:         "datadoghq.com",
		RateLimitRPS: 5.0,
	}
}

// Validate checks the Datadog configuration for errors.
func (d *DatadogConfig) Validate() error {
	if !d.Enabled {
		return nil
	}

	if d.APIKey == "" {
		return fmt.Errorf("datadog: api_key is required when enabled")
	}

	if d.AppKey == "" {
		return fmt.Errorf("datadog: app_key is required when enabled")
	}

	return nil
}

// EffectiveSite returns the configured site or the default "datadoghq.com".
func (d DatadogConfig) EffectiveSite() string {
	if d.Site == "" {
		return "datadoghq.com"
	}
	return d.Site
}

// BaseURL returns the Datadog API base URL derived from the configured site.
func (d DatadogConfig) BaseURL() string {
	return "https://api." + d.EffectiveSite() + "/"
}
