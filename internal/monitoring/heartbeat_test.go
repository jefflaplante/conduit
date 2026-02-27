package monitoring

import (
	"context"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
)

// mockCollector implements a test metrics collector
type mockCollector struct {
	lastActivity  time.Time
	collectError  error
	metrics       *GatewayMetrics
	stuckSessions []string
}

func newMockCollector() *mockCollector {
	return &mockCollector{
		lastActivity: time.Now(),
		metrics:      NewGatewayMetrics(),
	}
}

func (m *mockCollector) IsIdle(duration time.Duration) bool {
	return time.Since(m.lastActivity) > duration
}

func (m *mockCollector) MarkActivity() {
	m.lastActivity = time.Now()
}

func (m *mockCollector) GetLastActivityTime() time.Time {
	return m.lastActivity
}

func (m *mockCollector) CollectMetrics(ctx context.Context) (*GatewayMetrics, error) {
	if m.collectError != nil {
		return nil, m.collectError
	}
	return m.metrics, nil
}

func (m *mockCollector) DetectStuckSessions(ctx context.Context, threshold time.Duration) ([]string, error) {
	return m.stuckSessions, nil
}

func (m *mockCollector) ValidateDatabase(ctx context.Context) error {
	return nil
}

func (m *mockCollector) UpdateWebSocketConnections(count int) {
	// no-op for mock
}

func (m *mockCollector) UpdateActiveRequests(count int) {
	// no-op for mock
}

func (m *mockCollector) UpdateQueueDepth(depth int) {
	// no-op for mock
}

func (m *mockCollector) UpdateHeartbeatJobs(total, enabled int) {
	// no-op for mock
}

func (m *mockCollector) GetHeartbeatMetrics() HeartbeatMetrics {
	return HeartbeatMetrics{}
}

// Simulate idle system
func (m *mockCollector) setIdle(duration time.Duration) {
	m.lastActivity = time.Now().Add(-duration)
}

func TestNewHeartbeatService(t *testing.T) {
	cfg := config.DefaultHeartbeatConfig()
	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	if service == nil {
		t.Fatal("Expected non-nil heartbeat service")
	}

	if service.config != cfg {
		t.Error("Expected config to be set")
	}

	if service.collector != collector {
		t.Error("Expected collector to be set")
	}

	if service.eventStore != eventStore {
		t.Error("Expected event store to be set")
	}
}

func TestHeartbeatServiceLifecycle(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10, // Very short for testing
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	// Test initial state
	if service.IsRunning() {
		t.Error("Expected service to not be running initially")
	}

	// Start the service
	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}

	if !service.IsRunning() {
		t.Error("Expected service to be running after start")
	}

	// Wait for at least one heartbeat
	time.Sleep(1500 * time.Millisecond)

	stats := service.GetStats()
	if heartbeatCount, ok := stats["heartbeat_count"].(int64); ok {
		if heartbeatCount < 1 {
			t.Errorf("Expected at least 1 heartbeat, got %d", heartbeatCount)
		}
	} else {
		t.Error("Expected heartbeat_count in stats")
	}

	// Stop the service
	err = service.Stop()
	if err != nil {
		t.Fatalf("Failed to stop heartbeat service: %v", err)
	}

	if service.IsRunning() {
		t.Error("Expected service to not be running after stop")
	}
}

func TestHeartbeatServiceIdleSkip(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
		LogLevel:        "debug",
	}

	collector := newMockCollector()
	// Set collector to idle state (3 minutes ago)
	collector.setIdle(3 * time.Minute)

	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for potential heartbeats
	time.Sleep(1500 * time.Millisecond)

	stats := service.GetStats()
	heartbeatCount := stats["heartbeat_count"].(int64)

	// Should have low heartbeat count due to idle skipping
	if heartbeatCount > 1 {
		t.Errorf("Expected low heartbeat count due to idle system, got %d", heartbeatCount)
	}

	// Now mark activity and force a heartbeat to verify heartbeats resume
	collector.MarkActivity()
	err = service.ForceHeartbeat(ctx)
	if err != nil {
		t.Fatalf("Failed to force heartbeat: %v", err)
	}

	newStats := service.GetStats()
	newHeartbeatCount := newStats["heartbeat_count"].(int64)

	if newHeartbeatCount <= heartbeatCount {
		t.Error("Expected heartbeat count to increase after activity")
	}
}

func TestHeartbeatServiceEventGeneration(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for some heartbeats
	time.Sleep(2500 * time.Millisecond)

	// Check that events were generated
	events, err := service.GetRecentEvents(10)
	if err != nil {
		t.Fatalf("Failed to get recent events: %v", err)
	}

	if len(events) == 0 {
		t.Error("Expected at least some heartbeat events")
	}

	// Verify event structure
	for _, event := range events {
		if event.Type != EventTypeHeartbeat {
			continue // Skip non-heartbeat events
		}

		if event.Source != "heartbeat_service" {
			t.Errorf("Expected heartbeat event source to be 'heartbeat_service', got %s", event.Source)
		}

		if event.Severity != SeverityInfo {
			t.Errorf("Expected heartbeat event severity to be 'info', got %s", event.Severity)
		}

		if event.Metrics == nil {
			t.Error("Expected heartbeat event to include metrics")
		}
	}
}

func TestHeartbeatServiceStuckSessionDetection(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	// Add stuck sessions
	collector.stuckSessions = []string{"stuck_session_1", "stuck_session_2"}

	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for heartbeat to detect stuck sessions
	time.Sleep(1500 * time.Millisecond)

	// Check for stuck session alerts
	alerts, err := service.GetRecentAlerts(10)
	if err != nil {
		t.Fatalf("Failed to get recent alerts: %v", err)
	}

	stuckAlerts := 0
	for _, alert := range alerts {
		if alert.Type == EventTypeSystemEvent && alert.Severity == SeverityWarning {
			stuckAlerts++
		}
	}

	expectedAlerts := 2 // One for each stuck session
	if stuckAlerts < expectedAlerts {
		t.Errorf("Expected at least %d stuck session alerts, got %d", expectedAlerts, stuckAlerts)
	}
}

func TestHeartbeatServiceDisabled(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled: false, // Disabled
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Expected no error starting disabled service, got: %v", err)
	}

	// Service should not be running
	if service.IsRunning() {
		t.Error("Expected disabled service to not be running")
	}

	// No events should be generated
	time.Sleep(500 * time.Millisecond)

	events, err := service.GetRecentEvents(10)
	if err != nil {
		t.Fatalf("Failed to get recent events: %v", err)
	}

	if len(events) > 0 {
		t.Error("Expected no events from disabled service")
	}
}

func TestHeartbeatServiceForceHeartbeat(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 3600, // Very long interval
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Force a heartbeat
	err = service.ForceHeartbeat(ctx)
	if err != nil {
		t.Fatalf("Failed to force heartbeat: %v", err)
	}

	// Check that a heartbeat occurred
	stats := service.GetStats()
	heartbeatCount := stats["heartbeat_count"].(int64)

	if heartbeatCount < 1 {
		t.Errorf("Expected at least 1 heartbeat after forcing, got %d", heartbeatCount)
	}
}

func TestHeartbeatServiceErrorHandling(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	// Simulate collector error
	collector.collectError = &testError{"test collection error"}

	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for heartbeat to encounter error
	time.Sleep(1500 * time.Millisecond)

	// Check that errors are tracked
	stats := service.GetStats()
	errorCount := stats["error_count"].(int)

	if errorCount == 0 {
		t.Error("Expected at least one error to be tracked")
	}

	lastError := stats["last_error"].(string)
	if lastError == "" {
		t.Error("Expected last error to be recorded")
	}
}

func TestHeartbeatServiceDoubleStart(t *testing.T) {
	cfg := config.DefaultHeartbeatConfig()
	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()

	// Start once
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Try to start again - should return error
	err = service.Start(ctx)
	if err == nil {
		t.Error("Expected error when starting already running service")
	}
}

func TestHeartbeatServiceStopNotRunning(t *testing.T) {
	cfg := config.DefaultHeartbeatConfig()
	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	// Stop service that was never started - should not error
	err := service.Stop()
	if err != nil {
		t.Errorf("Expected no error stopping non-running service, got: %v", err)
	}
}

// testError implements error interface for testing
type testError struct {
	message string
}

func (e *testError) Error() string {
	return e.message
}

func TestHeartbeatServiceConfigValidation(t *testing.T) {
	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	testCases := []struct {
		name        string
		config      config.HeartbeatConfig
		shouldError bool
	}{
		{
			name: "valid config",
			config: config.HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 30,
				EnableMetrics:   true,
				EnableEvents:    true,
			},
			shouldError: false,
		},
		{
			name: "interval too short",
			config: config.HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 5, // Less than 10 seconds
				EnableMetrics:   true,
				EnableEvents:    true,
			},
			shouldError: true,
		},
		{
			name: "interval too long",
			config: config.HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 4000, // More than 1 hour
				EnableMetrics:   true,
				EnableEvents:    true,
			},
			shouldError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deps := HeartbeatDependencies{
				Config:     tc.config,
				Collector:  collector,
				EventStore: eventStore,
			}

			service := NewHeartbeatService(deps)

			ctx := context.Background()
			err := service.Start(ctx)

			if tc.shouldError && err == nil {
				t.Error("Expected error for invalid config")
			}

			if !tc.shouldError && err != nil {
				t.Errorf("Expected no error for valid config, got: %v", err)
			}

			if err == nil {
				service.Stop()
			}
		})
	}
}

// TestHeartbeatServiceConcurrentAccess tests concurrent access to heartbeat service
func TestHeartbeatServiceConcurrentAccess(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(1000)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Test concurrent access to service methods
	var wg sync.WaitGroup
	concurrentUsers := 50
	operationsPerUser := 20

	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < operationsPerUser; j++ {
				// Mix of operations
				switch j % 4 {
				case 0:
					service.GetStats()
				case 1:
					service.IsRunning()
				case 2:
					service.GetRecentEvents(10)
				case 3:
					service.ForceHeartbeat(ctx)
				}
			}
		}()
	}

	wg.Wait()

	// Verify service is still running and functional
	if !service.IsRunning() {
		t.Error("Service should still be running after concurrent access")
	}

	stats := service.GetStats()
	if stats["running"] != true {
		t.Error("Service should report running status after concurrent access")
	}
}

// TestHeartbeatServiceRaceConditions tests for race conditions with -race flag
func TestHeartbeatServiceRaceConditions(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()

	// Test rapid start/stop cycles
	for i := 0; i < 10; i++ {
		err := service.Start(ctx)
		if err != nil {
			t.Fatalf("Failed to start service on iteration %d: %v", i, err)
		}

		// Immediate operations while starting
		go func() {
			service.GetStats()
			service.IsRunning()
		}()

		// Quick stop
		time.Sleep(100 * time.Millisecond)
		err = service.Stop()
		if err != nil {
			t.Fatalf("Failed to stop service on iteration %d: %v", i, err)
		}
	}
}

// TestHeartbeatServiceResourceCleanup tests proper resource cleanup
func TestHeartbeatServiceResourceCleanup(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()

	// Start service
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}

	// Let it run briefly
	time.Sleep(2500 * time.Millisecond)

	// Verify resources are being used
	stats := service.GetStats()
	if stats["heartbeat_count"].(int64) < 1 {
		t.Error("Expected at least one heartbeat")
	}

	// Stop service
	err = service.Stop()
	if err != nil {
		t.Fatalf("Failed to stop heartbeat service: %v", err)
	}

	// Verify cleanup
	if service.IsRunning() {
		t.Error("Service should not be running after stop")
	}

	// Try operations on stopped service
	err = service.ForceHeartbeat(ctx)
	if err == nil {
		t.Error("Expected error when forcing heartbeat on stopped service")
	}
}

// TestHeartbeatServiceEventFiltering tests event filtering and querying
func TestHeartbeatServiceEventFiltering(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	// Add some stuck sessions to generate alerts
	collector.stuckSessions = []string{"stuck_session_1", "stuck_session_2"}

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for multiple heartbeats and alerts
	time.Sleep(3 * time.Second)

	// Test getting recent events
	events, err := service.GetRecentEvents(20)
	if err != nil {
		t.Fatalf("Failed to get recent events: %v", err)
	}

	if len(events) == 0 {
		t.Error("Expected some events to be generated")
	}

	// Test getting alerts only
	alerts, err := service.GetRecentAlerts(10)
	if err != nil {
		t.Fatalf("Failed to get recent alerts: %v", err)
	}

	// Should have alerts for stuck sessions
	alertCount := 0
	for _, alert := range alerts {
		if alert.Severity == SeverityWarning || alert.Severity == SeverityError {
			alertCount++
		}
	}

	if alertCount < 2 { // At least 2 stuck session alerts
		t.Errorf("Expected at least 2 alerts for stuck sessions, got %d", alertCount)
	}

	// Verify event types
	heartbeatEvents := 0
	systemEvents := 0

	for _, event := range events {
		switch event.Type {
		case EventTypeHeartbeat:
			heartbeatEvents++
		case EventTypeSystemEvent:
			systemEvents++
		}
	}

	if heartbeatEvents == 0 {
		t.Error("Expected at least one heartbeat event")
	}

	if systemEvents == 0 {
		t.Error("Expected at least one system event (for stuck sessions)")
	}
}

// TestHeartbeatServiceMetricsAccuracy tests accuracy of collected metrics
func TestHeartbeatServiceMetricsAccuracy(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	// Set up predictable metrics
	collector.metrics.UpdateSessionCount(5, 3, 1, 1, 10)
	collector.metrics.UpdateQueueMetrics(15, 8)
	collector.metrics.IncrementCompleted()
	collector.metrics.IncrementCompleted()
	collector.metrics.IncrementFailed()

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}
	defer service.Stop()

	// Wait for heartbeat to collect metrics
	time.Sleep(1500 * time.Millisecond)

	// Get events and verify metrics are accurate
	events, err := service.GetRecentEvents(10)
	if err != nil {
		t.Fatalf("Failed to get recent events: %v", err)
	}

	// Find the latest heartbeat event with metrics
	var latestHeartbeatEvent *HeartbeatEvent
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == EventTypeHeartbeat && events[i].Metrics != nil {
			latestHeartbeatEvent = events[i]
			break
		}
	}

	if latestHeartbeatEvent == nil {
		t.Fatal("No heartbeat event with metrics found")
	}

	metrics := latestHeartbeatEvent.Metrics

	// Verify metrics accuracy
	if metrics.ActiveSessions != 5 {
		t.Errorf("Expected 5 active sessions, got %d", metrics.ActiveSessions)
	}

	if metrics.ProcessingSessions != 3 {
		t.Errorf("Expected 3 processing sessions, got %d", metrics.ProcessingSessions)
	}

	if metrics.QueueDepth != 15 {
		t.Errorf("Expected queue depth 15, got %d", metrics.QueueDepth)
	}

	if metrics.CompletedRequests < 2 {
		t.Errorf("Expected at least 2 completed requests, got %d", metrics.CompletedRequests)
	}

	if metrics.FailedRequests < 1 {
		t.Errorf("Expected at least 1 failed request, got %d", metrics.FailedRequests)
	}
}

// TestHeartbeatServiceLogLevels tests different log levels
func TestHeartbeatServiceLogLevels(t *testing.T) {
	testCases := []struct {
		name     string
		logLevel string
		expected bool // Whether debug messages should be logged
	}{
		{
			name:     "debug level",
			logLevel: "debug",
			expected: true,
		},
		{
			name:     "info level",
			logLevel: "info",
			expected: false,
		},
		{
			name:     "warn level",
			logLevel: "warn",
			expected: false,
		},
		{
			name:     "error level",
			logLevel: "error",
			expected: false,
		},
		{
			name:     "default level",
			logLevel: "",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.HeartbeatConfig{
				Enabled:         true,
				IntervalSeconds: 10,
				EnableMetrics:   true,
				EnableEvents:    true,
				LogLevel:        tc.logLevel,
			}

			collector := newMockCollector()
			eventStore := NewMemoryEventStore(100)

			deps := HeartbeatDependencies{
				Config:     cfg,
				Collector:  collector,
				EventStore: eventStore,
			}

			service := NewHeartbeatService(deps)

			// Test shouldLog method
			shouldLogDebug := service.shouldLog("debug")
			if shouldLogDebug != tc.expected {
				t.Errorf("Expected shouldLog('debug') to be %t for log level '%s', got %t",
					tc.expected, tc.logLevel, shouldLogDebug)
			}
		})
	}
}

// TestHeartbeatServiceStopTimeout tests stop timeout behavior
func TestHeartbeatServiceStopTimeout(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true,
	}

	collector := newMockCollector()
	eventStore := NewMemoryEventStore(100)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: eventStore,
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}

	// Wait for service to be fully running
	time.Sleep(1500 * time.Millisecond)

	// Stop should complete within reasonable time (the service has 5-second timeout)
	start := time.Now()
	err = service.Stop()
	duration := time.Since(start)

	if err != nil {
		t.Errorf("Stop should not return error: %v", err)
	}

	// Should complete well within the 5-second timeout
	if duration > 3*time.Second {
		t.Errorf("Stop took too long: %v (expected < 3 seconds)", duration)
	}

	if service.IsRunning() {
		t.Error("Service should not be running after stop")
	}
}

// TestHeartbeatServiceEventsWithoutStore tests heartbeat without event store
func TestHeartbeatServiceEventsWithoutStore(t *testing.T) {
	cfg := config.HeartbeatConfig{
		Enabled:         true,
		IntervalSeconds: 10,
		EnableMetrics:   true,
		EnableEvents:    true, // Events enabled but no store provided
	}

	collector := newMockCollector()
	// No event store provided (nil)

	deps := HeartbeatDependencies{
		Config:     cfg,
		Collector:  collector,
		EventStore: nil, // No event store
	}

	service := NewHeartbeatService(deps)

	ctx := context.Background()
	err := service.Start(ctx)
	if err != nil {
		t.Fatalf("Failed to start heartbeat service without event store: %v", err)
	}
	defer service.Stop()

	// Should still run without errors
	time.Sleep(1500 * time.Millisecond)

	// Verify service is running
	if !service.IsRunning() {
		t.Error("Service should be running even without event store")
	}

	// Getting events should return error
	_, err = service.GetRecentEvents(10)
	if err == nil {
		t.Error("Expected error when getting events without event store")
	}

	_, err = service.GetRecentAlerts(10)
	if err == nil {
		t.Error("Expected error when getting alerts without event store")
	}
}
