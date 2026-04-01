package config

import (
	"testing"
)

func TestWebSocketConfigDefaults(t *testing.T) {
	cfg := DefaultWebSocketConfig()
	if cfg.MaxMessageSize != 1048576 {
		t.Errorf("DefaultWebSocketConfig().MaxMessageSize = %d, want 1048576 (1MB)", cfg.MaxMessageSize)
	}
}

func TestWebSocketConfigGetMaxMessageSize(t *testing.T) {
	tests := []struct {
		name     string
		config   WebSocketConfig
		expected int64
	}{
		{
			name:     "zero returns default",
			config:   WebSocketConfig{MaxMessageSize: 0},
			expected: 1048576,
		},
		{
			name:     "negative returns default",
			config:   WebSocketConfig{MaxMessageSize: -1},
			expected: 1048576,
		},
		{
			name:     "custom 2MB value",
			config:   WebSocketConfig{MaxMessageSize: 2097152},
			expected: 2097152,
		},
		{
			name:     "small 1KB value",
			config:   WebSocketConfig{MaxMessageSize: 1024},
			expected: 1024,
		},
		{
			name:     "large 10MB value",
			config:   WebSocketConfig{MaxMessageSize: 10485760},
			expected: 10485760,
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

func TestDefaultConfigIncludesWebSocket(t *testing.T) {
	cfg := Default()
	if cfg.WebSocket.MaxMessageSize != 1048576 {
		t.Errorf("Default().WebSocket.MaxMessageSize = %d, want 1048576 (1MB)", cfg.WebSocket.MaxMessageSize)
	}
}
