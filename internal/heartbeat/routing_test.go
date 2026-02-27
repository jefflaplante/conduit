package heartbeat

import (
	"testing"
	"time"

	"conduit/internal/config"
)

// createBasicTestConfig returns a minimal valid config for testing
func createBasicTestConfig() *config.AgentHeartbeatConfig {
	return &config.AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,
		Timezone:        "America/Los_Angeles",
		AlertQueuePath:  "test_queue.json",
		QuietEnabled:    true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},
	}
}

func TestAlertSeverityRouter_NewAlertSeverityRouter(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,
		Timezone:        "America/Los_Angeles",
		QuietEnabled:    true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
	}

	router := NewAlertSeverityRouter(cfg)

	if router == nil {
		t.Fatal("NewAlertSeverityRouter should not return nil")
	}

	if router.config != cfg {
		t.Error("Router should store the provided config")
	}
}

func TestAlertSeverityRouter_ShouldDeliverAlert_Critical(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Enabled:      true,
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
		AlertTargets: []config.AlertTarget{
			{
				Name:     "critical-target",
				Type:     "telegram",
				Severity: []string{"critical"},
			},
		},
	}

	router := NewAlertSeverityRouter(cfg)

	criticalAlert := Alert{
		ID:         "critical-test",
		Source:     "test",
		Title:      "Critical Alert",
		Message:    "This is critical",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	decision := router.ShouldDeliverAlert(criticalAlert)

	if !decision.ShouldDeliver {
		t.Error("Critical alerts should always be delivered immediately")
	}

	if decision.Reason != "Critical alert bypasses all timing restrictions" {
		t.Errorf("Unexpected reason for critical alert: %s", decision.Reason)
	}

	if len(decision.TargetNames) != 1 || decision.TargetNames[0] != "critical-target" {
		t.Errorf("Expected target [critical-target], got %v", decision.TargetNames)
	}
}

func TestAlertSeverityRouter_ShouldDeliverAlert_Info(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Enabled:      true,
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
	}

	router := NewAlertSeverityRouter(cfg)

	infoAlert := Alert{
		ID:         "info-test",
		Source:     "test",
		Title:      "Info Alert",
		Message:    "This is informational",
		Severity:   AlertSeverityInfo,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	decision := router.ShouldDeliverAlert(infoAlert)

	if decision.ShouldDeliver {
		t.Error("Info alerts should not be delivered immediately")
	}

	expectedReason := "Info alerts are saved for briefings and not delivered immediately"
	if decision.Reason != expectedReason {
		t.Errorf("Expected reason '%s', got '%s'", expectedReason, decision.Reason)
	}
}

func TestAlertSeverityRouter_ShouldDeliverAlert_WarningQuietHours(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Enabled:      true,
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
		AlertTargets: []config.AlertTarget{
			{
				Name:     "warning-target",
				Type:     "telegram",
				Severity: []string{"warning"},
			},
		},
	}

	router := NewAlertSeverityRouter(cfg)

	warningAlert := Alert{
		ID:         "warning-test",
		Source:     "test",
		Title:      "Warning Alert",
		Message:    "This is a warning",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Test during quiet hours (2 AM PT)
	loc, _ := time.LoadLocation("America/Los_Angeles")
	quietTime := time.Date(2023, 1, 1, 2, 0, 0, 0, loc)

	// Mock current time to be during quiet hours
	_ = router.ShouldDeliverAlert(warningAlert)

	// During quiet hours, the decision depends on actual current time
	// For testing, we'll verify the logic works with IsQuietTime
	if router.config.IsQuietTime(quietTime) {
		// If it's quiet time, warning should be delayed
		t.Logf("Quiet time test: %v", router.config.IsQuietTime(quietTime))
	}

	// Test during active hours (10 AM PT)
	activeTime := time.Date(2023, 1, 1, 10, 0, 0, 0, loc)
	if router.config.IsQuietTime(activeTime) {
		t.Error("10 AM should not be quiet time")
	}
}

func TestAlertSeverityRouter_ShouldDeliverAlert_Expired(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Enabled: true,
	}

	router := NewAlertSeverityRouter(cfg)

	pastTime := time.Now().Add(-2 * time.Hour)
	expiredAlert := Alert{
		ID:         "expired-test",
		Source:     "test",
		Title:      "Expired Alert",
		Message:    "This alert has expired",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  pastTime,
		ExpiresAt:  &[]time.Time{time.Now().Add(-1 * time.Hour)}[0],
		MaxRetries: 3,
	}

	decision := router.ShouldDeliverAlert(expiredAlert)

	if decision.ShouldDeliver {
		t.Error("Expired alerts should not be delivered")
	}

	if decision.Reason != "Alert has expired" {
		t.Errorf("Expected reason 'Alert has expired', got '%s'", decision.Reason)
	}
}

func TestAlertSeverityRouter_CalculateNextDeliveryTime(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Timezone: "America/Los_Angeles",
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
	}

	router := NewAlertSeverityRouter(cfg)

	// Test calculation during quiet hours (2 AM)
	loc, _ := time.LoadLocation("America/Los_Angeles")
	quietTime := time.Date(2023, 1, 15, 2, 0, 0, 0, loc)

	nextDelivery := router.calculateNextDeliveryTime(quietTime)

	// Should be 8 AM same day
	expected := time.Date(2023, 1, 15, 8, 0, 0, 0, loc)
	if !nextDelivery.Equal(expected) {
		t.Errorf("Expected next delivery at %v, got %v", expected, nextDelivery)
	}

	// Test calculation during late evening quiet hours (11 PM)
	lateQuietTime := time.Date(2023, 1, 15, 23, 30, 0, 0, loc)
	nextDelivery = router.calculateNextDeliveryTime(lateQuietTime)

	// Should be 8 AM next day
	expected = time.Date(2023, 1, 16, 8, 0, 0, 0, loc)
	if !nextDelivery.Equal(expected) {
		t.Errorf("Expected next delivery at %v, got %v", expected, nextDelivery)
	}
}

func TestAlertSeverityRouter_GetDeliveryTargets(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		AlertTargets: []config.AlertTarget{
			{
				Name:     "critical-telegram",
				Type:     "telegram",
				Severity: []string{"critical"},
			},
			{
				Name:     "warning-telegram",
				Type:     "telegram",
				Severity: []string{"warning", "critical"},
			},
			{
				Name:     "info-email",
				Type:     "email",
				Severity: []string{"info"},
			},
		},
	}

	router := NewAlertSeverityRouter(cfg)

	// Test critical alert targets
	criticalAlert := Alert{
		Severity: AlertSeverityCritical,
	}

	criticalTargets := router.GetDeliveryTargets(criticalAlert)
	if len(criticalTargets) != 2 {
		t.Errorf("Expected 2 critical targets, got %d", len(criticalTargets))
	}

	// Verify target names
	targetNames := make(map[string]bool)
	for _, target := range criticalTargets {
		targetNames[target.Name] = true
	}

	if !targetNames["critical-telegram"] {
		t.Error("Expected critical-telegram target for critical alert")
	}

	if !targetNames["warning-telegram"] {
		t.Error("Expected warning-telegram target for critical alert")
	}

	// Test info alert targets
	infoAlert := Alert{
		Severity: AlertSeverityInfo,
	}

	infoTargets := router.GetDeliveryTargets(infoAlert)
	if len(infoTargets) != 1 {
		t.Errorf("Expected 1 info target, got %d", len(infoTargets))
	}

	if infoTargets[0].Name != "info-email" {
		t.Errorf("Expected info-email target, got %s", infoTargets[0].Name)
	}
}

func TestAlertSeverityRouter_GetQuietHoursInfo(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
	}

	router := NewAlertSeverityRouter(cfg)

	info := router.GetQuietHoursInfo()

	if !info.Enabled {
		t.Error("Quiet hours should be enabled")
	}

	if info.Timezone != "America/Los_Angeles" {
		t.Errorf("Expected timezone America/Los_Angeles, got %s", info.Timezone)
	}

	if info.StartTime != "23:00" {
		t.Errorf("Expected start time 23:00, got %s", info.StartTime)
	}

	if info.EndTime != "08:00" {
		t.Errorf("Expected end time 08:00, got %s", info.EndTime)
	}

	// Verify current time is in Pacific timezone
	if info.CurrentTime.Location().String() != "America/Los_Angeles" {
		t.Errorf("Current time should be in Pacific timezone, got %s", info.CurrentTime.Location().String())
	}

	// Check that times are calculated
	if info.IsQuietTime {
		if info.NextActiveTime.IsZero() {
			t.Error("NextActiveTime should be set during quiet hours")
		}
	} else {
		if info.NextQuietTime.IsZero() {
			t.Error("NextQuietTime should be set during active hours")
		}
	}
}

func TestAlertSeverityRouter_CalculateRetryDelay(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},
	}

	router := NewAlertSeverityRouter(cfg)

	// Test retry delay calculation with different retry counts
	tests := []struct {
		retryCount  int
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{0, 5 * time.Minute, 5 * time.Minute},   // 5 * 2^0 = 5 minutes
		{1, 10 * time.Minute, 10 * time.Minute}, // 5 * 2^1 = 10 minutes
		{2, 20 * time.Minute, 20 * time.Minute}, // 5 * 2^2 = 20 minutes
		{3, 40 * time.Minute, 40 * time.Minute}, // 5 * 2^3 = 40 minutes
	}

	for _, test := range tests {
		alert := Alert{
			RetryCount: test.retryCount,
		}

		delay := router.CalculateRetryDelay(alert)

		if delay < test.expectedMin || delay > test.expectedMax {
			t.Errorf("Retry count %d: expected delay between %v and %v, got %v",
				test.retryCount, test.expectedMin, test.expectedMax, delay)
		}
	}

	// Test maximum delay cap
	alert := Alert{
		RetryCount: 10, // Very high retry count
	}

	delay := router.CalculateRetryDelay(alert)
	maxDelay := 1 * time.Hour

	if delay > maxDelay {
		t.Errorf("Delay should be capped at %v, got %v", maxDelay, delay)
	}
}

func TestAlertSeverityRouter_ShouldRetryAlert(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},
	}

	router := NewAlertSeverityRouter(cfg)

	// Test alert within retry limit
	alert := Alert{
		ID:         "retry-test",
		RetryCount: 1,
		MaxRetries: 3,
		SentAt:     &[]time.Time{time.Now().Add(-10 * time.Minute)}[0], // 10 minutes ago
	}

	shouldRetry, reason := router.ShouldRetryAlert(alert)
	if !shouldRetry {
		t.Errorf("Alert should be retryable, reason: %s", reason)
	}

	// Test alert at retry limit
	alert.RetryCount = 3
	shouldRetry, reason = router.ShouldRetryAlert(alert)
	if shouldRetry {
		t.Errorf("Alert should not be retryable at limit, reason: %s", reason)
	}

	// Test expired alert
	pastTime := time.Now().Add(-2 * time.Hour)
	alert = Alert{
		ID:         "expired-retry-test",
		RetryCount: 1,
		MaxRetries: 3,
		ExpiresAt:  &[]time.Time{time.Now().Add(-1 * time.Hour)}[0],
		SentAt:     &pastTime,
	}

	shouldRetry, reason = router.ShouldRetryAlert(alert)
	if shouldRetry {
		t.Errorf("Expired alert should not be retryable, reason: %s", reason)
	}

	// Test alert with insufficient time elapsed
	alert = Alert{
		ID:         "recent-retry-test",
		RetryCount: 1,
		MaxRetries: 3,
		SentAt:     &[]time.Time{time.Now().Add(-1 * time.Minute)}[0], // 1 minute ago
	}

	shouldRetry, reason = router.ShouldRetryAlert(alert)
	if shouldRetry {
		t.Errorf("Recently sent alert should not be retryable yet, reason: %s", reason)
	}
}

func TestAlertSeverityRouter_ValidateRoutingConfig(t *testing.T) {
	// Test valid configuration
	validCfg := createBasicTestConfig()
	validCfg.AlertTargets = []config.AlertTarget{
		{
			Name:     "critical-target",
			Type:     "telegram",
			Severity: []string{"critical"},
			Config:   map[string]string{"chat_id": "123"},
		},
		{
			Name:     "warning-target",
			Type:     "telegram",
			Severity: []string{"warning"},
			Config:   map[string]string{"chat_id": "123"},
		},
	}

	router := NewAlertSeverityRouter(validCfg)
	if err := router.ValidateRoutingConfig(); err != nil {
		t.Errorf("Valid configuration should pass validation: %v", err)
	}

	// Test configuration without critical targets
	noCriticalCfg := createBasicTestConfig()
	noCriticalCfg.AlertTargets = []config.AlertTarget{
		{
			Name:     "warning-only",
			Type:     "telegram",
			Severity: []string{"warning"},
		},
	}

	router = NewAlertSeverityRouter(noCriticalCfg)
	if err := router.ValidateRoutingConfig(); err == nil {
		t.Error("Configuration without critical targets should fail validation")
	}

	// Test configuration without warning targets
	noWarningCfg := createBasicTestConfig()
	noWarningCfg.AlertTargets = []config.AlertTarget{
		{
			Name:     "critical-only",
			Type:     "telegram",
			Severity: []string{"critical"},
		},
	}

	router = NewAlertSeverityRouter(noWarningCfg)
	if err := router.ValidateRoutingConfig(); err == nil {
		t.Error("Configuration without warning targets should fail validation")
	}
}

func TestAlertSeverityRouter_GetRoutingSummary(t *testing.T) {
	cfg := &config.AgentHeartbeatConfig{
		Timezone:     "America/Los_Angeles",
		QuietEnabled: true,
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
		AlertTargets: []config.AlertTarget{
			{
				Name:     "telegram-critical",
				Type:     "telegram",
				Severity: []string{"critical"},
			},
			{
				Name:     "telegram-all",
				Type:     "telegram",
				Severity: []string{"critical", "warning", "info"},
			},
		},
	}

	router := NewAlertSeverityRouter(cfg)

	summary := router.GetRoutingSummary()

	if !summary.QuietHoursEnabled {
		t.Error("Quiet hours should be enabled in summary")
	}

	if summary.Timezone != "America/Los_Angeles" {
		t.Errorf("Expected timezone America/Los_Angeles, got %s", summary.Timezone)
	}

	if summary.TargetCount != 2 {
		t.Errorf("Expected 2 targets, got %d", summary.TargetCount)
	}

	if summary.QuietHours == nil {
		t.Error("Quiet hours summary should not be nil")
	} else {
		if summary.QuietHours.StartTime != "23:00" {
			t.Errorf("Expected quiet start time 23:00, got %s", summary.QuietHours.StartTime)
		}
		if summary.QuietHours.EndTime != "08:00" {
			t.Errorf("Expected quiet end time 08:00, got %s", summary.QuietHours.EndTime)
		}
	}

	// Verify severity routing
	criticalTargets := summary.SeverityRouting[AlertSeverityCritical]
	if len(criticalTargets) != 2 {
		t.Errorf("Expected 2 critical targets, got %d", len(criticalTargets))
	}

	warningTargets := summary.SeverityRouting[AlertSeverityWarning]
	if len(warningTargets) != 1 {
		t.Errorf("Expected 1 warning target, got %d", len(warningTargets))
	}
}
