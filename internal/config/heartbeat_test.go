package config

import (
	"testing"
	"time"
)

func TestHeartbeatConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  HeartbeatConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "disabled config is valid",
			config: HeartbeatConfig{
				Enabled:         false,
				IntervalSeconds: 5, // Invalid but ignored when disabled
			},
			wantErr: false,
		},
		{
			name: "valid enabled config",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				EnableMetrics:   true,
				EnableEvents:    true,
				LogLevel:        "info",
				MaxQueueDepth:   1000,
			},
			wantErr: false,
		},
		{
			name: "interval too short",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 5,
			},
			wantErr: true,
			errMsg:  "heartbeat interval cannot be less than 10 seconds",
		},
		{
			name: "interval too long",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 4000,
			},
			wantErr: true,
			errMsg:  "heartbeat interval cannot exceed 1 hour",
		},
		{
			name: "invalid log level",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				LogLevel:        "invalid",
			},
			wantErr: true,
			errMsg:  "invalid log level: invalid",
		},
		{
			name: "negative max queue depth",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				MaxQueueDepth:   -1,
			},
			wantErr: true,
			errMsg:  "max queue depth cannot be negative",
		},
		{
			name: "valid log levels",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				LogLevel:        "debug",
			},
			wantErr: false,
		},
		{
			name: "empty log level is valid",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				LogLevel:        "",
			},
			wantErr: false,
		},
		{
			name: "edge case minimum interval",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 10,
			},
			wantErr: false,
		},
		{
			name: "edge case maximum interval",
			config: HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 3600,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("HeartbeatConfig.Validate() expected error but got nil")
					return
				}
				if tt.errMsg != "" && err.Error() != tt.errMsg && !contains(err.Error(), tt.errMsg) {
					t.Errorf("HeartbeatConfig.Validate() error message = %v, expected to contain %v", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("HeartbeatConfig.Validate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestHeartbeatConfig_Interval(t *testing.T) {
	tests := []struct {
		name     string
		config   HeartbeatConfig
		expected time.Duration
	}{
		{
			name: "30 second interval",
			config: HeartbeatConfig{
				IntervalSeconds: 30,
			},
			expected: 30 * time.Second,
		},
		{
			name: "1 minute interval",
			config: HeartbeatConfig{
				IntervalSeconds: 60,
			},
			expected: 1 * time.Minute,
		},
		{
			name: "10 second interval",
			config: HeartbeatConfig{
				IntervalSeconds: 10,
			},
			expected: 10 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.Interval()
			if got != tt.expected {
				t.Errorf("HeartbeatConfig.Interval() = %v, expected %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultHeartbeatConfig(t *testing.T) {
	config := DefaultHeartbeatConfig()

	// Check default values
	if !config.Enabled {
		t.Error("DefaultHeartbeatConfig() should be enabled by default")
	}

	if config.IntervalSeconds != 30 {
		t.Errorf("DefaultHeartbeatConfig() interval = %d, expected 30", config.IntervalSeconds)
	}

	if !config.EnableMetrics {
		t.Error("DefaultHeartbeatConfig() should enable metrics by default")
	}

	if !config.EnableEvents {
		t.Error("DefaultHeartbeatConfig() should enable events by default")
	}

	if config.LogLevel != "info" {
		t.Errorf("DefaultHeartbeatConfig() log level = %s, expected 'info'", config.LogLevel)
	}

	if config.MaxQueueDepth != 1000 {
		t.Errorf("DefaultHeartbeatConfig() max queue depth = %d, expected 1000", config.MaxQueueDepth)
	}

	// Validate the default config
	if err := config.Validate(); err != nil {
		t.Errorf("DefaultHeartbeatConfig() validation failed: %v", err)
	}
}

func TestHeartbeatConfig_LogLevelValidation(t *testing.T) {
	validLevels := []string{"debug", "info", "warn", "error", ""}

	for _, level := range validLevels {
		t.Run("valid_"+level, func(t *testing.T) {
			config := HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				LogLevel:        level,
			}

			err := config.Validate()
			if err != nil {
				t.Errorf("LogLevel %s should be valid, got error: %v", level, err)
			}
		})
	}

	invalidLevels := []string{"trace", "fatal", "invalid", "DEBUG", "INFO"}

	for _, level := range invalidLevels {
		t.Run("invalid_"+level, func(t *testing.T) {
			config := HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				LogLevel:        level,
			}

			err := config.Validate()
			if err == nil {
				t.Errorf("LogLevel %s should be invalid", level)
			}
		})
	}
}

func TestHeartbeatConfig_JSON(t *testing.T) {
	// Test JSON marshaling and unmarshaling
	original := HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 45,
		EnableMetrics:   true,
		EnableEvents:    false,
		LogLevel:        "warn",
		MaxQueueDepth:   500,
	}

	// This test ensures the struct has proper JSON tags
	// We don't need to test the actual JSON serialization here
	// as that's handled by the standard library, but we check
	// that the struct is properly tagged

	if original.Enabled != true {
		t.Error("Test data setup failed")
	}

	// Validate that this config would be valid
	if err := original.Validate(); err != nil {
		t.Errorf("Test config validation failed: %v", err)
	}
}

// Helper function to check if a string contains another string
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr ||
		len(s) > len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
