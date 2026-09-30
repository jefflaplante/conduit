package config

import (
	"fmt"
	"slices"
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
	Model           string `json:"model,omitempty"`           // AI model for heartbeat (e.g. "ghost/qwen3.5" for local LLM)
	TimeoutSeconds  int    `json:"timeout_seconds,omitempty"` // Per-execution timeout (default: 60, increase for local LLMs)

	// Quiet hours configuration (times when warning/info alerts are suppressed)
	QuietHours   QuietHoursConfig `json:"quiet_hours"`
	QuietEnabled bool             `json:"quiet_enabled"`

	// Alert processing settings
	//
	// Deprecated: AlertQueuePath (pending.json) is owned by alert-flush.sh and
	// the HEARTBEAT.md prompt; the gateway no longer processes it
	// (conduit-31jg.59). Still accepted, with a load-time warning; when set,
	// only its directory is used, to place the deferred.json queue.
	AlertQueuePath string `json:"alert_queue_path,omitempty"`
	// AlertTargets is validated but does NOT route alerts (conduit-31jg.73).
	// Only AlertTargets[0], when type "telegram", is read: its
	// config.chat_id becomes the agent heartbeat job's target. Additional
	// targets, other types and Severity filters are reserved; alert delivery
	// goes through heartbeat.DeliveryRegistry. A load-time warning is logged
	// when reserved parts are configured.
	AlertTargets     []AlertTarget    `json:"alert_targets"`
	AlertRetryPolicy AlertRetryPolicy `json:"alert_retry_policy"`

	// JobFailureAlertThreshold is how many consecutive failed runs of a
	// scheduled job trigger one alert to AlertTargets[0] (conduit-2six).
	// 0 means the default (3); a negative value disables the alerts (the
	// failure streak is still tracked and the failure log still written).
	JobFailureAlertThreshold int `json:"job_failure_alert_threshold,omitempty"`

	// Task processing settings
	HeartbeatTaskPath string   `json:"heartbeat_task_path"`
	EnabledTaskTypes  []string `json:"enabled_task_types"`

	// Logging and debugging
	LogLevel       string `json:"log_level,omitempty" validate:"enum=debug|info|warn|error"`
	VerboseLogging bool   `json:"verbose_logging,omitempty"`
}

// QuietHoursConfig defines when warning and info alerts should be suppressed
type QuietHoursConfig struct {
	StartTime string `json:"start_time"` // Format: "23:00"
	EndTime   string `json:"end_time"`   // Format: "08:00"
}

// AlertTarget describes an alert destination. Reserved: see
// AgentHeartbeatConfig.AlertTargets — only the first telegram target's
// chat_id is used today, and nothing filters by Severity.
type AlertTarget struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"` // one of alertTargetTypes; checked by Validate
	Config   map[string]string `json:"config"`
	Severity []string          `json:"severity"` // Reserved: not used for routing
}

// alertTargetTypes are the accepted alert_targets[].type values.
//
// conduit-40qj: "email" and "slack" were dropped — no deliverer exists for
// them (SMTP is conduit-115f). Configs that still use them keep loading:
// Validate accepts them (see removedAlertTargetTypes) and the load logs a
// one-time warning that the target is ignored.
var alertTargetTypes = []string{"telegram", "webhook", "mqtt"}

// removedAlertTargetTypes were once valid alert target types. They pass
// validation (so an existing config never fails to start) but warn at load.
var removedAlertTargetTypes = map[string]bool{"email": true, "slack": true}

// AlertRetryPolicy defines how failed alert deliveries should be retried
type AlertRetryPolicy struct {
	MaxRetries    int      `json:"max_retries"`
	RetryInterval Duration `json:"retry_interval"`
	BackoffFactor float64  `json:"backoff_factor"`
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

	if err := validateEnumTags(&a); err != nil {
		return err
	}

	// Validate enabled task types ([]string can't use enum tags)
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
	if !slices.Contains(alertTargetTypes, a.Type) && !removedAlertTargetTypes[a.Type] {
		return fmt.Errorf("invalid value %q for Type (allowed: %s)", a.Type, strings.Join(alertTargetTypes, ", "))
	}

	// Validate severity levels ([]string can't use enum tags)
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

// IsQuietTime, NextQuietEnd and NextQuietStart live in quiet_hours.go
// (conduit-31jg.33).

// DefaultAgentHeartbeatConfig returns default agent heartbeat configuration
func DefaultAgentHeartbeatConfig() AgentHeartbeatConfig {
	return AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,  // 5 minutes - more frequent than infrastructure heartbeat
		Timezone:        "", // inherit top-level timezone at Load; UTC if unset (conduit-31jg.40)

		QuietEnabled: true,
		QuietHours: QuietHoursConfig{
			StartTime: "23:00", // 11:00 PM in the effective timezone
			EndTime:   "08:00", // 8:00 AM in the effective timezone
		},

		AlertTargets: []AlertTarget{},
		AlertRetryPolicy: AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: Duration(5 * time.Minute),
			BackoffFactor: 2.0,
		},

		HeartbeatTaskPath: "HEARTBEAT.md",
		EnabledTaskTypes:  []string{"alerts", "checks", "reports"},

		LogLevel:       "info",
		VerboseLogging: false,
	}
}
