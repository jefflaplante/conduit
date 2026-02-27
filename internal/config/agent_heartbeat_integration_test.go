package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestConfigIntegrationWithAgentHeartbeat(t *testing.T) {
	// Test that the default configuration includes agent heartbeat settings
	cfg := Default()

	if !cfg.AgentHeartbeat.Enabled {
		t.Error("Expected agent heartbeat to be enabled by default")
	}

	if cfg.AgentHeartbeat.IntervalMinutes != 5 {
		t.Errorf("Expected interval of 5 minutes, got %d", cfg.AgentHeartbeat.IntervalMinutes)
	}

	// Test JSON marshaling/unmarshaling
	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	var unmarshaledCfg Config
	if err := json.Unmarshal(jsonData, &unmarshaledCfg); err != nil {
		t.Fatalf("Failed to unmarshal config: %v", err)
	}

	if unmarshaledCfg.AgentHeartbeat.IntervalMinutes != cfg.AgentHeartbeat.IntervalMinutes {
		t.Error("Agent heartbeat config not preserved during JSON round-trip")
	}

	if unmarshaledCfg.AgentHeartbeat.QuietHours.StartTime != cfg.AgentHeartbeat.QuietHours.StartTime {
		t.Error("Quiet hours config not preserved during JSON round-trip")
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name         string
		modifyConfig func(*Config)
		expectError  bool
	}{
		{
			name: "valid default config",
			modifyConfig: func(c *Config) {
				// No modifications - should be valid
			},
			expectError: false,
		},
		{
			name: "invalid heartbeat interval too low",
			modifyConfig: func(c *Config) {
				c.AgentHeartbeat.IntervalMinutes = 0
			},
			expectError: true,
		},
		{
			name: "invalid heartbeat interval too high",
			modifyConfig: func(c *Config) {
				c.AgentHeartbeat.IntervalMinutes = 100
			},
			expectError: true,
		},
		{
			name: "invalid quiet hours start time",
			modifyConfig: func(c *Config) {
				c.AgentHeartbeat.QuietEnabled = true
				c.AgentHeartbeat.QuietHours.StartTime = "25:00"
			},
			expectError: true,
		},
		{
			name: "disabled agent heartbeat (should be valid)",
			modifyConfig: func(c *Config) {
				c.AgentHeartbeat.Enabled = false
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.modifyConfig(cfg)

			err := cfg.Validate()

			if tt.expectError && err == nil {
				t.Error("Expected validation error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Expected no validation error but got: %v", err)
			}
		})
	}
}

func TestAgentHeartbeatInterval(t *testing.T) {
	cfg := DefaultAgentHeartbeatConfig()

	expectedDuration := 5 * time.Minute
	actualDuration := cfg.Interval()

	if actualDuration != expectedDuration {
		t.Errorf("Expected interval of %v, got %v", expectedDuration, actualDuration)
	}
}

func TestAgentHeartbeatQuietTime(t *testing.T) {
	cfg := DefaultAgentHeartbeatConfig()

	// Test times in Pacific timezone (default)
	testCases := []struct {
		hour     int
		minute   int
		expected bool
	}{
		{22, 30, false}, // 10:30 PM - not quiet time
		{23, 30, true},  // 11:30 PM - quiet time
		{2, 0, true},    // 2:00 AM - quiet time
		{7, 59, true},   // 7:59 AM - quiet time
		{8, 1, false},   // 8:01 AM - not quiet time
		{12, 0, false},  // 12:00 PM - not quiet time
	}

	loc := cfg.GetLocation()
	for _, tc := range testCases {
		testTime := time.Date(2023, 1, 1, tc.hour, tc.minute, 0, 0, loc)
		result := cfg.IsQuietTime(testTime)

		if result != tc.expected {
			t.Errorf("For time %02d:%02d, expected quiet time to be %v, got %v",
				tc.hour, tc.minute, tc.expected, result)
		}
	}
}
