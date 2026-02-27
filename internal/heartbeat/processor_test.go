package heartbeat

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/config"
)

// mockDeliveryFunc is a test implementation of alert delivery
type mockDeliveryFunc struct {
	calls     []mockCall
	shouldErr bool
	errMsg    string
}

type mockCall struct {
	alert  Alert
	target config.AlertTarget
	time   time.Time
}

func (m *mockDeliveryFunc) deliver(alert Alert, target config.AlertTarget) error {
	m.calls = append(m.calls, mockCall{
		alert:  alert,
		target: target,
		time:   time.Now(),
	})

	if m.shouldErr {
		return fmt.Errorf("%s", m.errMsg)
	}

	return nil
}

func (m *mockDeliveryFunc) reset() {
	m.calls = nil
	m.shouldErr = false
	m.errMsg = ""
}

func createTestConfig() *config.AgentHeartbeatConfig {
	return &config.AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,
		Timezone:        "America/Los_Angeles",
		QuietEnabled:    true,
		AlertQueuePath:  "test_queue.json",
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "08:00",
		},
		AlertTargets: []config.AlertTarget{
			{
				Name:     "telegram-critical",
				Type:     "telegram",
				Severity: []string{"critical"},
				Config:   map[string]string{"chat_id": "123"},
			},
			{
				Name:     "telegram-warning",
				Type:     "telegram",
				Severity: []string{"warning"},
				Config:   map[string]string{"chat_id": "456"},
			},
			{
				Name:     "email-all",
				Type:     "email",
				Severity: []string{"critical", "warning", "info"},
				Config:   map[string]string{"to": "test@example.com"},
			},
		},
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},
	}
}

func TestNewAlertProcessor(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "processor_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	if processor == nil {
		t.Fatal("NewAlertProcessor should not return nil")
	}

	if processor.config != cfg {
		t.Error("Processor should store the provided config")
	}

	if processor.queue == nil {
		t.Error("Processor should create a queue")
	}

	if processor.router == nil {
		t.Error("Processor should create a router")
	}
}

func TestAlertProcessorImpl_ProcessAlert_Critical(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "critical_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	criticalAlert := Alert{
		ID:         "critical-test",
		Source:     "test-source",
		Title:      "Critical Test Alert",
		Message:    "This is a critical test alert",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Add alert to queue first
	if err := processor.AddAlert(criticalAlert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Process the alert
	if err := processor.ProcessAlert(criticalAlert); err != nil {
		t.Fatalf("ProcessAlert failed: %v", err)
	}

	// Verify delivery was attempted
	if len(mockDelivery.calls) == 0 {
		t.Error("Expected delivery to be attempted")
	}

	// Verify correct targets were called
	expectedTargets := map[string]bool{
		"telegram-critical": false,
		"email-all":         false,
	}

	for _, call := range mockDelivery.calls {
		if _, exists := expectedTargets[call.target.Name]; exists {
			expectedTargets[call.target.Name] = true
		}
	}

	for target, called := range expectedTargets {
		if !called {
			t.Errorf("Expected delivery to %s for critical alert", target)
		}
	}
}

func TestAlertProcessorImpl_ProcessAlert_Info(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "info_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	infoAlert := Alert{
		ID:         "info-test",
		Source:     "test-source",
		Title:      "Info Test Alert",
		Message:    "This is an info test alert",
		Severity:   AlertSeverityInfo,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Process the alert
	err := processor.ProcessAlert(infoAlert)

	// Info alerts should be suppressed, not delivered
	if err == nil {
		t.Error("Expected info alert to be suppressed")
	}

	// Verify no delivery was attempted
	if len(mockDelivery.calls) != 0 {
		t.Errorf("Expected no delivery for info alert, got %d calls", len(mockDelivery.calls))
	}
}

func TestAlertProcessorImpl_ProcessAlert_DeliveryFailure(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "delivery_failure_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{
		shouldErr: true,
		errMsg:    "delivery failed",
	}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	criticalAlert := Alert{
		ID:         "delivery-failure-test",
		Source:     "test-source",
		Title:      "Delivery Failure Test",
		Message:    "This alert will fail delivery",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Add alert to queue first
	if err := processor.AddAlert(criticalAlert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Process the alert (should fail)
	err := processor.ProcessAlert(criticalAlert)
	if err == nil {
		t.Error("Expected ProcessAlert to fail when delivery fails")
	}

	// Verify delivery was attempted
	if len(mockDelivery.calls) == 0 {
		t.Error("Expected delivery to be attempted despite failure")
	}
}

func TestAlertProcessorImpl_ProcessAlert_PartialDeliverySuccess(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "partial_success_test.json")
	cfg := createTestConfig()

	// Mock that fails for telegram but succeeds for email
	mockDelivery := &mockDeliveryFunc{}
	conditionalDelivery := func(alert Alert, target config.AlertTarget) error {
		mockDelivery.deliver(alert, target)
		if target.Type == "telegram" {
			return fmt.Errorf("telegram delivery failed")
		}
		return nil
	}

	processor := NewAlertProcessor(queuePath, cfg, conditionalDelivery)

	criticalAlert := Alert{
		ID:         "partial-success-test",
		Source:     "test-source",
		Title:      "Partial Success Test",
		Message:    "This alert will partially succeed",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Add alert to queue first
	if err := processor.AddAlert(criticalAlert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Process the alert
	err := processor.ProcessAlert(criticalAlert)

	// Should succeed because at least one delivery succeeded (email)
	if err != nil {
		t.Errorf("Expected ProcessAlert to succeed with partial delivery: %v", err)
	}

	// Verify delivery was attempted for all targets
	if len(mockDelivery.calls) < 2 {
		t.Errorf("Expected multiple delivery attempts, got %d", len(mockDelivery.calls))
	}
}

func TestAlertProcessorImpl_AddAlert(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "add_alert_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	alert := Alert{
		ID:       "add-test",
		Source:   "test-source",
		Title:    "Add Test Alert",
		Message:  "Testing alert addition",
		Severity: AlertSeverityWarning,
		// Status and CreatedAt should be set automatically
	}

	// Add alert
	if err := processor.AddAlert(alert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Verify alert was added with default values
	stats, err := processor.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	if stats.TotalAlerts != 1 {
		t.Errorf("Expected 1 alert, got %d", stats.TotalAlerts)
	}

	if stats.PendingAlerts != 1 {
		t.Errorf("Expected 1 pending alert, got %d", stats.PendingAlerts)
	}
}

func TestAlertProcessorImpl_ProcessPendingAlerts(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "process_pending_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	// Add multiple alerts
	alerts := []Alert{
		{
			ID:         "pending-1",
			Source:     "test",
			Title:      "Pending Alert 1",
			Message:    "First pending alert",
			Severity:   AlertSeverityCritical,
			Status:     AlertStatusPending,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		},
		{
			ID:         "pending-2",
			Source:     "test",
			Title:      "Pending Alert 2",
			Message:    "Second pending alert",
			Severity:   AlertSeverityCritical,
			Status:     AlertStatusPending,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		},
		{
			ID:         "info-alert",
			Source:     "test",
			Title:      "Info Alert",
			Message:    "Info alert (should be suppressed)",
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusPending,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		},
	}

	for _, alert := range alerts {
		if err := processor.AddAlert(alert); err != nil {
			t.Fatalf("AddAlert failed for %s: %v", alert.ID, err)
		}
	}

	// Process pending alerts
	if err := processor.ProcessPendingAlerts(); err == nil {
		// Some alerts may be suppressed (info alerts), so error is expected
		t.Logf("ProcessPendingAlerts completed with suppressed alerts")
	}

	// Verify that critical alerts were processed (delivered)
	deliveredCritical := 0
	for _, call := range mockDelivery.calls {
		if call.alert.Severity == AlertSeverityCritical {
			deliveredCritical++
		}
	}

	// Should have at least attempted delivery for critical alerts
	if deliveredCritical == 0 {
		t.Error("Expected critical alerts to be delivered")
	}
}

func TestAlertProcessorImpl_GetQueueStats(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "queue_stats_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	// Add alerts with different severities
	alerts := []Alert{
		{
			ID: "critical-1", Source: "test", Title: "Critical", Message: "Critical alert",
			Severity: AlertSeverityCritical, Status: AlertStatusPending, CreatedAt: time.Now(), MaxRetries: 3,
		},
		{
			ID: "warning-1", Source: "test", Title: "Warning", Message: "Warning alert",
			Severity: AlertSeverityWarning, Status: AlertStatusPending, CreatedAt: time.Now(), MaxRetries: 3,
		},
		{
			ID: "info-1", Source: "test", Title: "Info", Message: "Info alert",
			Severity: AlertSeverityInfo, Status: AlertStatusSent, CreatedAt: time.Now(), MaxRetries: 3,
		},
	}

	for _, alert := range alerts {
		if err := processor.AddAlert(alert); err != nil {
			t.Fatalf("AddAlert failed for %s: %v", alert.ID, err)
		}
	}

	// Get stats
	stats, err := processor.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	if stats.TotalAlerts != 3 {
		t.Errorf("Expected 3 total alerts, got %d", stats.TotalAlerts)
	}

	if stats.PendingAlerts != 2 {
		t.Errorf("Expected 2 pending alerts, got %d", stats.PendingAlerts)
	}

	if stats.BySeverity[AlertSeverityCritical] != 1 {
		t.Errorf("Expected 1 critical alert, got %d", stats.BySeverity[AlertSeverityCritical])
	}
}

func TestAlertProcessorImpl_GetProcessingStats(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "processing_stats_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	stats, err := processor.GetProcessingStats()
	if err != nil {
		t.Fatalf("GetProcessingStats failed: %v", err)
	}

	// Verify queue stats
	if stats.QueueStats.TotalAlerts < 0 {
		t.Error("QueueStats should be initialized")
	}

	// Verify routing info
	if stats.RoutingInfo.TargetCount != 3 {
		t.Errorf("Expected 3 targets in routing info, got %d", stats.RoutingInfo.TargetCount)
	}

	// Verify quiet hours info
	if !stats.QuietHoursInfo.Enabled {
		t.Error("Quiet hours should be enabled in processing stats")
	}
}

func TestAlertProcessorImpl_IsHealthy(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "health_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	// Test healthy processor
	if err := processor.IsHealthy(); err != nil {
		t.Errorf("Healthy processor should pass health check: %v", err)
	}

	// Test processor without delivery function
	processorNoDelivery := NewAlertProcessor(queuePath, cfg, nil)
	if err := processorNoDelivery.IsHealthy(); err == nil {
		t.Error("Processor without delivery function should fail health check")
	}
}

func TestAlertProcessorImpl_ValidateConfiguration(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "validate_config_test.json")

	// Test valid configuration
	validCfg := createTestConfig()
	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, validCfg, mockDelivery.deliver)

	if err := processor.ValidateConfiguration(); err != nil {
		t.Errorf("Valid configuration should pass validation: %v", err)
	}

	// Test nil configuration
	processorNilCfg := NewAlertProcessor(queuePath, nil, mockDelivery.deliver)
	if err := processorNilCfg.ValidateConfiguration(); err == nil {
		t.Error("Nil configuration should fail validation")
	}

	// Test invalid configuration (no critical targets)
	invalidCfg := &config.AgentHeartbeatConfig{
		Enabled:         true,
		IntervalMinutes: 5,
		AlertTargets: []config.AlertTarget{
			{
				Name:     "warning-only",
				Type:     "telegram",
				Severity: []string{"warning"},
			},
		},
		AlertRetryPolicy: config.AlertRetryPolicy{
			MaxRetries:    3,
			RetryInterval: 5 * time.Minute,
			BackoffFactor: 2.0,
		},
	}

	processorInvalidCfg := NewAlertProcessor(queuePath, invalidCfg, mockDelivery.deliver)
	if err := processorInvalidCfg.ValidateConfiguration(); err == nil {
		t.Error("Invalid configuration should fail validation")
	}
}

func TestAlertProcessorImpl_CleanupExpiredAlerts(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "cleanup_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	now := time.Now()

	// Add alerts with different statuses and expiration times
	alerts := []Alert{
		{
			ID: "pending-valid", Source: "test", Title: "Valid", Message: "Still valid",
			Severity: AlertSeverityWarning, Status: AlertStatusPending,
			CreatedAt: now, MaxRetries: 3,
		},
		{
			ID: "sent-alert", Source: "test", Title: "Sent", Message: "Already sent",
			Severity: AlertSeverityCritical, Status: AlertStatusSent,
			CreatedAt: now, MaxRetries: 3,
		},
		{
			ID: "expired-alert", Source: "test", Title: "Expired", Message: "This expired",
			Severity: AlertSeverityInfo, Status: AlertStatusPending,
			CreatedAt:  now.Add(-2 * time.Hour),
			ExpiresAt:  &[]time.Time{now.Add(-1 * time.Hour)}[0],
			MaxRetries: 3,
		},
	}

	for _, alert := range alerts {
		if err := processor.AddAlert(alert); err != nil {
			t.Fatalf("AddAlert failed for %s: %v", alert.ID, err)
		}
	}

	// Cleanup expired alerts
	if err := processor.CleanupExpiredAlerts(); err != nil {
		t.Fatalf("CleanupExpiredAlerts failed: %v", err)
	}

	// Verify only valid pending alert remains
	stats, err := processor.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	// Should have only the pending valid alert
	if stats.PendingAlerts != 1 {
		t.Errorf("Expected 1 pending alert after cleanup, got %d", stats.PendingAlerts)
	}
}

func TestAlertProcessorImpl_ShouldProcessAlert(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "should_process_test.json")
	cfg := createTestConfig()

	mockDelivery := &mockDeliveryFunc{}
	processor := NewAlertProcessor(queuePath, cfg, mockDelivery.deliver)

	// Test valid alert
	validAlert := Alert{
		ID:         "should-process-valid",
		Source:     "test",
		Title:      "Valid Alert",
		Message:    "This should be processed",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	shouldProcess, reason := processor.ShouldProcessAlert(validAlert)
	if !shouldProcess {
		t.Errorf("Valid alert should be processed, reason: %s", reason)
	}

	// Test expired alert
	pastTime := time.Now().Add(-2 * time.Hour)
	expiredAlert := Alert{
		ID:         "should-process-expired",
		Source:     "test",
		Title:      "Expired Alert",
		Message:    "This has expired",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  pastTime,
		ExpiresAt:  &[]time.Time{time.Now().Add(-1 * time.Hour)}[0],
		MaxRetries: 3,
	}

	shouldProcess, reason = processor.ShouldProcessAlert(expiredAlert)
	if shouldProcess {
		t.Errorf("Expired alert should not be processed, reason: %s", reason)
	}

	if reason != "Alert has expired" {
		t.Errorf("Expected 'Alert has expired', got '%s'", reason)
	}
}

func TestDefaultTelegramDeliveryFunc(t *testing.T) {
	alert := Alert{
		ID:       "telegram-test",
		Source:   "test",
		Title:    "Test Alert",
		Message:  "Testing Telegram delivery",
		Severity: AlertSeverityCritical,
	}

	telegramTarget := config.AlertTarget{
		Name: "telegram-test",
		Type: "telegram",
		Config: map[string]string{
			"chat_id": "123456",
		},
	}

	// Test successful delivery
	err := DefaultTelegramDeliveryFunc(alert, telegramTarget)
	if err != nil {
		t.Errorf("DefaultTelegramDeliveryFunc should succeed: %v", err)
	}

	// Test unsupported target type
	emailTarget := config.AlertTarget{
		Name: "email-test",
		Type: "email",
	}

	err = DefaultTelegramDeliveryFunc(alert, emailTarget)
	if err == nil {
		t.Error("DefaultTelegramDeliveryFunc should fail for non-telegram targets")
	}
}

func TestFormatAlertForTelegram(t *testing.T) {
	alert := Alert{
		ID:        "format-test",
		Source:    "test-system",
		Component: "database",
		Title:     "Database Connection Failed",
		Message:   "Unable to connect to primary database",
		Details:   "Connection timeout after 30 seconds",
		Severity:  AlertSeverityCritical,
		Tags:      []string{"database", "connection", "critical"},
		CreatedAt: time.Date(2023, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	formatted := formatAlertForTelegram(alert)

	// Verify formatted message contains expected elements
	expectedElements := []string{
		"🚨", // Critical alert icon
		"Database Connection Failed",
		"test-system",
		"critical",
		"Unable to connect to primary database",
		"Connection timeout after 30 seconds",
		"database",
		"2023-01-01 12:00:00",
	}

	for _, element := range expectedElements {
		if !containsString(formatted, element) {
			t.Errorf("Formatted message should contain '%s'\nGot: %s", element, formatted)
		}
	}
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) &&
			(s[:len(substr)] == substr ||
				s[len(s)-len(substr):] == substr ||
				findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
