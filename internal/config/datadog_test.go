package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDatadogConfig_Validate_Disabled(t *testing.T) {
	cfg := DatadogConfig{Enabled: false}
	err := cfg.Validate()
	require.NoError(t, err)
}

func TestDatadogConfig_Validate_MissingAPIKey(t *testing.T) {
	cfg := DatadogConfig{
		Enabled: true,
		AppKey:  "some-app-key",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api_key is required")
}

func TestDatadogConfig_Validate_MissingAppKey(t *testing.T) {
	cfg := DatadogConfig{
		Enabled: true,
		APIKey:  "some-api-key",
	}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "app_key is required")
}

func TestDatadogConfig_Validate_Valid(t *testing.T) {
	cfg := DatadogConfig{
		Enabled: true,
		APIKey:  "some-api-key",
		AppKey:  "some-app-key",
	}
	err := cfg.Validate()
	require.NoError(t, err)
}

func TestDatadogConfig_BaseURL(t *testing.T) {
	tests := []struct {
		name     string
		site     string
		expected string
	}{
		{"default site", "", "https://api.datadoghq.com/"},
		{"custom site", "datadoghq.eu", "https://api.datadoghq.eu/"},
		{"us5 site", "us5.datadoghq.com", "https://api.us5.datadoghq.com/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DatadogConfig{Site: tt.site}
			assert.Equal(t, tt.expected, cfg.BaseURL())
		})
	}
}

func TestDatadogConfig_EffectiveSite(t *testing.T) {
	t.Run("empty returns default", func(t *testing.T) {
		cfg := DatadogConfig{}
		assert.Equal(t, "datadoghq.com", cfg.EffectiveSite())
	})

	t.Run("set returns configured", func(t *testing.T) {
		cfg := DatadogConfig{Site: "datadoghq.eu"}
		assert.Equal(t, "datadoghq.eu", cfg.EffectiveSite())
	})
}

func TestDefaultDatadogConfig(t *testing.T) {
	cfg := DefaultDatadogConfig()
	assert.False(t, cfg.Enabled)
	assert.Equal(t, "datadoghq.com", cfg.Site)
	assert.Equal(t, 5.0, cfg.RateLimitRPS)
	assert.Empty(t, cfg.APIKey)
	assert.Empty(t, cfg.AppKey)
}
