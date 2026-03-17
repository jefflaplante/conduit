package ai

import (
	"fmt"
	"testing"
)

func TestUserFriendlyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "context size exceeded",
			err:      fmt.Errorf(`API error: 400 - {"error":{"code":400,"message":"request (32598 tokens) exceeds the available context size (16384 tokens), try increasing it","type":"exceed_context_size_error"}}`),
			expected: "request (32598 tokens) exceeds the available context size (16384 tokens), try increasing it",
		},
		{
			name:     "generic API error with message",
			err:      fmt.Errorf(`API error: 500 - {"error":{"message":"internal server error","type":"server_error"}}`),
			expected: "internal server error",
		},
		{
			name:     "non-JSON error",
			err:      fmt.Errorf("request failed: connection refused"),
			expected: "request failed: connection refused",
		},
		{
			name:     "API error with non-JSON body",
			err:      fmt.Errorf("API error: 502 - Bad Gateway"),
			expected: "API error: 502 - Bad Gateway",
		},
		{
			name:     "API error with empty message field",
			err:      fmt.Errorf(`API error: 400 - {"error":{"type":"bad_request"}}`),
			expected: `API error: 400 - {"error":{"type":"bad_request"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := UserFriendlyError(tt.err)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
