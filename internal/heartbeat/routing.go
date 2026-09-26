package heartbeat

import (
	"fmt"
	"time"

	"conduit/internal/config"
)

// AlertSeverityRouter handles routing logic based on alert severity and timing rules
// It implements timezone-aware quiet hours and delivery decisions
type AlertSeverityRouter struct {
	config *config.AgentHeartbeatConfig
}

// NewAlertSeverityRouter creates a new alert severity router
func NewAlertSeverityRouter(config *config.AgentHeartbeatConfig) *AlertSeverityRouter {
	return &AlertSeverityRouter{
		config: config,
	}
}

// RoutingDecision represents the decision made for an alert
type RoutingDecision struct {
	ShouldDeliver bool       `json:"should_deliver"`
	Reason        string     `json:"reason"`
	DelayUntil    *time.Time `json:"delay_until,omitempty"`
	TargetNames   []string   `json:"target_names"`
}

// ShouldDeliverAlert determines if an alert should be delivered immediately
// based on severity and current time conditions
func (r *AlertSeverityRouter) ShouldDeliverAlert(alert Alert) RoutingDecision {
	return r.ShouldDeliverAlertAt(alert, time.Now())
}

// ShouldDeliverAlertAt determines if an alert should be delivered at the given time
func (r *AlertSeverityRouter) ShouldDeliverAlertAt(alert Alert, now time.Time) RoutingDecision {

	// Critical alerts always deliver immediately
	if alert.Severity == AlertSeverityCritical {
		targets := r.getTargetsForSeverity(alert.Severity)
		return RoutingDecision{
			ShouldDeliver: true,
			Reason:        "Critical alert bypasses all timing restrictions",
			TargetNames:   targets,
		}
	}

	// Check if alert is expired
	if alert.IsExpired() {
		return RoutingDecision{
			ShouldDeliver: false,
			Reason:        "Alert has expired",
		}
	}

	// Info alerts are never delivered immediately - saved for briefings
	if alert.Severity == AlertSeverityInfo {
		return RoutingDecision{
			ShouldDeliver: false,
			Reason:        "Info alerts are saved for briefings and not delivered immediately",
		}
	}

	// Warning alerts respect quiet hours
	if alert.Severity == AlertSeverityWarning {
		if r.config.QuietEnabled && r.config.IsQuietTime(now) {
			// Calculate when quiet hours end
			nextDeliveryTime := r.calculateNextDeliveryTime(now)
			return RoutingDecision{
				ShouldDeliver: false,
				// conduit-31jg.33: describe the configured window, not a hardcoded one.
				Reason: fmt.Sprintf("Warning alert delayed due to quiet hours (%s-%s %s), will deliver at %s",
					r.config.QuietHours.StartTime, r.config.QuietHours.EndTime, r.config.GetLocation(),
					nextDeliveryTime.In(r.config.GetLocation()).Format("15:04 MST")),
				DelayUntil: &nextDeliveryTime,
			}
		}

		targets := r.getTargetsForSeverity(alert.Severity)
		return RoutingDecision{
			ShouldDeliver: true,
			Reason:        "Warning alert delivered outside quiet hours",
			TargetNames:   targets,
		}
	}

	// Unknown severity - be safe and don't deliver
	return RoutingDecision{
		ShouldDeliver: false,
		Reason:        fmt.Sprintf("Unknown alert severity: %s", alert.Severity),
	}
}

// calculateNextDeliveryTime returns when quiet hours next end. conduit-31jg.33:
// delegates to the single config implementation (wall-clock, DST-safe).
func (r *AlertSeverityRouter) calculateNextDeliveryTime(currentTime time.Time) time.Time {
	return r.config.NextQuietEnd(currentTime)
}

// GetDeliveryTargets returns the appropriate delivery targets for an alert
func (r *AlertSeverityRouter) GetDeliveryTargets(alert Alert) []config.AlertTarget {
	var targets []config.AlertTarget

	for _, target := range r.config.AlertTargets {
		if r.targetHandlesSeverity(target, alert.Severity) {
			targets = append(targets, target)
		}
	}

	return targets
}

// getTargetsForSeverity returns target names that handle the given severity
func (r *AlertSeverityRouter) getTargetsForSeverity(severity AlertSeverity) []string {
	var targetNames []string

	for _, target := range r.config.AlertTargets {
		if r.targetHandlesSeverity(target, severity) {
			targetNames = append(targetNames, target.Name)
		}
	}

	return targetNames
}

// targetHandlesSeverity checks if a target should receive alerts of the given severity
func (r *AlertSeverityRouter) targetHandlesSeverity(target config.AlertTarget, severity AlertSeverity) bool {
	for _, targetSeverity := range target.Severity {
		if targetSeverity == string(severity) {
			return true
		}
	}
	return false
}

// GetQuietHoursInfo returns information about current quiet hours status
func (r *AlertSeverityRouter) GetQuietHoursInfo() QuietHoursInfo {
	now := time.Now()
	loc := r.config.GetLocation()
	localTime := now.In(loc)

	info := QuietHoursInfo{
		Enabled:     r.config.QuietEnabled,
		CurrentTime: localTime,
		Timezone:    r.config.Timezone,
		IsQuietTime: false,
	}

	if !r.config.QuietEnabled {
		return info
	}

	info.StartTime = r.config.QuietHours.StartTime
	info.EndTime = r.config.QuietHours.EndTime
	info.IsQuietTime = r.config.IsQuietTime(now)

	if info.IsQuietTime {
		info.NextActiveTime = r.calculateNextDeliveryTime(now)
	} else {
		info.NextQuietTime = r.calculateNextQuietTime(now)
	}

	return info
}

// calculateNextQuietTime returns when quiet hours will next begin.
func (r *AlertSeverityRouter) calculateNextQuietTime(currentTime time.Time) time.Time {
	return r.config.NextQuietStart(currentTime)
}

// QuietHoursInfo provides information about quiet hours configuration and status
type QuietHoursInfo struct {
	Enabled        bool      `json:"enabled"`
	CurrentTime    time.Time `json:"current_time"`
	Timezone       string    `json:"timezone"`
	StartTime      string    `json:"start_time,omitempty"`
	EndTime        string    `json:"end_time,omitempty"`
	IsQuietTime    bool      `json:"is_quiet_time"`
	NextActiveTime time.Time `json:"next_active_time,omitempty"` // When quiet hours will end
	NextQuietTime  time.Time `json:"next_quiet_time,omitempty"`  // When quiet hours will begin
}

// ValidateRoutingConfig validates that the router configuration is valid
func (r *AlertSeverityRouter) ValidateRoutingConfig() error {
	if r.config == nil {
		return fmt.Errorf("router configuration cannot be nil")
	}

	if err := r.config.Validate(); err != nil {
		return fmt.Errorf("invalid router configuration: %w", err)
	}

	// Ensure we have at least one target for each severity
	criticalTargets := r.getTargetsForSeverity(AlertSeverityCritical)
	warningTargets := r.getTargetsForSeverity(AlertSeverityWarning)

	if len(criticalTargets) == 0 {
		return fmt.Errorf("no targets configured for critical alerts")
	}

	if len(warningTargets) == 0 {
		return fmt.Errorf("no targets configured for warning alerts")
	}

	// Info targets are optional since they're not delivered immediately

	return nil
}

// CalculateRetryDelay calculates the delay for retrying a failed alert delivery
func (r *AlertSeverityRouter) CalculateRetryDelay(alert Alert) time.Duration {
	// Use exponential backoff based on retry count
	baseDelay := r.config.AlertRetryPolicy.RetryInterval
	backoffFactor := r.config.AlertRetryPolicy.BackoffFactor

	// Calculate exponential backoff: baseDelay * (backoffFactor ^ retryCount)
	multiplier := 1.0
	for i := 0; i < alert.RetryCount; i++ {
		multiplier *= backoffFactor
	}

	delay := time.Duration(float64(baseDelay) * multiplier)

	// Cap the maximum delay at 1 hour to prevent excessive delays
	maxDelay := 1 * time.Hour
	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}

// ShouldRetryAlert determines if a failed alert should be retried
func (r *AlertSeverityRouter) ShouldRetryAlert(alert Alert) (bool, string) {
	// Check if alert has expired
	if alert.IsExpired() {
		return false, "Alert has expired"
	}

	// Check retry limit
	if alert.RetryCount >= r.config.AlertRetryPolicy.MaxRetries {
		return false, fmt.Sprintf("Maximum retry limit reached (%d/%d)",
			alert.RetryCount, r.config.AlertRetryPolicy.MaxRetries)
	}

	// Check if enough time has passed for retry. conduit-31jg.33: backoff is
	// measured from the last failed attempt (SentAt is only set on success).
	last := alert.LastAttemptAt
	if last == nil {
		last = alert.SentAt
	}
	if last != nil {
		retryDelay := r.CalculateRetryDelay(alert)
		nextRetryTime := last.Add(retryDelay)
		if time.Now().Before(nextRetryTime) {
			return false, fmt.Sprintf("Retry delay not elapsed, next retry at %s",
				nextRetryTime.Format("15:04:05"))
		}
	}

	return true, "Alert eligible for retry"
}

// GetRoutingSummary returns a summary of routing configuration
func (r *AlertSeverityRouter) GetRoutingSummary() RoutingSummary {
	summary := RoutingSummary{
		QuietHoursEnabled: r.config.QuietEnabled,
		Timezone:          r.config.Timezone,
		TargetCount:       len(r.config.AlertTargets),
		SeverityRouting:   make(map[AlertSeverity][]string),
	}

	if r.config.QuietEnabled {
		summary.QuietHours = &QuietHoursSummary{
			StartTime: r.config.QuietHours.StartTime,
			EndTime:   r.config.QuietHours.EndTime,
		}
	}

	// Build severity routing map
	summary.SeverityRouting[AlertSeverityCritical] = r.getTargetsForSeverity(AlertSeverityCritical)
	summary.SeverityRouting[AlertSeverityWarning] = r.getTargetsForSeverity(AlertSeverityWarning)
	summary.SeverityRouting[AlertSeverityInfo] = r.getTargetsForSeverity(AlertSeverityInfo)

	return summary
}

// RoutingSummary provides a summary of routing configuration
type RoutingSummary struct {
	QuietHoursEnabled bool                       `json:"quiet_hours_enabled"`
	QuietHours        *QuietHoursSummary         `json:"quiet_hours,omitempty"`
	Timezone          string                     `json:"timezone"`
	TargetCount       int                        `json:"target_count"`
	SeverityRouting   map[AlertSeverity][]string `json:"severity_routing"`
}

// QuietHoursSummary provides a summary of quiet hours configuration
type QuietHoursSummary struct {
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}
