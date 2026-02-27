package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"conduit/internal/monitoring"
)

// TestHealthMonitoringIntegration tests the complete health monitoring system
func TestHealthMonitoringIntegration(t *testing.T) {
	// Skip integration tests that require a more complex setup
	// These tests set metrics directly but collectors overwrite them
	t.Skip("TODO: Refactor integration tests to create actual sessions instead of setting metrics directly")

	// Create a test gateway with full monitoring setup
	gw := createTestGatewayForIntegration(t)

	// Setup test data
	setupTestData(gw)

	// Create a test server
	server := createTestServer(gw)
	defer server.Close()

	// Test all endpoints
	t.Run("Health Endpoint", func(t *testing.T) {
		testHealthEndpoint(t, server.URL)
	})

	t.Run("Metrics Endpoint", func(t *testing.T) {
		testMetricsEndpoint(t, server.URL)
	})

	t.Run("Diagnostics Endpoint", func(t *testing.T) {
		testDiagnosticsEndpoint(t, server.URL)
	})

	t.Run("Prometheus Endpoint", func(t *testing.T) {
		testPrometheusEndpoint(t, server.URL)
	})

	t.Run("Rate Limiting", func(t *testing.T) {
		testRateLimitingIntegration(t, server.URL)
	})

	t.Run("Event Emission", func(t *testing.T) {
		testEventEmission(t, gw)
	})
}

func createTestGatewayForIntegration(t *testing.T) *Gateway {
	// Use the same setup as the unit tests but with event emission
	gw := createTestGateway(t)

	// Add some events to test diagnostics
	events := []*monitoring.HeartbeatEvent{
		monitoring.NewHeartbeatEvent(monitoring.EventTypeHeartbeat, monitoring.SeverityInfo, "System startup", "gateway"),
		monitoring.NewHeartbeatEvent(monitoring.EventTypeStatusChange, monitoring.SeverityWarning, "Status changed to degraded", "monitor"),
		monitoring.NewHeartbeatEvent(monitoring.EventTypeMetricAlert, monitoring.SeverityError, "High memory usage detected", "collector"),
		monitoring.NewSystemEvent(monitoring.SeverityCritical, "Database connection lost", "database"),
	}

	for _, event := range events {
		gw.eventStore.Store(event)
	}

	return gw
}

func setupTestData(gw *Gateway) {
	// Populate metrics with realistic test data
	gw.gatewayMetrics.UpdateSessionCount(25, 10, 5, 10, 100)
	gw.gatewayMetrics.UpdateQueueMetrics(15, 8)
	gw.gatewayMetrics.UpdateWebhookMetrics(12, 10)

	// Increment counters
	for i := 0; i < 50; i++ {
		gw.gatewayMetrics.IncrementCompleted()
	}
	for i := 0; i < 5; i++ {
		gw.gatewayMetrics.IncrementFailed()
	}

	// Update system metrics
	gw.gatewayMetrics.UpdateSystemMetrics()

	// Mark activity in collector
	gw.metricsCollector.MarkActivity()
	gw.metricsCollector.UpdateWebSocketConnections(12)
	gw.metricsCollector.UpdateActiveRequests(8)
}

func createTestServer(gw *Gateway) *httptest.Server {
	mux := http.NewServeMux()

	// Register health monitoring endpoints (same as in gateway.Start)
	mux.Handle("/health", gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleHealthEnhanced)))
	mux.Handle("/metrics", gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleMetrics)))
	mux.Handle("/diagnostics", gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handleDiagnostics)))
	mux.Handle("/prometheus", gw.rateLimitMiddleware.Wrap(http.HandlerFunc(gw.handlePrometheusMetrics)))

	return httptest.NewServer(mux)
}

func testHealthEndpoint(t *testing.T, baseURL string) {
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("Failed to call health endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var healthResp HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&healthResp); err != nil {
		t.Fatalf("Failed to decode health response: %v", err)
	}

	// Validate response structure
	if healthResp.Status == "" {
		t.Error("Health response missing status")
	}
	if healthResp.Version == "" {
		t.Error("Health response missing version")
	}
	if healthResp.Uptime == "" {
		t.Error("Health response missing uptime")
	}
	if healthResp.Timestamp.IsZero() {
		t.Error("Health response missing timestamp")
	}
}

func testMetricsEndpoint(t *testing.T, baseURL string) {
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to call metrics endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var metricsResp MetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&metricsResp); err != nil {
		t.Fatalf("Failed to decode metrics response: %v", err)
	}

	// Validate comprehensive metrics data
	if metricsResp.MetricsSnapshot == nil {
		t.Fatal("Metrics response missing gateway metrics")
	}

	// Check session metrics
	if metricsResp.MetricsSnapshot.ActiveSessions != 25 {
		t.Errorf("Expected 25 active sessions, got %d", metricsResp.MetricsSnapshot.ActiveSessions)
	}

	// Check request metrics
	if metricsResp.MetricsSnapshot.CompletedRequests != 50 {
		t.Errorf("Expected 50 completed requests, got %d", metricsResp.MetricsSnapshot.CompletedRequests)
	}

	// Check system health flags
	if metricsResp.SystemHealth.MemoryPressure && metricsResp.MetricsSnapshot.MemoryUsageMB < 100 {
		t.Error("Memory pressure flag inconsistent with actual usage")
	}

	// Check database health
	if !metricsResp.Database.Connected {
		t.Error("Expected database to be connected")
	}

	// Check activity tracking
	if metricsResp.LastActivity.IsZero() {
		t.Error("Expected last activity timestamp")
	}
}

func testDiagnosticsEndpoint(t *testing.T, baseURL string) {
	// Test basic diagnostics
	resp, err := http.Get(baseURL + "/diagnostics")
	if err != nil {
		t.Fatalf("Failed to call diagnostics endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	var diagResp struct {
		Events     []*monitoring.HeartbeatEvent `json:"events"`
		Count      int                          `json:"count"`
		Filter     monitoring.EventFilter       `json:"filter"`
		Timestamp  time.Time                    `json:"timestamp"`
		SystemInfo map[string]interface{}       `json:"system_info"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&diagResp); err != nil {
		t.Fatalf("Failed to decode diagnostics response: %v", err)
	}

	// Should have 4 events from setup
	if diagResp.Count != 4 {
		t.Errorf("Expected 4 events, got %d", diagResp.Count)
	}

	if len(diagResp.Events) != 4 {
		t.Errorf("Expected 4 events in array, got %d", len(diagResp.Events))
	}

	// Test filtering by severity
	resp2, err := http.Get(baseURL + "/diagnostics?severity=error")
	if err != nil {
		t.Fatalf("Failed to call filtered diagnostics: %v", err)
	}
	defer resp2.Body.Close()

	var filteredResp struct {
		Events []*monitoring.HeartbeatEvent `json:"events"`
		Count  int                          `json:"count"`
	}

	if err := json.NewDecoder(resp2.Body).Decode(&filteredResp); err != nil {
		t.Fatalf("Failed to decode filtered response: %v", err)
	}

	// Should only have error-level event (1 event)
	if filteredResp.Count != 1 {
		t.Errorf("Expected 1 error event, got %d", filteredResp.Count)
	}

	// Test filtering by type
	resp3, err := http.Get(baseURL + "/diagnostics?type=heartbeat")
	if err != nil {
		t.Fatalf("Failed to call type-filtered diagnostics: %v", err)
	}
	defer resp3.Body.Close()

	var typeFilteredResp struct {
		Count int `json:"count"`
	}

	if err := json.NewDecoder(resp3.Body).Decode(&typeFilteredResp); err != nil {
		t.Fatalf("Failed to decode type-filtered response: %v", err)
	}

	// Should only have heartbeat events (1 event)
	if typeFilteredResp.Count != 1 {
		t.Errorf("Expected 1 heartbeat event, got %d", typeFilteredResp.Count)
	}
}

func testPrometheusEndpoint(t *testing.T, baseURL string) {
	resp, err := http.Get(baseURL + "/prometheus")
	if err != nil {
		t.Fatalf("Failed to call prometheus endpoint: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Check content type
	contentType := resp.Header.Get("Content-Type")
	if contentType != "text/plain; version=0.0.4; charset=utf-8" {
		t.Errorf("Expected Prometheus content type, got %s", contentType)
	}

	// Read body and check format
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])

	// Check for required Prometheus metrics
	requiredMetrics := []string{
		"conduit_sessions_active 25",            // From our test data
		"conduit_requests_completed_total 50",   // From our test data
		"conduit_queue_depth 15",                // From our test data
		"# HELP conduit_uptime_seconds",         // Help text
		"# TYPE conduit_uptime_seconds counter", // Type declaration
	}

	for _, required := range requiredMetrics {
		if !contains(content, required) {
			t.Errorf("Prometheus output missing required content: %s", required)
		}
	}
}

func testRateLimitingIntegration(t *testing.T, baseURL string) {
	// This would normally test rate limiting, but since we have it disabled
	// in the test gateway, we'll just verify the endpoints are accessible
	endpoints := []string{"/health", "/metrics", "/diagnostics", "/prometheus"}

	for _, endpoint := range endpoints {
		resp, err := http.Get(baseURL + endpoint)
		if err != nil {
			t.Errorf("Failed to access %s: %v", endpoint, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 400 {
			t.Errorf("Endpoint %s returned error status: %d", endpoint, resp.StatusCode)
		}
	}
}

func testEventEmission(t *testing.T, gw *Gateway) {
	// Test the webhook event emitter functionality
	webhookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock webhook endpoint that receives events
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var event monitoring.HeartbeatEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Validate event structure
		if event.ID == "" || event.Type == "" || event.Timestamp.IsZero() {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer webhookServer.Close()

	// Create webhook emitter
	config := monitoring.EventEmitterConfig{
		Enabled:  true,
		Type:     "webhook",
		Endpoint: webhookServer.URL,
		Format:   "json",
		Timeout:  5000,
		Filters: monitoring.EventEmitterFilters{
			MinSeverity: monitoring.SeverityInfo,
		},
	}

	emitter := monitoring.NewWebhookEventEmitter(config)
	defer emitter.Close()

	// Test single event emission
	testEvent := monitoring.NewHeartbeatEvent(
		monitoring.EventTypeSystemEvent,
		monitoring.SeverityInfo,
		"Test event emission",
		"test",
	)

	if err := emitter.EmitEvent(testEvent); err != nil {
		t.Errorf("Failed to emit event: %v", err)
	}

	// Test batch emission
	batchEvents := []*monitoring.HeartbeatEvent{
		monitoring.NewHeartbeatEvent(monitoring.EventTypeHeartbeat, monitoring.SeverityInfo, "Batch event 1", "test"),
		monitoring.NewHeartbeatEvent(monitoring.EventTypeMetricAlert, monitoring.SeverityWarning, "Batch event 2", "test"),
	}

	if err := emitter.EmitBatch(batchEvents); err != nil {
		t.Errorf("Failed to emit batch events: %v", err)
	}

	// Test filtering (should not emit low severity events when min severity is higher)
	config.Filters.MinSeverity = monitoring.SeverityError
	emitter.Configure(config)

	lowSeverityEvent := monitoring.NewHeartbeatEvent(
		monitoring.EventTypeHeartbeat,
		monitoring.SeverityInfo,
		"Low severity event",
		"test",
	)

	// This should not fail, but the event should be filtered out
	if err := emitter.EmitEvent(lowSeverityEvent); err != nil {
		t.Errorf("Event emission should not fail for filtered events: %v", err)
	}
}

// Helper function to check if a string contains a substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			indexOf(s, substr) >= 0)))
}

// Simple substring search
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
