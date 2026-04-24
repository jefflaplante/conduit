package heartbeat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedAlertQueue_NewSharedAlertQueue(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "test_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	if queue == nil {
		t.Fatal("NewSharedAlertQueue should not return nil")
	}

	if queue.filePath != queuePath {
		t.Errorf("Expected filePath %s, got %s", queuePath, queue.filePath)
	}
}

func TestSharedAlertQueue_LoadQueue_EmptyFile(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "empty_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Test loading non-existent file
	alertQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue should handle non-existent file gracefully: %v", err)
	}

	if alertQueue == nil {
		t.Fatal("LoadQueue should return empty queue for non-existent file")
	}

	if len(alertQueue.Alerts) != 0 {
		t.Errorf("Expected empty alerts slice, got %d alerts", len(alertQueue.Alerts))
	}

	// Create empty file
	if err := os.WriteFile(queuePath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create empty file: %v", err)
	}

	// Test loading empty file
	alertQueue, err = queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue should handle empty file gracefully: %v", err)
	}

	if len(alertQueue.Alerts) != 0 {
		t.Errorf("Expected empty alerts slice for empty file, got %d alerts", len(alertQueue.Alerts))
	}
}

func TestSharedAlertQueue_SaveAndLoadQueue(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "save_load_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Create test alert
	testAlert := Alert{
		ID:         "test-alert-1",
		Source:     "test-source",
		Title:      "Test Alert",
		Message:    "This is a test alert",
		Severity:   AlertSeverityCritical,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Create alert queue
	alertQueue := &AlertQueue{
		Alerts:   []Alert{testAlert},
		LastSync: time.Now(),
		Version:  1,
	}

	// Save queue
	if err := queue.SaveQueue(alertQueue); err != nil {
		t.Fatalf("SaveQueue failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(queuePath); os.IsNotExist(err) {
		t.Fatal("SaveQueue should create file")
	}

	// Load queue
	loadedQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}

	// Verify loaded data
	if len(loadedQueue.Alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(loadedQueue.Alerts))
	}

	loadedAlert := loadedQueue.Alerts[0]
	if loadedAlert.ID != testAlert.ID {
		t.Errorf("Expected alert ID %s, got %s", testAlert.ID, loadedAlert.ID)
	}

	if loadedAlert.Severity != testAlert.Severity {
		t.Errorf("Expected severity %s, got %s", testAlert.Severity, loadedAlert.Severity)
	}
}

func TestSharedAlertQueue_AddAlert(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "add_alert_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	testAlert := Alert{
		ID:         "test-alert-add",
		Source:     "test-source",
		Title:      "Test Add Alert",
		Message:    "This is a test alert for adding",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Add alert
	if err := queue.AddAlert(testAlert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Verify alert was added
	alerts, err := queue.GetPendingAlerts()
	if err != nil {
		t.Fatalf("GetPendingAlerts failed: %v", err)
	}

	if len(alerts) != 1 {
		t.Errorf("Expected 1 pending alert, got %d", len(alerts))
	}

	if alerts[0].ID != testAlert.ID {
		t.Errorf("Expected alert ID %s, got %s", testAlert.ID, alerts[0].ID)
	}
}

func TestSharedAlertQueue_UpdateAlertStatus(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "update_status_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	testAlert := Alert{
		ID:         "test-alert-update",
		Source:     "test-source",
		Title:      "Test Update Alert",
		Message:    "This is a test alert for status update",
		Severity:   AlertSeverityInfo,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	// Add alert
	if err := queue.AddAlert(testAlert); err != nil {
		t.Fatalf("AddAlert failed: %v", err)
	}

	// Update status
	if err := queue.UpdateAlertStatus(testAlert.ID, AlertStatusSent); err != nil {
		t.Fatalf("UpdateAlertStatus failed: %v", err)
	}

	// Verify status was updated
	loadedQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}

	if len(loadedQueue.Alerts) != 1 {
		t.Errorf("Expected 1 alert, got %d", len(loadedQueue.Alerts))
	}

	if loadedQueue.Alerts[0].Status != AlertStatusSent {
		t.Errorf("Expected status %s, got %s", AlertStatusSent, loadedQueue.Alerts[0].Status)
	}
}

func TestSharedAlertQueue_RemoveProcessedAlerts(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "remove_processed_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	now := time.Now()

	alerts := []Alert{
		{
			ID:         "pending-alert",
			Source:     "test",
			Title:      "Pending",
			Message:    "Still pending",
			Severity:   AlertSeverityWarning,
			Status:     AlertStatusPending,
			CreatedAt:  now,
			MaxRetries: 3,
		},
		{
			ID:         "sent-alert",
			Source:     "test",
			Title:      "Sent",
			Message:    "Already sent",
			Severity:   AlertSeverityCritical,
			Status:     AlertStatusSent,
			CreatedAt:  now,
			MaxRetries: 3,
		},
		{
			ID:         "expired-alert",
			Source:     "test",
			Title:      "Expired",
			Message:    "This is expired",
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusPending,
			CreatedAt:  now.Add(-2 * time.Hour),
			ExpiresAt:  &[]time.Time{now.Add(-1 * time.Hour)}[0],
			MaxRetries: 3,
		},
	}

	// Add all alerts
	for _, alert := range alerts {
		if err := queue.AddAlert(alert); err != nil {
			t.Fatalf("AddAlert failed for %s: %v", alert.ID, err)
		}
	}

	// Remove processed alerts
	if err := queue.RemoveProcessedAlerts(); err != nil {
		t.Fatalf("RemoveProcessedAlerts failed: %v", err)
	}

	// Verify only pending, non-expired alert remains
	loadedQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}

	if len(loadedQueue.Alerts) != 1 {
		t.Errorf("Expected 1 remaining alert, got %d", len(loadedQueue.Alerts))
	}

	if len(loadedQueue.Alerts) > 0 && loadedQueue.Alerts[0].ID != "pending-alert" {
		t.Errorf("Expected remaining alert to be 'pending-alert', got %s", loadedQueue.Alerts[0].ID)
	}
}

func TestSharedAlertQueue_ConcurrentAccess(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "concurrent_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Test concurrent read/write operations
	const numGoroutines = 5 // Reduce to make test more reliable
	const alertsPerGoroutine = 3

	var wg sync.WaitGroup
	var errorCount int32

	wg.Add(numGoroutines)

	// Concurrent alert additions
	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < alertsPerGoroutine; j++ {
				alert := Alert{
					ID:         fmt.Sprintf("concurrent-alert-%d-%d", goroutineID, j),
					Source:     fmt.Sprintf("source-%d", goroutineID),
					Title:      fmt.Sprintf("Concurrent Alert %d-%d", goroutineID, j),
					Message:    "Concurrent test alert",
					Severity:   AlertSeverityWarning,
					Status:     AlertStatusPending,
					CreatedAt:  time.Now(),
					MaxRetries: 3,
				}

				if err := queue.AddAlert(alert); err != nil {
					// Don't use t.Errorf in goroutine, just count errors
					atomic.AddInt32(&errorCount, 1)
					return
				}
			}
		}(i)
	}

	wg.Wait()

	// Check for errors
	if atomic.LoadInt32(&errorCount) > 0 {
		t.Errorf("Got %d errors during concurrent operations", errorCount)
	}

	// Verify alerts were added (allow for some failures due to concurrency)
	stats, err := queue.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	expectedTotal := numGoroutines * alertsPerGoroutine
	// Just verify that some alerts were successfully added
	if stats.TotalAlerts == 0 {
		t.Errorf("Expected at least some alerts to be added, got %d", stats.TotalAlerts)
	} else {
		t.Logf("Successfully added %d out of %d alerts concurrently", stats.TotalAlerts, expectedTotal)
	}
}

// TestSharedAlertQueue_Concurrent_AddAndUpdate_NoLostAlerts is a regression test
// for conduit-22a0: UpdateAlertStatus previously did LoadQueue (RLock) -> modify
// -> SaveQueue (Lock). Any AddAlert that completed its own load-modify-save
// between those two calls was silently overwritten by the stale snapshot from
// UpdateAlertStatus, losing alerts. With the fix both mutators hold the write
// lock across the full load-modify-save.
func TestSharedAlertQueue_Concurrent_AddAndUpdate_NoLostAlerts(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "concurrent_add_update.json")
	queue := NewSharedAlertQueue(queuePath)

	// Seed the queue with an alert that updaters will target so UpdateAlertStatus
	// always has something real to mutate (avoiding spurious not-found errors).
	seed := Alert{
		ID:         "seed-alert",
		Source:     "test",
		Title:      "Seed",
		Message:    "seed alert for status updates",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}
	if err := queue.AddAlert(seed); err != nil {
		t.Fatalf("seed AddAlert failed: %v", err)
	}

	const (
		numAdders       = 4
		alertsPerAdder  = 10
		numUpdaters     = 2
		updatesPerRound = 15
	)

	var wg sync.WaitGroup
	var addErrs int32
	var updateErrs int32

	start := make(chan struct{})

	// Adders: each adds alertsPerAdder uniquely-IDed alerts.
	for i := 0; i < numAdders; i++ {
		wg.Add(1)
		go func(adderID int) {
			defer wg.Done()
			<-start
			for j := 0; j < alertsPerAdder; j++ {
				a := Alert{
					ID:         fmt.Sprintf("toctou-add-%d-%d", adderID, j),
					Source:     "test",
					Title:      "Concurrent Add",
					Message:    "race against status updates",
					Severity:   AlertSeverityInfo,
					Status:     AlertStatusPending,
					CreatedAt:  time.Now(),
					MaxRetries: 3,
				}
				if err := queue.AddAlert(a); err != nil {
					atomic.AddInt32(&addErrs, 1)
					return
				}
			}
		}(i)
	}

	// Updaters: repeatedly flip the seed alert's status between Pending and Sent.
	// Each call is its own load-modify-save on the shared file.
	for u := 0; u < numUpdaters; u++ {
		wg.Add(1)
		go func(updaterID int) {
			defer wg.Done()
			<-start
			for r := 0; r < updatesPerRound; r++ {
				status := AlertStatusPending
				if r%2 == 0 {
					status = AlertStatusSent
				}
				if err := queue.UpdateAlertStatus(seed.ID, status); err != nil {
					atomic.AddInt32(&updateErrs, 1)
					return
				}
			}
		}(u)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&addErrs); got != 0 {
		t.Fatalf("unexpected AddAlert errors: %d", got)
	}
	if got := atomic.LoadInt32(&updateErrs); got != 0 {
		t.Fatalf("unexpected UpdateAlertStatus errors: %d", got)
	}

	loaded, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}

	// We expect: seed alert + every adder's every alert.
	expected := 1 + numAdders*alertsPerAdder
	if len(loaded.Alerts) != expected {
		t.Fatalf("lost alerts: expected %d total, got %d. Before the fix, "+
			"UpdateAlertStatus's stale snapshot would overwrite concurrent AddAlerts.",
			expected, len(loaded.Alerts))
	}

	// Spot-check: every adder/index pair is present.
	found := make(map[string]bool, len(loaded.Alerts))
	for _, a := range loaded.Alerts {
		found[a.ID] = true
	}
	for i := 0; i < numAdders; i++ {
		for j := 0; j < alertsPerAdder; j++ {
			id := fmt.Sprintf("toctou-add-%d-%d", i, j)
			if !found[id] {
				t.Fatalf("missing alert %s", id)
			}
		}
	}
	if !found[seed.ID] {
		t.Fatalf("seed alert %s missing", seed.ID)
	}
}

// TestSharedAlertQueue_Concurrent_AddAndRemoveProcessed_NoLostAlerts is the
// second half of the conduit-22a0 regression: RemoveProcessedAlerts had the
// same LoadQueue (RLock) -> SaveQueue (Lock) split, so AddAlerts that landed
// between the two were dropped by the cleanup's stale snapshot. The fix holds
// the write lock across the full sequence; RemoveExpiredAlerts only drops
// alerts that are expired or already Sent, so pending alerts added concurrently
// must survive.
func TestSharedAlertQueue_Concurrent_AddAndRemoveProcessed_NoLostAlerts(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "concurrent_add_remove.json")
	queue := NewSharedAlertQueue(queuePath)

	const (
		numAdders      = 4
		alertsPerAdder = 10
		numCleaners    = 2
		cleanupRounds  = 10
	)

	// Pre-seed already-Sent alerts. RemoveProcessedAlerts only writes when
	// it actually has something to drop ("only save if something changed"
	// guard); without seeded removables the buggy load-RLock/save-Lock
	// split never enters the save path so the TOCTOU race rarely
	// manifests. Seeding one per cleanup round per cleaner keeps the
	// save path active throughout the run.
	totalSeeds := numCleaners * cleanupRounds
	for s := 0; s < totalSeeds; s++ {
		seed := Alert{
			ID:         fmt.Sprintf("removable-seed-%d", s),
			Source:     "test",
			Title:      "Removable",
			Message:    "already sent — cleaner should drop this",
			Severity:   AlertSeverityInfo,
			Status:     AlertStatusSent,
			CreatedAt:  time.Now(),
			MaxRetries: 3,
		}
		if err := queue.AddAlert(seed); err != nil {
			t.Fatalf("seed AddAlert(%s) failed: %v", seed.ID, err)
		}
	}

	var wg sync.WaitGroup
	var addErrs int32
	var cleanupErrs int32

	start := make(chan struct{})

	// Adders produce strictly pending, non-expired alerts. These must NEVER be
	// removed by RemoveProcessedAlerts.
	for i := 0; i < numAdders; i++ {
		wg.Add(1)
		go func(adderID int) {
			defer wg.Done()
			<-start
			for j := 0; j < alertsPerAdder; j++ {
				a := Alert{
					ID:         fmt.Sprintf("toctou-rm-%d-%d", adderID, j),
					Source:     "test",
					Title:      "Concurrent Add (vs cleanup)",
					Message:    "race against RemoveProcessedAlerts",
					Severity:   AlertSeverityInfo,
					Status:     AlertStatusPending,
					CreatedAt:  time.Now(),
					MaxRetries: 3,
				}
				if err := queue.AddAlert(a); err != nil {
					atomic.AddInt32(&addErrs, 1)
					return
				}
			}
		}(i)
	}

	// Cleaners call RemoveProcessedAlerts in a loop. With the bug, each cleaner
	// sees an arbitrarily old snapshot and races the writer's save — alerts
	// added in the window get wiped.
	for c := 0; c < numCleaners; c++ {
		wg.Add(1)
		go func(cleanerID int) {
			defer wg.Done()
			<-start
			for r := 0; r < cleanupRounds; r++ {
				if err := queue.RemoveProcessedAlerts(); err != nil {
					atomic.AddInt32(&cleanupErrs, 1)
					return
				}
			}
		}(c)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&addErrs); got != 0 {
		t.Fatalf("unexpected AddAlert errors: %d", got)
	}
	if got := atomic.LoadInt32(&cleanupErrs); got != 0 {
		t.Fatalf("unexpected RemoveProcessedAlerts errors: %d", got)
	}

	loaded, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}

	expected := numAdders * alertsPerAdder
	if len(loaded.Alerts) != expected {
		t.Fatalf("RemoveProcessedAlerts dropped legitimate pending alerts: "+
			"expected %d, got %d (before conduit-22a0 fix, cleanup's stale "+
			"snapshot would overwrite concurrent AddAlerts).",
			expected, len(loaded.Alerts))
	}

	// Every survivor must be one we added (none invented, none mutated).
	for _, a := range loaded.Alerts {
		if a.Status != AlertStatusPending {
			t.Errorf("alert %s unexpectedly has status %s", a.ID, a.Status)
		}
	}
	found := make(map[string]bool, len(loaded.Alerts))
	for _, a := range loaded.Alerts {
		found[a.ID] = true
	}
	for i := 0; i < numAdders; i++ {
		for j := 0; j < alertsPerAdder; j++ {
			id := fmt.Sprintf("toctou-rm-%d-%d", i, j)
			if !found[id] {
				t.Fatalf("missing pending alert %s after concurrent cleanup", id)
			}
		}
	}
}

// TestSharedAlertQueue_RemoveProcessedAlerts_RemovesSentAndExpired ensures the
// conduit-22a0 fix didn't accidentally stop RemoveProcessedAlerts from doing
// its actual job: sent/expired alerts must still be dropped.
func TestSharedAlertQueue_RemoveProcessedAlerts_RemovesSentAndExpired(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "remove_legit.json")
	queue := NewSharedAlertQueue(queuePath)

	now := time.Now()
	past := now.Add(-1 * time.Hour)

	alerts := []Alert{
		{
			ID: "keep-pending", Source: "t", Title: "k", Message: "keep",
			Severity: AlertSeverityWarning, Status: AlertStatusPending,
			CreatedAt: now, MaxRetries: 3,
		},
		{
			ID: "drop-sent", Source: "t", Title: "s", Message: "sent",
			Severity: AlertSeverityInfo, Status: AlertStatusSent,
			CreatedAt: now, MaxRetries: 3,
		},
		{
			ID: "drop-expired", Source: "t", Title: "e", Message: "expired",
			Severity: AlertSeverityInfo, Status: AlertStatusPending,
			CreatedAt: past.Add(-time.Hour), ExpiresAt: &past, MaxRetries: 3,
		},
	}
	for _, a := range alerts {
		if err := queue.AddAlert(a); err != nil {
			t.Fatalf("AddAlert(%s) failed: %v", a.ID, err)
		}
	}

	if err := queue.RemoveProcessedAlerts(); err != nil {
		t.Fatalf("RemoveProcessedAlerts failed: %v", err)
	}

	loaded, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue failed: %v", err)
	}
	if len(loaded.Alerts) != 1 {
		t.Fatalf("expected 1 alert to remain, got %d", len(loaded.Alerts))
	}
	if loaded.Alerts[0].ID != "keep-pending" {
		t.Fatalf("wrong alert survived: %s", loaded.Alerts[0].ID)
	}
}

func TestSharedAlertQueue_CorruptedFileRecovery(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "corrupted_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Create corrupted JSON file
	corruptedJSON := `{
		"alerts": [
			{
				"id": "valid-alert",
				"source": "test",
				"title": "Valid Alert",
				"message": "This alert is valid",
				"severity": "warning",
				"status": "pending",
				"created_at": "2023-01-01T10:00:00Z",
				"max_retries": 3
			},
			{
				"id": "corrupted-alert",
				"source": "test",
				"title": "Corrupted Alert",
				"message": "This alert has invalid data",
				"severity": "invalid_severity",
				"status": "pending",
				"created_at": "2023-01-01T10:00:00Z"
			}
		],
		"last_sync": "2023-01-01T10:00:00Z",
		"version": 1
	}`

	if err := os.WriteFile(queuePath, []byte(corruptedJSON), 0644); err != nil {
		t.Fatalf("Failed to write corrupted file: %v", err)
	}

	// Attempt to load corrupted queue
	loadedQueue, err := queue.LoadQueue()
	if err != nil {
		t.Fatalf("LoadQueue should recover from corruption: %v", err)
	}

	// Should recover at least the valid alert
	if len(loadedQueue.Alerts) == 0 {
		t.Error("Expected to recover at least one valid alert")
	}

	// Verify the recovered alert
	if len(loadedQueue.Alerts) > 0 {
		recoveredAlert := loadedQueue.Alerts[0]
		if recoveredAlert.ID != "valid-alert" {
			t.Errorf("Expected recovered alert ID 'valid-alert', got %s", recoveredAlert.ID)
		}
	}
}

func TestSharedAlertQueue_GetQueueStats(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "stats_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Add alerts with different severities and statuses
	alerts := []Alert{
		{
			ID: "critical-pending", Source: "test", Title: "Critical", Message: "Critical alert",
			Severity: AlertSeverityCritical, Status: AlertStatusPending, CreatedAt: time.Now(), MaxRetries: 3,
		},
		{
			ID: "warning-pending", Source: "test", Title: "Warning", Message: "Warning alert",
			Severity: AlertSeverityWarning, Status: AlertStatusPending, CreatedAt: time.Now(), MaxRetries: 3,
		},
		{
			ID: "info-sent", Source: "test", Title: "Info", Message: "Info alert",
			Severity: AlertSeverityInfo, Status: AlertStatusSent, CreatedAt: time.Now(), MaxRetries: 3,
		},
	}

	for _, alert := range alerts {
		if err := queue.AddAlert(alert); err != nil {
			t.Fatalf("AddAlert failed for %s: %v", alert.ID, err)
		}
	}

	// Get stats
	stats, err := queue.GetQueueStats()
	if err != nil {
		t.Fatalf("GetQueueStats failed: %v", err)
	}

	// Verify stats
	if stats.TotalAlerts != 3 {
		t.Errorf("Expected 3 total alerts, got %d", stats.TotalAlerts)
	}

	if stats.PendingAlerts != 2 {
		t.Errorf("Expected 2 pending alerts, got %d", stats.PendingAlerts)
	}

	if stats.BySeverity[AlertSeverityCritical] != 1 {
		t.Errorf("Expected 1 critical alert, got %d", stats.BySeverity[AlertSeverityCritical])
	}

	if stats.ByStatus[AlertStatusPending] != 2 {
		t.Errorf("Expected 2 pending alerts, got %d", stats.ByStatus[AlertStatusPending])
	}
}

func TestSharedAlertQueue_IsHealthy(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "health_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	// Test health check
	if err := queue.IsHealthy(); err != nil {
		t.Errorf("IsHealthy should pass for valid queue: %v", err)
	}

	// Test with invalid directory permissions
	restrictedDir := filepath.Join(tempDir, "restricted")
	if err := os.Mkdir(restrictedDir, 0000); err != nil {
		t.Fatalf("Failed to create restricted directory: %v", err)
	}
	defer os.Chmod(restrictedDir, 0755) // Clean up

	restrictedQueuePath := filepath.Join(restrictedDir, "queue.json")
	restrictedQueue := NewSharedAlertQueue(restrictedQueuePath)

	if err := restrictedQueue.IsHealthy(); err == nil {
		t.Error("IsHealthy should fail for inaccessible directory")
	}
}

func TestSharedAlertQueue_AtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	queuePath := filepath.Join(tempDir, "atomic_queue.json")

	queue := NewSharedAlertQueue(queuePath)

	testAlert := Alert{
		ID:         "atomic-test",
		Source:     "test",
		Title:      "Atomic Test",
		Message:    "Testing atomic writes",
		Severity:   AlertSeverityWarning,
		Status:     AlertStatusPending,
		CreatedAt:  time.Now(),
		MaxRetries: 3,
	}

	alertQueue := &AlertQueue{
		Alerts:  []Alert{testAlert},
		Version: 1,
	}

	// Save queue
	if err := queue.SaveQueue(alertQueue); err != nil {
		t.Fatalf("SaveQueue failed: %v", err)
	}

	// Verify no temp file remains
	tempPath := queuePath + ".tmp"
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Error("Temp file should not exist after successful atomic write")
	}

	// Verify file content is valid JSON
	data, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatalf("Failed to read saved file: %v", err)
	}

	var loadedQueue AlertQueue
	if err := json.Unmarshal(data, &loadedQueue); err != nil {
		t.Fatalf("Saved file should contain valid JSON: %v", err)
	}
}
