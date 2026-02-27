package monitoring

import (
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"

	"conduit/internal/sessions"
)

func TestNewGatewayMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()

	if metrics == nil {
		t.Fatal("NewGatewayMetrics() returned nil")
	}

	// Check initial values
	if metrics.Status != "healthy" {
		t.Errorf("Initial status = %s, expected 'healthy'", metrics.Status)
	}

	// Check that timestamps are set
	if metrics.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	if metrics.startTime.IsZero() {
		t.Error("startTime should be set")
	}

	// Check that uptime is calculated correctly
	uptime := metrics.GetUptime()
	if uptime < 0 {
		t.Errorf("Uptime should be non-negative, got %v", uptime)
	}
}

func TestGatewayMetrics_UpdateSessionCount(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Update session counts
	active, processing, waiting, idle, total := 5, 2, 1, 2, 10
	metrics.UpdateSessionCount(active, processing, waiting, idle, total)

	// Verify the values were set
	if metrics.ActiveSessions != active {
		t.Errorf("ActiveSessions = %d, expected %d", metrics.ActiveSessions, active)
	}
	if metrics.ProcessingSessions != processing {
		t.Errorf("ProcessingSessions = %d, expected %d", metrics.ProcessingSessions, processing)
	}
	if metrics.WaitingSessions != waiting {
		t.Errorf("WaitingSessions = %d, expected %d", metrics.WaitingSessions, waiting)
	}
	if metrics.IdleSessions != idle {
		t.Errorf("IdleSessions = %d, expected %d", metrics.IdleSessions, idle)
	}
	if metrics.TotalSessions != total {
		t.Errorf("TotalSessions = %d, expected %d", metrics.TotalSessions, total)
	}
}

func TestGatewayMetrics_UpdateQueueMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()

	queueDepth, pending := 15, 8
	metrics.UpdateQueueMetrics(queueDepth, pending)

	if metrics.QueueDepth != queueDepth {
		t.Errorf("QueueDepth = %d, expected %d", metrics.QueueDepth, queueDepth)
	}
	if metrics.PendingRequests != pending {
		t.Errorf("PendingRequests = %d, expected %d", metrics.PendingRequests, pending)
	}
}

func TestGatewayMetrics_IncrementCounters(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Initial values should be 0
	if metrics.CompletedRequests != 0 {
		t.Errorf("Initial CompletedRequests = %d, expected 0", metrics.CompletedRequests)
	}
	if metrics.FailedRequests != 0 {
		t.Errorf("Initial FailedRequests = %d, expected 0", metrics.FailedRequests)
	}

	// Increment completed requests
	metrics.IncrementCompleted()
	metrics.IncrementCompleted()
	if metrics.CompletedRequests != 2 {
		t.Errorf("CompletedRequests = %d, expected 2", metrics.CompletedRequests)
	}

	// Increment failed requests
	metrics.IncrementFailed()
	if metrics.FailedRequests != 1 {
		t.Errorf("FailedRequests = %d, expected 1", metrics.FailedRequests)
	}
}

func TestGatewayMetrics_UpdateWebhookMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()

	connections, active := 3, 2
	metrics.UpdateWebhookMetrics(connections, active)

	if metrics.WebhookConnections != connections {
		t.Errorf("WebhookConnections = %d, expected %d", metrics.WebhookConnections, connections)
	}
	if metrics.ActiveWebhooks != active {
		t.Errorf("ActiveWebhooks = %d, expected %d", metrics.ActiveWebhooks, active)
	}
}

func TestGatewayMetrics_UpdateSystemMetrics(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Wait a bit to ensure uptime is measurable
	time.Sleep(10 * time.Millisecond)

	metrics.UpdateSystemMetrics()

	// Check uptime is non-negative (may be 0 for very fast execution)
	if metrics.UptimeSeconds < 0 {
		t.Errorf("UptimeSeconds = %d, expected >= 0", metrics.UptimeSeconds)
	}

	// Check memory usage is set
	if metrics.MemoryUsageBytes <= 0 {
		t.Errorf("MemoryUsageBytes = %d, expected > 0", metrics.MemoryUsageBytes)
	}

	if metrics.MemoryUsageMB <= 0 {
		t.Errorf("MemoryUsageMB = %f, expected > 0", metrics.MemoryUsageMB)
	}

	// Check goroutine count
	expectedGoroutines := runtime.NumGoroutine()
	if metrics.GoroutineCount != expectedGoroutines {
		t.Errorf("GoroutineCount = %d, expected %d", metrics.GoroutineCount, expectedGoroutines)
	}
}

func TestGatewayMetrics_SetStatus(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Test setting different statuses
	statuses := []string{"healthy", "degraded", "error"}

	for _, status := range statuses {
		metrics.SetStatus(status)
		if metrics.Status != status {
			t.Errorf("Status = %s, expected %s", metrics.Status, status)
		}
	}
}

func TestGatewayMetrics_SetVersion(t *testing.T) {
	metrics := NewGatewayMetrics()

	version := "1.0.0"
	metrics.SetVersion(version)

	if metrics.Version != version {
		t.Errorf("Version = %s, expected %s", metrics.Version, version)
	}
}

func TestGatewayMetrics_Snapshot(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Set some values
	metrics.UpdateSessionCount(5, 2, 1, 2, 10)
	metrics.UpdateQueueMetrics(15, 8)
	metrics.IncrementCompleted()
	metrics.IncrementFailed()
	metrics.SetStatus("degraded")
	metrics.SetVersion("1.2.3")

	// Take snapshot
	snapshot := metrics.Snapshot()

	// Verify snapshot matches original
	if snapshot.ActiveSessions != 5 {
		t.Errorf("Snapshot ActiveSessions = %d, expected 5", snapshot.ActiveSessions)
	}
	if snapshot.QueueDepth != 15 {
		t.Errorf("Snapshot QueueDepth = %d, expected 15", snapshot.QueueDepth)
	}
	if snapshot.CompletedRequests != 1 {
		t.Errorf("Snapshot CompletedRequests = %d, expected 1", snapshot.CompletedRequests)
	}
	if snapshot.Status != "degraded" {
		t.Errorf("Snapshot Status = %s, expected 'degraded'", snapshot.Status)
	}
	if snapshot.Version != "1.2.3" {
		t.Errorf("Snapshot Version = %s, expected '1.2.3'", snapshot.Version)
	}

	// Verify snapshot is independent (no mutex fields)
	// This is implicit in the fact that we can access fields without locks
}

func TestGatewayMetrics_IsHealthy(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Initially healthy
	if !metrics.IsHealthy() {
		t.Error("New metrics should be healthy")
	}

	// Set to unhealthy
	metrics.SetStatus("error")
	if metrics.IsHealthy() {
		t.Error("Metrics with error status should not be healthy")
	}

	// Set to degraded
	metrics.SetStatus("degraded")
	if metrics.IsHealthy() {
		t.Error("Metrics with degraded status should not be healthy")
	}

	// Set back to healthy
	metrics.SetStatus("healthy")
	if !metrics.IsHealthy() {
		t.Error("Metrics with healthy status should be healthy")
	}
}

func TestGatewayMetrics_GetUptime(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Sleep a bit to ensure some uptime
	time.Sleep(1 * time.Millisecond)

	uptime := metrics.GetUptime()
	if uptime <= 0 {
		t.Errorf("GetUptime() = %v, expected > 0", uptime)
	}

	// Sleep again and verify uptime increased
	time.Sleep(1 * time.Millisecond)
	newUptime := metrics.GetUptime()
	if newUptime <= uptime {
		t.Errorf("GetUptime() should increase over time: %v <= %v", newUptime, uptime)
	}
}

func TestGatewayMetrics_Reset(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Set some values
	metrics.UpdateSessionCount(5, 2, 1, 2, 10)
	metrics.UpdateQueueMetrics(15, 8)
	metrics.IncrementCompleted()
	metrics.IncrementFailed()
	metrics.SetStatus("error")
	metrics.SetVersion("1.0.0")

	// Store original start time
	originalStartTime := metrics.startTime

	// Reset
	metrics.Reset()

	// Check that counters are reset
	if metrics.ActiveSessions != 0 {
		t.Errorf("After reset, ActiveSessions = %d, expected 0", metrics.ActiveSessions)
	}
	if metrics.CompletedRequests != 0 {
		t.Errorf("After reset, CompletedRequests = %d, expected 0", metrics.CompletedRequests)
	}
	if metrics.Status != "healthy" {
		t.Errorf("After reset, Status = %s, expected 'healthy'", metrics.Status)
	}

	// Check that start time is preserved
	if metrics.startTime != originalStartTime {
		t.Error("Reset should preserve start time")
	}
}

func TestGatewayMetrics_ThreadSafety(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Run concurrent operations
	var wg sync.WaitGroup
	numWorkers := 10
	numOperations := 100

	// Start workers for different operations
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				metrics.IncrementCompleted()
				metrics.IncrementFailed()
				metrics.UpdateSessionCount(j, j/2, j/3, j/4, j)
				metrics.UpdateQueueMetrics(j*2, j)
				metrics.SetStatus("healthy")
				_ = metrics.Snapshot()
				_ = metrics.IsHealthy()
				_ = metrics.GetUptime()
			}
		}()
	}

	wg.Wait()

	// Verify final state makes sense
	if metrics.CompletedRequests != int64(numWorkers*numOperations) {
		t.Errorf("CompletedRequests = %d, expected %d", metrics.CompletedRequests, numWorkers*numOperations)
	}

	if metrics.FailedRequests != int64(numWorkers*numOperations) {
		t.Errorf("FailedRequests = %d, expected %d", metrics.FailedRequests, numWorkers*numOperations)
	}
}

func TestGatewayMetrics_JSON_Serialization(t *testing.T) {
	metrics := NewGatewayMetrics()

	// Set some test values
	metrics.UpdateSessionCount(5, 2, 1, 2, 10)
	metrics.UpdateQueueMetrics(15, 8)
	metrics.IncrementCompleted()
	metrics.SetStatus("degraded")
	metrics.SetVersion("1.0.0")
	metrics.UpdateSystemMetrics()

	snapshot := metrics.Snapshot()

	// Serialize to JSON
	jsonData, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("JSON marshaling failed: %v", err)
	}

	// Deserialize from JSON
	var unmarshaled MetricsSnapshot
	err = json.Unmarshal(jsonData, &unmarshaled)
	if err != nil {
		t.Fatalf("JSON unmarshaling failed: %v", err)
	}

	// Verify data integrity
	if unmarshaled.ActiveSessions != snapshot.ActiveSessions {
		t.Errorf("JSON round-trip failed for ActiveSessions: %d != %d",
			unmarshaled.ActiveSessions, snapshot.ActiveSessions)
	}

	if unmarshaled.Status != snapshot.Status {
		t.Errorf("JSON round-trip failed for Status: %s != %s",
			unmarshaled.Status, snapshot.Status)
	}

	if unmarshaled.Version != snapshot.Version {
		t.Errorf("JSON round-trip failed for Version: %s != %s",
			unmarshaled.Version, snapshot.Version)
	}

	// Verify timestamp is preserved
	if !unmarshaled.Timestamp.Equal(snapshot.Timestamp) {
		t.Errorf("JSON round-trip failed for Timestamp: %v != %v",
			unmarshaled.Timestamp, snapshot.Timestamp)
	}
}

func TestSessionState_Constants(t *testing.T) {
	// Test that session state constants are properly defined
	if sessions.SessionStateIdle != "idle" {
		t.Errorf("SessionStateIdle = %s, expected 'idle'", sessions.SessionStateIdle)
	}

	if sessions.SessionStateProcessing != "processing" {
		t.Errorf("SessionStateProcessing = %s, expected 'processing'", sessions.SessionStateProcessing)
	}

	if sessions.SessionStateWaiting != "waiting" {
		t.Errorf("SessionStateWaiting = %s, expected 'waiting'", sessions.SessionStateWaiting)
	}
}
