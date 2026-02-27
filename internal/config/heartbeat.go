package config

import (
	"fmt"
	"time"
)

// HeartbeatConfig contains settings for heartbeat monitoring
type HeartbeatConfig struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	EnableMetrics   bool   `json:"enable_metrics"`
	EnableEvents    bool   `json:"enable_events"`
	LogLevel        string `json:"log_level,omitempty"`
	MaxQueueDepth   int    `json:"max_queue_depth,omitempty"`
}

// Validate validates the heartbeat configuration
func (h HeartbeatConfig) Validate() error {
	if !h.Enabled {
		return nil // No validation needed if disabled
	}

	if h.IntervalSeconds < 10 {
		return fmt.Errorf("heartbeat interval cannot be less than 10 seconds (got %d)", h.IntervalSeconds)
	}

	if h.IntervalSeconds > 3600 {
		return fmt.Errorf("heartbeat interval cannot exceed 1 hour (got %d seconds)", h.IntervalSeconds)
	}

	if h.LogLevel != "" && h.LogLevel != "debug" && h.LogLevel != "info" && h.LogLevel != "warn" && h.LogLevel != "error" {
		return fmt.Errorf("invalid log level: %s (must be debug, info, warn, or error)", h.LogLevel)
	}

	if h.MaxQueueDepth < 0 {
		return fmt.Errorf("max queue depth cannot be negative (got %d)", h.MaxQueueDepth)
	}

	return nil
}

// Interval returns the heartbeat interval as a time.Duration
func (h HeartbeatConfig) Interval() time.Duration {
	return time.Duration(h.IntervalSeconds) * time.Second
}

// DefaultHeartbeatConfig returns default heartbeat configuration
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 30, // 30 seconds like TS Conduit
		EnableMetrics:   true,
		EnableEvents:    true,
		LogLevel:        "info",
		MaxQueueDepth:   1000,
	}
}
