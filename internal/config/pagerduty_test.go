package config

import (
	"testing"
)

func TestPagerDutyConfig_Validate_Disabled(t *testing.T) {
	cfg := PagerDutyConfig{Enabled: false}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected nil error for disabled config, got: %v", err)
	}
}

func TestPagerDutyConfig_Validate_MissingToken(t *testing.T) {
	cfg := PagerDutyConfig{Enabled: true}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing api_token, got nil")
	}
}

func TestPagerDutyConfig_Validate_Valid(t *testing.T) {
	cfg := PagerDutyConfig{
		Enabled:  true,
		APIToken: "test-token",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected nil error for valid config, got: %v", err)
	}
}

func TestPagerDutyConfig_EffectiveBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{"default when empty", "", "https://api.pagerduty.com"},
		{"custom URL", "https://custom.pagerduty.com", "https://custom.pagerduty.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := PagerDutyConfig{BaseURL: tt.baseURL}
			if got := cfg.EffectiveBaseURL(); got != tt.want {
				t.Errorf("EffectiveBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultPagerDutyConfig(t *testing.T) {
	cfg := DefaultPagerDutyConfig()
	if cfg.Enabled {
		t.Error("expected Enabled to be false")
	}
	if cfg.BaseURL != "https://api.pagerduty.com" {
		t.Errorf("expected default BaseURL, got %q", cfg.BaseURL)
	}
	if cfg.RateLimitRPS != 5.0 {
		t.Errorf("expected RateLimitRPS 5.0, got %f", cfg.RateLimitRPS)
	}
}
