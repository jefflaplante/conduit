package gateway

import (
	"testing"

	"conduit/internal/config"
)

func TestWebSocketConfigDefaults(t *testing.T) {
	// Test DefaultWebSocketConfig
	cfg := config.DefaultWebSocketConfig()
	if cfg.MaxMessageSize != 1048576 {
		t.Errorf("DefaultWebSocketConfig().MaxMessageSize = %d, want 1048576", cfg.MaxMessageSize)
	}
}

func TestWebSocketConfigGetMaxMessageSize(t *testing.T) {
	tests := []struct {
		name     string
		config   config.WebSocketConfig
		expected int64
	}{
		{
			name:     "default value when zero",
			config:   config.WebSocketConfig{MaxMessageSize: 0},
			expected: 1048576, // 1MB default
		},
		{
			name:     "default value when negative",
			config:   config.WebSocketConfig{MaxMessageSize: -1},
			expected: 1048576, // 1MB default
		},
		{
			name:     "custom value",
			config:   config.WebSocketConfig{MaxMessageSize: 2097152},
			expected: 2097152, // 2MB
		},
		{
			name:     "small value",
			config:   config.WebSocketConfig{MaxMessageSize: 1024},
			expected: 1024, // 1KB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.GetMaxMessageSize()
			if result != tt.expected {
				t.Errorf("GetMaxMessageSize() = %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestConfigIncludesWebSocketDefaults(t *testing.T) {
	// Test that Default() includes WebSocket config
	cfg := config.Default()
	if cfg.WebSocket.MaxMessageSize != 1048576 {
		t.Errorf("Default().WebSocket.MaxMessageSize = %d, want 1048576", cfg.WebSocket.MaxMessageSize)
	}
}
