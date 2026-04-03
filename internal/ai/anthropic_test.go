package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAnthropicUsage_WithCacheMetrics(t *testing.T) {
	provider := &AnthropicProvider{}

	tests := []struct {
		name                     string
		response                 map[string]interface{}
		expectedPromptTokens     int
		expectedCompletionTokens int
		expectedCacheWrite       int
		expectedCacheRead        int
	}{
		{
			name: "no cache metrics",
			response: map[string]interface{}{
				"usage": map[string]interface{}{
					"input_tokens":  float64(1000),
					"output_tokens": float64(500),
				},
			},
			expectedPromptTokens:     1000,
			expectedCompletionTokens: 500,
			expectedCacheWrite:       0,
			expectedCacheRead:        0,
		},
		{
			name: "cache write (first request)",
			response: map[string]interface{}{
				"usage": map[string]interface{}{
					"input_tokens":                float64(100),
					"output_tokens":               float64(200),
					"cache_creation_input_tokens": float64(5000),
					"cache_read_input_tokens":     float64(0),
				},
			},
			expectedPromptTokens:     100,
			expectedCompletionTokens: 200,
			expectedCacheWrite:       5000,
			expectedCacheRead:        0,
		},
		{
			name: "cache read (subsequent request)",
			response: map[string]interface{}{
				"usage": map[string]interface{}{
					"input_tokens":                float64(50),
					"output_tokens":               float64(150),
					"cache_creation_input_tokens": float64(0),
					"cache_read_input_tokens":     float64(5000),
				},
			},
			expectedPromptTokens:     50,
			expectedCompletionTokens: 150,
			expectedCacheWrite:       0,
			expectedCacheRead:        5000,
		},
		{
			name: "partial cache hit",
			response: map[string]interface{}{
				"usage": map[string]interface{}{
					"input_tokens":                float64(200),
					"output_tokens":               float64(300),
					"cache_creation_input_tokens": float64(1000),
					"cache_read_input_tokens":     float64(4000),
				},
			},
			expectedPromptTokens:     200,
			expectedCompletionTokens: 300,
			expectedCacheWrite:       1000,
			expectedCacheRead:        4000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := provider.parseAnthropicUsage(tt.response)

			assert.Equal(t, tt.expectedPromptTokens, usage.PromptTokens, "PromptTokens mismatch")
			assert.Equal(t, tt.expectedCompletionTokens, usage.CompletionTokens, "CompletionTokens mismatch")
			assert.Equal(t, tt.expectedCacheWrite, usage.CacheCreationInputTokens, "CacheCreationInputTokens mismatch")
			assert.Equal(t, tt.expectedCacheRead, usage.CacheReadInputTokens, "CacheReadInputTokens mismatch")
		})
	}
}
