package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAgentHeartbeatConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  AgentHeartbeatConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 5,
				Timezone:        "America/Los_Angeles",
				QuietEnabled:    true,
				QuietHours: QuietHoursConfig{
					StartTime: "23:00",
					EndTime:   "08:00",
				},
				AlertQueuePath: "memory/alerts/pending.json",
				AlertTargets: []AlertTarget{
					{
						Name:     "telegram",
						Type:     "telegram",
						Severity: []string{"critical", "warning"},
						Config:   map[string]string{"chat_id": "-123456"},
					},
				},
				AlertRetryPolicy: AlertRetryPolicy{
					MaxRetries:    3,
					RetryInterval: 5 * time.Minute,
					BackoffFactor: 2.0,
				},
				HeartbeatTaskPath: "HEARTBEAT.md",
				EnabledTaskTypes:  []string{"alerts", "checks"},
				LogLevel:          "info",
			},
			wantErr: false,
		},
		{
			name: "disabled config (should pass validation)",
			config: AgentHeartbeatConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "invalid interval - too low",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 0,
				AlertQueuePath:  "test.json",
			},
			wantErr: true,
			errMsg:  "interval cannot be less than 1 minute",
		},
		{
			name: "invalid interval - too high",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 61,
				AlertQueuePath:  "test.json",
			},
			wantErr: true,
			errMsg:  "interval cannot exceed 60 minutes",
		},
		{
			name: "invalid timezone",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 5,
				Timezone:        "Invalid/Timezone",
				AlertQueuePath:  "test.json",
			},
			wantErr: true,
			errMsg:  "invalid timezone",
		},
		{
			name: "empty alert queue path",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 5,
			},
			wantErr: true,
			errMsg:  "alert queue path cannot be empty",
		},
		{
			name: "invalid log level",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 5,
				AlertQueuePath:  "test.json",
				AlertRetryPolicy: AlertRetryPolicy{
					MaxRetries:    3,
					RetryInterval: 5 * time.Minute,
					BackoffFactor: 2.0,
				},
				LogLevel: "invalid",
			},
			wantErr: true,
			errMsg:  "invalid log level",
		},
		{
			name: "invalid task type",
			config: AgentHeartbeatConfig{
				Enabled:         true,
				IntervalMinutes: 5,
				AlertQueuePath:  "test.json",
				AlertRetryPolicy: AlertRetryPolicy{
					MaxRetries:    3,
					RetryInterval: 5 * time.Minute,
					BackoffFactor: 2.0,
				},
				EnabledTaskTypes: []string{"invalid_type"},
			},
			wantErr: true,
			errMsg:  "invalid task type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestQuietHoursConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  QuietHoursConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid quiet hours",
			config: QuietHoursConfig{
				StartTime: "23:00",
				EndTime:   "08:00",
			},
			wantErr: false,
		},
		{
			name: "empty start time",
			config: QuietHoursConfig{
				StartTime: "",
				EndTime:   "08:00",
			},
			wantErr: true,
			errMsg:  "start_time and end_time must be specified",
		},
		{
			name: "invalid start time format",
			config: QuietHoursConfig{
				StartTime: "25:00",
				EndTime:   "08:00",
			},
			wantErr: true,
			errMsg:  "invalid start_time format",
		},
		{
			name: "invalid end time format",
			config: QuietHoursConfig{
				StartTime: "23:00",
				EndTime:   "invalid",
			},
			wantErr: true,
			errMsg:  "invalid end_time format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestAlertTargetValidation(t *testing.T) {
	tests := []struct {
		name    string
		target  AlertTarget
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid telegram target",
			target: AlertTarget{
				Name:     "telegram",
				Type:     "telegram",
				Severity: []string{"critical", "warning"},
				Config:   map[string]string{"chat_id": "-123456"},
			},
			wantErr: false,
		},
		{
			name: "empty name",
			target: AlertTarget{
				Type:     "telegram",
				Severity: []string{"critical"},
			},
			wantErr: true,
			errMsg:  "name cannot be empty",
		},
		{
			name: "invalid type",
			target: AlertTarget{
				Name:     "test",
				Type:     "invalid_type",
				Severity: []string{"critical"},
			},
			wantErr: true,
			errMsg:  "invalid type",
		},
		{
			name: "invalid severity",
			target: AlertTarget{
				Name:     "test",
				Type:     "telegram",
				Severity: []string{"invalid_severity"},
			},
			wantErr: true,
			errMsg:  "invalid severity",
		},
		{
			name: "no severities",
			target: AlertTarget{
				Name:     "test",
				Type:     "telegram",
				Severity: []string{},
			},
			wantErr: true,
			errMsg:  "at least one severity level must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.target.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestAlertRetryPolicyValidation(t *testing.T) {
	tests := []struct {
		name    string
		policy  AlertRetryPolicy
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid policy",
			policy: AlertRetryPolicy{
				MaxRetries:    3,
				RetryInterval: 5 * time.Minute,
				BackoffFactor: 2.0,
			},
			wantErr: false,
		},
		{
			name: "negative max retries",
			policy: AlertRetryPolicy{
				MaxRetries:    -1,
				RetryInterval: 5 * time.Minute,
				BackoffFactor: 2.0,
			},
			wantErr: true,
			errMsg:  "max_retries cannot be negative",
		},
		{
			name: "too many max retries",
			policy: AlertRetryPolicy{
				MaxRetries:    11,
				RetryInterval: 5 * time.Minute,
				BackoffFactor: 2.0,
			},
			wantErr: true,
			errMsg:  "max_retries cannot exceed 10",
		},
		{
			name: "invalid backoff factor - too low",
			policy: AlertRetryPolicy{
				MaxRetries:    3,
				RetryInterval: 5 * time.Minute,
				BackoffFactor: 0.5,
			},
			wantErr: true,
			errMsg:  "backoff_factor cannot be less than 1.0",
		},
		{
			name: "invalid backoff factor - too high",
			policy: AlertRetryPolicy{
				MaxRetries:    3,
				RetryInterval: 5 * time.Minute,
				BackoffFactor: 6.0,
			},
			wantErr: true,
			errMsg:  "backoff_factor cannot exceed 5.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate()

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected validation error, got nil")
					return
				}
				if tt.errMsg != "" && !containsString(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected validation error: %v", err)
				}
			}
		})
	}
}

func TestAgentHeartbeatConfigInterval(t *testing.T) {
	config := AgentHeartbeatConfig{
		IntervalMinutes: 5,
	}

	expected := 5 * time.Minute
	if config.Interval() != expected {
		t.Errorf("expected interval %v, got %v", expected, config.Interval())
	}
}

func TestAgentHeartbeatConfigGetLocation(t *testing.T) {
	tests := []struct {
		name     string
		timezone string
		expected string
	}{
		{
			name:     "valid timezone",
			timezone: "America/Los_Angeles",
			expected: "America/Los_Angeles",
		},
		{
			name:     "empty timezone",
			timezone: "",
			expected: "UTC",
		},
		{
			name:     "invalid timezone",
			timezone: "Invalid/Timezone",
			expected: "UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := AgentHeartbeatConfig{
				Timezone: tt.timezone,
			}

			loc := config.GetLocation()
			if loc.String() != tt.expected {
				t.Errorf("expected location %s, got %s", tt.expected, loc.String())
			}
		})
	}
}

func TestAgentHeartbeatConfigIsQuietTime(t *testing.T) {
	config := AgentHeartbeatConfig{
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours: QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
	}

	// Create test times in Pacific timezone
	loc, _ := time.LoadLocation("America/Los_Angeles")

	tests := []struct {
		name     string
		hour     int
		expected bool
	}{
		{"early morning quiet", 3, true},
		{"end of quiet hours", 7, true},
		{"just after quiet hours", 9, false},
		{"afternoon", 15, false},
		{"evening", 20, false},
		{"start of quiet hours", 23, true},
		{"late night quiet", 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime := time.Date(2024, 1, 15, tt.hour, 30, 0, 0, loc)
			result := config.IsQuietTime(testTime)

			if result != tt.expected {
				t.Errorf("hour %d: expected %v, got %v", tt.hour, tt.expected, result)
			}
		})
	}

	// Test with quiet hours disabled
	config.QuietEnabled = false
	testTime := time.Date(2024, 1, 15, 3, 30, 0, 0, loc) // 3:30 AM (should be quiet time if enabled)
	if config.IsQuietTime(testTime) {
		t.Error("expected false when quiet hours disabled, got true")
	}
}

func TestAgentHeartbeatConfigJSONSerialization(t *testing.T) {
	config := DefaultAgentHeartbeatConfig()
	config.AlertTargets = []AlertTarget{
		{
			Name:     "telegram",
			Type:     "telegram",
			Severity: []string{"critical", "warning"},
			Config:   map[string]string{"chat_id": "-123456"},
		},
	}

	// Test marshaling
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}

	// Test unmarshaling
	var unmarshaled AgentHeartbeatConfig
	if err := json.Unmarshal(data, &unmarshaled); err != nil {
		t.Fatalf("failed to unmarshal config: %v", err)
	}

	// Validate round trip
	if err := unmarshaled.Validate(); err != nil {
		t.Errorf("unmarshaled config validation failed: %v", err)
	}

	if unmarshaled.IntervalMinutes != config.IntervalMinutes {
		t.Errorf("interval mismatch: expected %d, got %d", config.IntervalMinutes, unmarshaled.IntervalMinutes)
	}

	if unmarshaled.Timezone != config.Timezone {
		t.Errorf("timezone mismatch: expected %s, got %s", config.Timezone, unmarshaled.Timezone)
	}

	if len(unmarshaled.AlertTargets) != len(config.AlertTargets) {
		t.Errorf("alert targets count mismatch: expected %d, got %d", len(config.AlertTargets), len(unmarshaled.AlertTargets))
	}
}

func TestDefaultAgentHeartbeatConfig(t *testing.T) {
	config := DefaultAgentHeartbeatConfig()

	if err := config.Validate(); err != nil {
		t.Errorf("default config validation failed: %v", err)
	}

	if !config.Enabled {
		t.Error("expected default config to be enabled")
	}

	if config.IntervalMinutes != 5 {
		t.Errorf("expected default interval 5 minutes, got %d", config.IntervalMinutes)
	}

	if config.Timezone != "America/Los_Angeles" {
		t.Errorf("expected default timezone America/Los_Angeles, got %s", config.Timezone)
	}

	if !config.QuietEnabled {
		t.Error("expected quiet hours to be enabled by default")
	}

	if config.QuietHours.StartTime != "23:00" {
		t.Errorf("expected default quiet start time 23:00, got %s", config.QuietHours.StartTime)
	}

	if config.QuietHours.EndTime != "08:00" {
		t.Errorf("expected default quiet end time 08:00, got %s", config.QuietHours.EndTime)
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) &&
			(s[:len(substr)] == substr ||
				s[len(s)-len(substr):] == substr ||
				containsSubstring(s, substr))))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
