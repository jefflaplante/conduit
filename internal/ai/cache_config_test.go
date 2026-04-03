package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetCacheMinTokens(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected int
	}{
		{"opus-4-6", "claude-opus-4-6", 4096},
		{"opus-4-5", "claude-opus-4-5", 4096},
		{"sonnet-4-6", "claude-sonnet-4-6", 2048},
		{"sonnet-4-5", "claude-sonnet-4-5", 1024},
		{"sonnet-4", "claude-sonnet-4", 1024},
		{"sonnet-3.7", "claude-sonnet-3.7", 1024},
		{"haiku-4-5", "claude-haiku-4-5", 4096},
		{"haiku-3.5", "claude-haiku-3.5", 2048},
		{"haiku-3", "claude-haiku-3", 2048},
		{"unknown", "gpt-4o", DefaultCacheMinTokens},
		{"with-date-suffix", "claude-opus-4-6-20251101", 4096},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetCacheMinTokens(tt.model)
			assert.Equal(t, tt.expected, result, "model: %s", tt.model)
		})
	}
}

func TestDefaultPromptCachingConfig(t *testing.T) {
	cfg := DefaultPromptCachingConfig()

	assert.True(t, cfg.Enabled, "Enabled should be true by default")
	assert.False(t, cfg.ExtendedTTL, "ExtendedTTL should be false by default")
	assert.True(t, cfg.CacheTools, "CacheTools should be true by default")
	assert.True(t, cfg.CacheSystem, "CacheSystem should be true by default")
	assert.True(t, cfg.CacheHistory, "CacheHistory should be true by default")
	assert.Equal(t, 15, cfg.HistoryBreakpointInterval, "HistoryBreakpointInterval should be 15")
}

func TestCacheMinTokensDefaultValue(t *testing.T) {
	// Verify the constant value
	assert.Equal(t, 2048, DefaultCacheMinTokens)
}
