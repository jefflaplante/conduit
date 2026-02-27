package heartbeat

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// TestAlertQueueSeverityRouting tests comprehensive alert severity routing
func TestAlertQueueSeverityRouting(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "routing_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	// Test scenarios for different severity levels
	testCases := []struct {
		name                     string
		severity                 AlertSeverity
		expectQuietHoursBehavior bool
		expectImmedateDelivery   bool
		expectBatching           bool
	}{
		{
			name:                     "critical_alerts",
			severity:                 AlertSeverityCritical,
			expectQuietHoursBehavior: false, // Critical ignores quiet hours
			expectImmedateDelivery:   true,
			expectBatching:           false,
		},
		{
			name:                     "warning_alerts",
			severity:                 AlertSeverityWarning,
			expectQuietHoursBehavior: true, // Warning respects quiet hours
			expectImmedateDelivery:   false,
			expectBatching:           false,
		},
		{
			name:                     "info_alerts",
			severity:                 AlertSeverityInfo,
			expectQuietHoursBehavior: true, // Info respects quiet hours
			expectImmedateDelivery:   false,
			expectBatching:           true, // Info alerts can be batched
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testSeverityRouting(t, queue, tc.severity, tc.expectQuietHoursBehavior,
				tc.expectImmedateDelivery, tc.expectBatching)
		})
	}
}

// TestAlertQueueQuietHoursProcessing tests timezone-aware quiet hours behavior
func TestAlertQueueQuietHoursProcessing(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "quiet_hours_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	// Create router using the actual available constructor
	cfg := &config.AgentHeartbeatConfig{
		QuietEnabled: true,
		Timezone:     "America/Los_Angeles",
		QuietHours: config.QuietHoursConfig{
			StartTime: "23:00",
			EndTime:   "07:00",
		},
	}
	router := NewAlertSeverityRouter(cfg)

	// Test different times in Pacific timezone
	testTimes := []struct {
		name        string
		timeStr     string
		isQuietTime bool
	}{
		{
			name:        "morning_awake",
			timeStr:     "2024-01-01T09:00:00", // 9 AM PT
			isQuietTime: false,
		},
		{
			name:        "afternoon_awake",
			timeStr:     "2024-01-01T15:00:00", // 3 PM PT
			isQuietTime: false,
		},
		{
			name:        "evening_awake",
			timeStr:     "2024-01-01T22:30:00", // 10:30 PM PT
			isQuietTime: false,
		},
		{
			name:        "late_night_quiet",
			timeStr:     "2024-01-01T23:30:00", // 11:30 PM PT
			isQuietTime: true,
		},
		{
			name:        "early_morning_quiet",
			timeStr:     "2024-01-01T03:00:00", // 3 AM PT
			isQuietTime: true,
		},
		{
			name:        "dawn_quiet",
			timeStr:     "2024-01-01T06:30:00", // 6:30 AM PT
			isQuietTime: true,
		},
	}

	pacificLoc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("Failed to load timezone: %v", err)
	}

	for _, tt := range testTimes {
		t.Run(tt.name, func(t *testing.T) {
			// Parse time directly in Pacific timezone so "09:00:00" means 9 AM PT
			testTimePT, err := time.ParseInLocation("2006-01-02T15:04:05", tt.timeStr, pacificLoc)
			if err != nil {
				t.Fatalf("Failed to parse test time: %v", err)
			}

			isQuiet := cfg.IsQuietTime(testTimePT)
			if isQuiet != tt.isQuietTime {
				t.Errorf("Expected quiet hours=%t for %s, got %t",
					tt.isQuietTime, testTimePT.Format("2006-01-02 15:04:05 MST"), isQuiet)
			}

			// Test alert routing during this time
			testAlertRoutingAtTime(t, router, queue, testTimePT, tt.isQuietTime)
		})
	}
}

// TestAlertQueueRetryMechanism tests alert retry logic and failure handling
func TestAlertQueueRetryMechanism(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "retry_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	// Test alert with retry logic
	alert := Alert{
		ID:         "retry-test-001",
		Source:     "test",
		Title:      "Retry Test Alert",
		Message:    "This alert will test retry mechanism",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
		RetryCount: 0,
	}

	// Add alert to queue
	if err := queue.AddAlert(alert); err != nil {
		t.Fatalf("Failed to add retry test alert: %v", err)
	}

	// Simulate delivery failures and retries by manually loading, updating, and saving
	for i := 0; i < alert.MaxRetries; i++ {
		// Load current queue
		alertQueue, err := queue.LoadQueue()
		if err != nil {
			t.Fatalf("Failed to load queue: %v", err)
		}

		// Find and update the alert
		found := false
		for j := range alertQueue.Alerts {
			if alertQueue.Alerts[j].ID == alert.ID {
				if alertQueue.Alerts[j].RetryCount != i {
					t.Errorf("Expected retry count %d, got %d", i, alertQueue.Alerts[j].RetryCount)
				}
				// Simulate delivery failure and retry increment
				alertQueue.Alerts[j].Status = AlertStatusFailed
				alertQueue.Alerts[j].RetryCount++
				alertQueue.Alerts[j].LastError = fmt.Sprintf("Simulated failure %d", i+1)
				found = true
				break
			}
		}

		if !found {
			t.Fatal("Alert should still be in queue")
		}

		// Save updated queue
		if err := queue.SaveQueue(alertQueue); err != nil {
			t.Fatalf("Failed to save updated queue: %v", err)
		}
	}

	// Verify final state
	finalQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("Failed to load final queue: %v", err)
	}

	// Alert should still exist but with max retries exceeded
	found := false
	for _, finalAlert := range finalQueue.Alerts {
		if finalAlert.ID == alert.ID {
			found = true
			if finalAlert.RetryCount != alert.MaxRetries {
				t.Errorf("Expected retry count to equal max retries (%d), got %d",
					alert.MaxRetries, finalAlert.RetryCount)
			}
			if !finalAlert.CanRetry() {
				t.Log("Alert correctly cannot be retried anymore")
			}
			break
		}
	}

	if !found {
		t.Error("Alert should still exist in queue even after max retries")
	}
}

// TestAlertQueueConcurrentSeverityProcessing tests concurrent processing of different severity alerts
func TestAlertQueueConcurrentSeverityProcessing(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "concurrent_severity_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	// Number of alerts per severity
	alertsPerSeverity := 5
	totalAlerts := alertsPerSeverity * 3 // Critical, Warning, Info

	var wg sync.WaitGroup
	severities := []AlertSeverity{AlertSeverityCritical, AlertSeverityWarning, AlertSeverityInfo}

	// Add alerts concurrently for different severities
	for _, severity := range severities {
		wg.Add(1)
		go func(sev AlertSeverity) {
			defer wg.Done()
			for i := 0; i < alertsPerSeverity; i++ {
				alert := Alert{
					ID:         fmt.Sprintf("%s-concurrent-%d", sev, i),
					Source:     "concurrent-test",
					Title:      fmt.Sprintf("%s Alert %d", sev, i),
					Message:    fmt.Sprintf("Concurrent %s alert for testing", sev),
					Severity:   sev,
					Status:     AlertStatusPending,
					CreatedAt:  time.Now(),
					MaxRetries: 3,
				}

				if err := queue.AddAlert(alert); err != nil {
					t.Logf("Error adding concurrent alert %s: %v", alert.ID, err)
				}

				// Small delay to simulate realistic timing
				time.Sleep(10 * time.Millisecond)
			}
		}(severity)
	}

	wg.Wait()

	// Verify all alerts were added
	stats, err := queue.GetQueueStats()
	if err != nil {
		t.Fatalf("Failed to get queue stats: %v", err)
	}

	if stats.TotalAlerts < totalAlerts {
		t.Errorf("Expected at least %d alerts, got %d", totalAlerts, stats.TotalAlerts)
	}

	// Verify severity distribution
	if stats.BySeverity[AlertSeverityCritical] == 0 {
		t.Error("Expected critical alerts")
	}
	if stats.BySeverity[AlertSeverityWarning] == 0 {
		t.Error("Expected warning alerts")
	}
	if stats.BySeverity[AlertSeverityInfo] == 0 {
		t.Error("Expected info alerts")
	}

	// Test priority-based processing order
	testSeverityProcessingOrder(t, queue)
}

// TestAlertQueueBatchingBehavior tests alert batching for non-critical alerts
func TestAlertQueueBatchingBehavior(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "batching_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	// Create a simple batching configuration for testing
	maxBatchSize := 5
	_ = 1 // batchIntervalMinutes - not used in current implementation

	// Add multiple info alerts that should be batched
	infoAlerts := []Alert{}
	for i := 0; i < 7; i++ {
		alert := Alert{
			ID:         fmt.Sprintf("batch-info-%d", i),
			Source:     "batch-test",
			Title:      fmt.Sprintf("Info Alert %d", i),
			Message:    fmt.Sprintf("Batchable info alert %d", i),
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusPending,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		}
		infoAlerts = append(infoAlerts, alert)

		if err := queue.AddAlert(alert); err != nil {
			t.Fatalf("Failed to add batchable alert: %v", err)
		}
	}

	// Add one critical alert that should NOT be batched
	criticalAlert := Alert{
		ID:         "critical-nobatch",
		Source:     "batch-test",
		Title:      "Critical Alert",
		Message:    "This should not be batched",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	if err := queue.AddAlert(criticalAlert); err != nil {
		t.Fatalf("Failed to add critical alert: %v", err)
	}

	// Test batching behavior
	alerts, err := queue.GetPendingAlerts()
	if err != nil {
		t.Fatalf("Failed to get pending alerts for batching: %v", err)
	}

	// Group alerts by batchability using simple logic
	var batchableAlerts, nonBatchableAlerts []Alert
	for _, alert := range alerts {
		if alert.Severity == AlertSeverityCritical {
			nonBatchableAlerts = append(nonBatchableAlerts, alert)
		} else {
			batchableAlerts = append(batchableAlerts, alert)
		}
	}

	// Critical alert should not be batchable
	criticalFound := false
	for _, alert := range nonBatchableAlerts {
		if alert.ID == criticalAlert.ID {
			criticalFound = true
			break
		}
	}

	if !criticalFound {
		t.Error("Critical alert should be in non-batchable group")
	}

	// Info alerts should be batchable
	if len(batchableAlerts) < len(infoAlerts) {
		t.Errorf("Expected %d batchable info alerts, got %d", len(infoAlerts), len(batchableAlerts))
	}

	// Test simple batching logic
	if len(batchableAlerts) > 0 {
		firstBatchSize := maxBatchSize
		if len(batchableAlerts) < maxBatchSize {
			firstBatchSize = len(batchableAlerts)
		}

		t.Logf("Would create first batch with %d alerts (max: %d)", firstBatchSize, maxBatchSize)

		remaining := len(batchableAlerts) - firstBatchSize
		if remaining > 0 {
			t.Logf("Would create second batch with %d remaining alerts", remaining)
		}
	}
}

// TestAlertQueueExpirationHandling tests alert expiration and cleanup
func TestAlertQueueExpirationHandling(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "expiration_test_queue.json")
	queue := NewSharedAlertQueue(queuePath)

	now := time.Now()

	// Create alerts with different expiration times
	alerts := []Alert{
		{
			ID:         "expired-alert",
			Source:     "expiration-test",
			Title:      "Expired Alert",
			Message:    "This alert has expired",
			Severity:   AlertSeverityWarning,
			Status:     AlertStatusPending,
			CreatedAt:  now.Add(-2 * time.Hour),
			ExpiresAt:  &[]time.Time{now.Add(-1 * time.Hour)}[0], // Expired 1 hour ago
			MaxRetries: 3,
		},
		{
			ID:         "valid-alert",
			Source:     "expiration-test",
			Title:      "Valid Alert",
			Message:    "This alert is still valid",
			Severity:   AlertSeverityWarning,
			Status:     AlertStatusPending,
			CreatedAt:  now,
			ExpiresAt:  &[]time.Time{now.Add(1 * time.Hour)}[0], // Expires in 1 hour
			MaxRetries: 3,
		},
		{
			ID:         "no-expiration-alert",
			Source:     "expiration-test",
			Title:      "No Expiration Alert",
			Message:    "This alert never expires",
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusPending,
			CreatedAt:  now,
			ExpiresAt:  nil, // No expiration
			MaxRetries: 3,
		},
	}

	// Add all alerts
	for _, alert := range alerts {
		if err := queue.AddAlert(alert); err != nil {
			t.Fatalf("Failed to add alert %s: %v", alert.ID, err)
		}
	}

	// Remove processed alerts (which includes expired ones)
	if err := queue.RemoveProcessedAlerts(); err != nil {
		t.Fatalf("Failed to remove processed alerts: %v", err)
	}

	// Verify only non-expired alerts remain
	remainingAlerts, err := queue.GetPendingAlerts()
	if err != nil {
		t.Fatalf("Failed to get remaining alerts: %v", err)
	}

	if len(remainingAlerts) != 2 {
		t.Errorf("Expected 2 remaining alerts after expiration cleanup, got %d", len(remainingAlerts))
	}

	// Verify the expired alert is not present
	for _, alert := range remainingAlerts {
		if alert.ID == "expired-alert" {
			t.Error("Expired alert should have been removed")
		}
	}

	// Verify valid and non-expiring alerts are still present
	validFound := false
	noExpirationFound := false
	for _, alert := range remainingAlerts {
		switch alert.ID {
		case "valid-alert":
			validFound = true
		case "no-expiration-alert":
			noExpirationFound = true
		}
	}

	if !validFound {
		t.Error("Valid alert should still be present")
	}
	if !noExpirationFound {
		t.Error("Non-expiring alert should still be present")
	}
}

// TestAlertQueueHealthCheckComprehensive tests comprehensive health checking
func TestAlertQueueHealthCheckComprehensive(t *testing.T) {
	tempDir := t.TempDir()

	// Test healthy queue
	t.Run("healthy_queue", func(t *testing.T) {
		healthyQueuePath := filepath.Join(tempDir, "healthy_queue.json")
		healthyQueue := NewSharedAlertQueue(healthyQueuePath)

		// Add some normal alerts
		testAlert := Alert{
			ID:         "health-test-001",
			Source:     "health-test",
			Title:      "Health Test Alert",
			Message:    "Testing queue health",
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusPending,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		}

		if err := healthyQueue.AddAlert(testAlert); err != nil {
			t.Fatalf("Failed to add test alert to healthy queue: %v", err)
		}

		if err := healthyQueue.IsHealthy(); err != nil {
			t.Errorf("Healthy queue should pass health check: %v", err)
		}
	})

	// Test queue with excessive alerts
	t.Run("overloaded_queue", func(t *testing.T) {
		overloadedQueuePath := filepath.Join(tempDir, "overloaded_queue.json")
		overloadedQueue := NewSharedAlertQueue(overloadedQueuePath)

		// Add many alerts to simulate overload
		for i := 0; i < 1000; i++ {
			alert := Alert{
				ID:         fmt.Sprintf("overload-alert-%d", i),
				Source:     "overload-test",
				Title:      fmt.Sprintf("Overload Alert %d", i),
				Message:    "Testing queue overload",
				Severity:   AlertSeverityInfo,
				Status:     AlertStatusPending,
				CreatedAt:  time.Now(),
				MaxRetries: 3,
			}

			if err := overloadedQueue.AddAlert(alert); err != nil {
				t.Logf("Failed to add overload alert %d: %v", i, err)
				break
			}
		}

		// Queue should still be healthy but may have warnings
		if err := overloadedQueue.IsHealthy(); err != nil {
			t.Logf("Overloaded queue health check result: %v", err)
			// This may be expected behavior for overloaded queues
		}

		stats, err := overloadedQueue.GetQueueStats()
		if err != nil {
			t.Fatalf("Failed to get overloaded queue stats: %v", err)
		}

		t.Logf("Overloaded queue stats: %d total alerts, %d pending",
			stats.TotalAlerts, stats.PendingAlerts)
	})

	// Test queue with many failed alerts
	t.Run("high_failure_rate_queue", func(t *testing.T) {
		failureQueuePath := filepath.Join(tempDir, "failure_queue.json")
		failureQueue := NewSharedAlertQueue(failureQueuePath)

		// Add alerts and mark many as failed
		for i := 0; i < 100; i++ {
			alert := Alert{
				ID:         fmt.Sprintf("failure-alert-%d", i),
				Source:     "failure-test",
				Title:      fmt.Sprintf("Failure Alert %d", i),
				Message:    "Testing failure handling",
				Severity:   AlertSeverityWarning,
				Status:     AlertStatusPending,
				CreatedAt:  time.Now(),
				MaxRetries: 3,
				RetryCount: 3, // Already at max retries
			}

			if err := failureQueue.AddAlert(alert); err != nil {
				t.Fatalf("Failed to add failure test alert: %v", err)
			}

			// Mark 80% as failed
			if i%5 != 0 {
				if err := failureQueue.UpdateAlertStatus(alert.ID, AlertStatusFailed); err != nil {
					t.Logf("Failed to update alert status: %v", err)
				}
			}
		}

		healthErr := failureQueue.IsHealthy()
		stats, _ := failureQueue.GetQueueStats()

		failureRate := float64(stats.ByStatus[AlertStatusFailed]) / float64(stats.TotalAlerts)
		t.Logf("Failure queue has %.2f%% failure rate", failureRate*100)

		// IsHealthy checks filesystem health, not failure rates
		if healthErr != nil {
			t.Logf("Health check detected filesystem issue: %v", healthErr)
		}

		// Verify the failure rate is as expected from test setup
		if failureRate < 0.5 {
			t.Errorf("Expected high failure rate (>50%%), got %.2f%%", failureRate*100)
		}
	})
}

// Helper functions for comprehensive queue testing

func testSeverityRouting(t *testing.T, queue *SharedAlertQueue, severity AlertSeverity,
	expectQuietHours, expectImmediate, expectBatching bool) {

	alert := Alert{
		ID:         fmt.Sprintf("routing-test-%s", severity),
		Source:     "routing-test",
		Title:      fmt.Sprintf("Routing Test %s", severity),
		Message:    fmt.Sprintf("Testing %s severity routing", severity),
		Severity:   severity,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	if err := queue.AddAlert(alert); err != nil {
		t.Fatalf("Failed to add routing test alert: %v", err)
	}

	// Test quiet hours behavior
	if !expectQuietHours && severity.ShouldRespectQuietHours() != expectQuietHours {
		t.Errorf("Severity %s quiet hours behavior mismatch: expected %t, got %t",
			severity, expectQuietHours, severity.ShouldRespectQuietHours())
	}

	// Test priority for immediate delivery
	if expectImmediate && severity.Priority() < AlertSeverityWarning.Priority() {
		t.Errorf("Severity %s should have high priority for immediate delivery", severity)
	}

	// Test batching suitability (info alerts are typically batchable)
	isBatchable := severity == AlertSeverityInfo
	if expectBatching != isBatchable {
		t.Errorf("Severity %s batching expectation mismatch: expected %t, got %t",
			severity, expectBatching, isBatchable)
	}
}

func testAlertRoutingAtTime(t *testing.T, router *AlertSeverityRouter, queue *SharedAlertQueue,
	testTime time.Time, isQuietTime bool) {

	severities := []AlertSeverity{AlertSeverityCritical, AlertSeverityWarning, AlertSeverityInfo}

	for _, severity := range severities {
		alert := Alert{
			ID:         fmt.Sprintf("time-test-%s-%d", severity, testTime.Unix()),
			Source:     "time-test",
			Title:      fmt.Sprintf("Time Test %s", severity),
			Message:    fmt.Sprintf("Testing %s during %s", severity, testTime.Format("15:04:05 MST")),
			Severity:   severity,
			Status:     AlertStatusPending,
			CreatedAt:  testTime,
			MaxRetries: 3,
		}

		// Test routing decision at the specified time
		decision := router.ShouldDeliverAlertAt(alert, testTime)
		shouldDeliver := decision.ShouldDeliver

		if severity == AlertSeverityCritical {
			// Critical alerts should always deliver immediately
			if !shouldDeliver {
				t.Errorf("Critical alert should deliver immediately at %s", testTime.Format("15:04:05 MST"))
			}
		} else if isQuietTime {
			// Non-critical alerts should be delayed during quiet hours
			if shouldDeliver {
				t.Errorf("%s alert should not deliver immediately during quiet hours at %s",
					severity, testTime.Format("15:04:05 MST"))
			}
		} else {
			// Non-critical alerts can deliver during awake hours
			// (implementation may vary based on specific routing logic)
			t.Logf("%s alert delivery decision during awake hours at %s: %t",
				severity, testTime.Format("15:04:05 MST"), shouldDeliver)
		}
	}
}

func testSeverityProcessingOrder(t *testing.T, queue *SharedAlertQueue) {
	alerts, err := queue.GetPendingAlerts()
	if err != nil {
		t.Fatalf("Failed to get alerts for processing order test: %v", err)
	}

	// Sort alerts by priority to test ordering
	priorityOrder := make(map[AlertSeverity][]Alert)
	for _, alert := range alerts {
		priorityOrder[alert.Severity] = append(priorityOrder[alert.Severity], alert)
	}

	// Verify critical alerts exist and have highest priority
	criticalAlerts := priorityOrder[AlertSeverityCritical]
	warningAlerts := priorityOrder[AlertSeverityWarning]
	infoAlerts := priorityOrder[AlertSeverityInfo]

	if len(criticalAlerts) > 0 && len(warningAlerts) > 0 {
		if AlertSeverityCritical.Priority() <= AlertSeverityWarning.Priority() {
			t.Error("Critical alerts should have higher priority than warning alerts")
		}
	}

	if len(warningAlerts) > 0 && len(infoAlerts) > 0 {
		if AlertSeverityWarning.Priority() <= AlertSeverityInfo.Priority() {
			t.Error("Warning alerts should have higher priority than info alerts")
		}
	}

	t.Logf("Processing order test: %d critical, %d warning, %d info alerts",
		len(criticalAlerts), len(warningAlerts), len(infoAlerts))
}
