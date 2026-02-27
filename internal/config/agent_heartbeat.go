package config

import (
	"fmt"
	"strings"
	"time"
)

// AgentHeartbeatConfig contains settings for agent heartbeat loop
// This handles agent task processing, alert delivery, and HEARTBEAT.md tasks
type AgentHeartbeatConfig struct {
	// Core loop settings
	Enabled         bool   `json:"enabled"`
	IntervalMinutes int    `json:"interval_minutes"`
	Timezone        string `json:"timezone"`

	// Quiet hours configuration (times when warning/info alerts are suppressed)
	QuietHours   QuietHoursConfig `json:"quiet_hours"`
	QuietEnabled bool             `json:"quiet_enabled"`

	// Alert processing settings
	AlertQueuePath   string           `json:"alert_queue_path"`
	AlertTargets     []AlertTarget    `json:"alert_targets"`
	AlertRetryPolicy AlertRetryPolicy `json:"alert_retry_policy"`

	// Task processing settings
	HeartbeatTaskPath string   `json:"heartbeat_task_path"`
	EnabledTaskTypes  []string `json:"enabled_task_types"`

	// Logging and debugging
	LogLevel       string `json:"log_level,omitempty"`
	VerboseLogging bool   `json:"verbose_logging,omitempty"`
}

// QuietHoursConfig defines when warning and info alerts should be suppressed
type QuietHoursConfig struct {
	StartTime string `json:"start_time"` // Format: "23:00"
	EndTime   string `json:"end_time"`   // Format: "08:00"
}

// AlertTarget defines where alerts should be delivered
type AlertTarget struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"` // "telegram", "email", "slack", etc.
	Config   map[string]string `json:"config"`
	Severity []string          `json:"severity"` // Which severities this target handles
}

// AlertRetryPolicy defines how failed alert deliveries should be retried
type AlertRetryPolicy struct {
	MaxRetries    int           `json:"max_retries"`
	RetryInterval time.Duration `json:"retry_interval"`
	BackoffFactor float64       `json:"backoff_factor"`
}

// Validate validates the agent heartbeat configuration
func (a AgentHeartbeatConfig) Validate() error {
	if !a.Enabled {
		return nil // No validation needed if disabled
	}

	if a.IntervalMinutes < 1 {
		return fmt.Errorf("agent heartbeat interval cannot be less than 1 minute (got %d)", a.IntervalMinutes)
	}

	if a.IntervalMinutes > 60 {
		return fmt.Errorf("agent heartbeat interval cannot exceed 60 minutes (got %d)", a.IntervalMinutes)
	}

	// Validate timezone
	if a.Timezone != "" {
		if _, err := time.LoadLocation(a.Timezone); err != nil {
			return fmt.Errorf("invalid timezone '%s': %w", a.Timezone, err)
		}
	}

	// Validate quiet hours
	if a.QuietEnabled {
		if err := a.QuietHours.Validate(); err != nil {
			return fmt.Errorf("invalid quiet hours configuration: %w", err)
		}
	}

	// Validate alert queue path
	if a.AlertQueuePath == "" {
		return fmt.Errorf("alert queue path cannot be empty")
	}

	// Validate alert targets
	for i, target := range a.AlertTargets {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("invalid alert target %d (%s): %w", i, target.Name, err)
		}
	}

	// Validate alert retry policy
	if err := a.AlertRetryPolicy.Validate(); err != nil {
		return fmt.Errorf("invalid alert retry policy: %w", err)
	}

	// Validate log level
	if a.LogLevel != "" {
		validLevels := []string{"debug", "info", "warn", "error"}
		valid := false
		for _, level := range validLevels {
			if a.LogLevel == level {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid log level: %s (must be one of: %s)", a.LogLevel, strings.Join(validLevels, ", "))
		}
	}

	// Validate enabled task types
	validTaskTypes := []string{"alerts", "checks", "reports", "maintenance"}
	for _, taskType := range a.EnabledTaskTypes {
		valid := false
		for _, validType := range validTaskTypes {
			if taskType == validType {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid task type: %s (must be one of: %s)", taskType, strings.Join(validTaskTypes, ", "))
		}
	}

	return nil
}

// Validate validates quiet hours configuration
func (q QuietHoursConfig) Validate() error {
	if q.StartTime == "" || q.EndTime == "" {
		return fmt.Errorf("start_time and end_time must be specified")
	}

	// Validate time formats
	if _, err := time.Parse("15:04", q.StartTime); err != nil {
		return fmt.Errorf("invalid start_time format '%s': must be HH:MM", q.StartTime)
	}

	if _, err := time.Parse("15:04", q.EndTime); err != nil {
		return fmt.Errorf("invalid end_time format '%s': must be HH:MM", q.EndTime)
	}

	return nil
}

// Validate validates alert target configuration
func (a AlertTarget) Validate() error {
	if a.Name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	if a.Type == "" {
		return fmt.Errorf("type cannot be empty")
	}

	validTypes := []string{"telegram", "email", "slack", "webhook"}
	valid := false
	for _, validType := range validTypes {
		if a.Type == validType {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid type: %s (must be one of: %s)", a.Type, strings.Join(validTypes, ", "))
	}

	// Validate severity levels
	validSeverities := []string{"critical", "warning", "info"}
	for _, severity := range a.Severity {
		valid := false
		for _, validSeverity := range validSeverities {
			if severity == validSeverity {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("invalid severity: %s (must be one of: %s)", severity, strings.Join(validSeverities, ", "))
		}
	}

	if len(a.Severity) == 0 {
		return fmt.Errorf("at least one severity level must be specified")
	}

	return nil
}

// Validate validates alert retry policy
func (a AlertRetryPolicy) Validate() error {
	if a.MaxRetries < 0 {
		return fmt.Errorf("max_retries cannot be negative (got %d)", a.MaxRetries)
	}

	if a.MaxRetries > 10 {
		return fmt.Errorf("max_retries cannot exceed 10 (got %d)", a.MaxRetries)
	}

	if a.RetryInterval < 0 {
		return fmt.Errorf("retry_interval cannot be negative")
	}

	if a.BackoffFactor < 1.0 {
		return fmt.Errorf("backoff_factor cannot be less than 1.0 (got %f)", a.BackoffFactor)
	}

	if a.BackoffFactor > 5.0 {
		return fmt.Errorf("backoff_factor cannot exceed 5.0 (got %f)", a.BackoffFactor)
	}

	return nil
}

// Interval returns the heartbeat interval as a time.Duration
func (a AgentHeartbeatConfig) Interval() time.Duration {
	return time.Duration(a.IntervalMinutes) * time.Minute
}

// GetLocation returns the configured timezone location, defaulting to UTC
func (a AgentHeartbeatConfig) GetLocation() *time.Location {
	if a.Timezone == "" {
		return time.UTC
	}

	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		// Fallback to UTC if timezone is invalid
		return time.UTC
	}

	return loc
}

// IsQuietTime checks if the given time falls within quiet hours
func (a AgentHeartbeatConfig) IsQuietTime(t time.Time) bool {
	if !a.QuietEnabled {
		return false
	}

	// Convert time to configured timezone
	loc := a.GetLocation()
	localTime := t.In(loc)

	startTime, _ := time.Parse("15:04", a.QuietHours.StartTime)
	endTime, _ := time.Parse("15:04", a.QuietHours.EndTime)

	// Create time objects for comparison (same day as localTime)
	startOfDay := time.Date(localTime.Year(), localTime.Month(), localTime.Day(), 0, 0, 0, 0, loc)
	quietStart := startOfDay.Add(time.Duration(startTime.Hour())*time.Hour + time.Duration(startTime.Minute())*time.Minute)
	quietEnd := startOfDay.Add(time.Duration(endTime.Hour())*time.Hour + time.Duration(endTime.Minute())*time.Minute)

	// Handle overnight quiet hours (e.g., 23:00 to 08:00)
	if quietEnd.Before(quietStart) {
		// Quiet hours span midnight
		return localTime.After(quietStart) || localTime.Before(quietEnd)
	}

	// Normal quiet hours within same day
	return localTime.After(quietStart) && localTime.Before(quietEnd)
}

// DefaultAgentHeartbeatConfig returns default agent heartbeat configuration
func DefaultAgentHeartbeatConfig() AgentHeartbeatConfig {
	return AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,                     // 5 minutes - more frequent than infrastructure heartbeat
		Timezone:        "America/Los_Angeles", // Pacific timezone for Jeff

		QuietEnabled: true,
		QuietHours: QuietHoursConfig{
			StartTime: "23:00", // 11:00 PM PT
			EndTime:   "08:00", // 8:00 AM PT
		},

		AlertQueuePath: "memory/alerts/pending.json",
		AlertTargets:   []AlertTarget{},
		AlertRetryPolicy: AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},

		HeartbeatTaskPath: "HEARTBEAT.md",
		EnabledTaskTypes:  []string{"alerts", "checks", "reports"},

		LogLevel:       "info",
		VerboseLogging: false,
	}
}
