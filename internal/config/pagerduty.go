package config

import "fmt"

// PagerDutyConfig holds configuration for the optional PagerDuty integration.
type PagerDutyConfig struct {
	Enabled                   bool    `json:"enabled"`
	APIToken                  string  `json:"api_token,omitempty" cfg:"env"`
	DefaultServiceID          string  `json:"default_service_id,omitempty"`
	DefaultEscalationPolicyID string  `json:"default_escalation_policy_id,omitempty"`
	BaseURL                   string  `json:"base_url,omitempty"`
	RateLimitRPS              float64 `json:"rate_limit_rps,omitempty"`
}

// DefaultPagerDutyConfig returns sensible defaults for PagerDuty configuration.
func DefaultPagerDutyConfig() PagerDutyConfig {
	return PagerDutyConfig{
		Enabled:      false,
		BaseURL:      "https://api.pagerduty.com",
		RateLimitRPS: 5.0,
	}
}

// Validate checks the PagerDuty configuration for errors.
func (p *PagerDutyConfig) Validate() error {
	if !p.Enabled {
		return nil
	}

	if p.APIToken == "" {
		return fmt.Errorf("pagerduty: api_token is required when enabled")
	}

	return nil
}

// EffectiveBaseURL returns the configured BaseURL or the default if empty.
func (p *PagerDutyConfig) EffectiveBaseURL() string {
	if p.BaseURL != "" {
		return p.BaseURL
	}
	return "https://api.pagerduty.com"
}
