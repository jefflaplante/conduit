package monitoring

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"conduit/internal/sessions"
)

// mockSessionStore implements SessionStoreInterface for testing
type mockSessionStore struct {
	sessions []sessions.Session
	db       *sql.DB
}

func newMockSessionStore() *mockSessionStore {
	// Create an in-memory SQLite database for testing
	db, _ := sql.Open("sqlite", ":memory:")
	if db != nil {
		db.Exec("CREATE TABLE IF NOT EXISTS sessions (id INTEGER PRIMARY KEY)")
	}
	return &mockSessionStore{db: db}
}

func (m *mockSessionStore) ListActiveSessions(limit int) ([]sessions.Session, error) {
	if limit > 0 && len(m.sessions) > limit {
		return m.sessions[:limit], nil
	}
	return m.sessions, nil
}

func (m *mockSessionStore) DB() *sql.DB {
	return m.db
}

func (m *mockSessionStore) GetSessionStateMetrics() sessions.SessionStateMetrics {
	// Count sessions by simulated state based on UpdatedAt
	var processing, waiting, idle int64
	for _, sess := range m.sessions {
		age := time.Since(sess.UpdatedAt)
		if age < 2*time.Minute {
			processing++
		} else if age < 5*time.Minute {
			waiting++
		} else {
			idle++
		}
	}
	return sessions.SessionStateMetrics{
		TotalSessions:      int64(len(m.sessions)),
		ProcessingSessions: processing,
		WaitingSessions:    waiting,
		IdleSessions:       idle,
	}
}

func (m *mockSessionStore) DetectStuckSessions(config sessions.StuckSessionConfig) []sessions.StuckSessionInfo {
	var stuck []sessions.StuckSessionInfo
	for _, sess := range m.sessions {
		age := time.Since(sess.UpdatedAt)
		// Sessions older than threshold but younger than 1 hour are considered stuck
		if age > config.ProcessingTimeout && age < 1*time.Hour {
			stuck = append(stuck, sessions.StuckSessionInfo{
				SessionKey:    sess.Key,
				Reason:        sessions.StuckReasonProcessingTimeout,
				StuckDuration: age,
			})
		}
	}
	return stuck
}

func TestNewMetricsCollector(t *testing.T) {
	gatewayMetrics := NewGatewayMetrics()

	deps := CollectorDependencies{
		SessionStore:   newMockSessionStore(),
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	if collector == nil {
		t.Fatal("Expected non-nil collector")
	}

	if collector.sessionStore == nil {
		t.Error("Expected session store to be set")
	}

	if collector.gatewayMetrics == nil {
		t.Error("Expected gateway metrics to be set")
	}
}

func TestMetricsCollectorActivity(t *testing.T) {
	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   newMockSessionStore(),
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	// Test initial state
	if !collector.IsIdle(0) {
		t.Error("Expected collector to be idle initially")
	}

	// Mark activity
	before := collector.GetLastActivityTime()
	time.Sleep(1 * time.Millisecond) // Ensure time difference
	collector.MarkActivity()
	after := collector.GetLastActivityTime()

	if !after.After(before) {
		t.Error("Expected activity time to update after MarkActivity")
	}

	// Test idle detection
	if collector.IsIdle(10 * time.Minute) {
		t.Error("Expected collector to not be idle after recent activity")
	}
}

func TestMetricsCollectorUpdates(t *testing.T) {
	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   newMockSessionStore(),
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	// Test WebSocket connection updates
	collector.UpdateWebSocketConnections(5)
	if collector.wsConnections != 5 {
		t.Errorf("Expected 5 WebSocket connections, got %d", collector.wsConnections)
	}

	// Test active request updates
	collector.UpdateActiveRequests(3)
	if collector.activeRequests != 3 {
		t.Errorf("Expected 3 active requests, got %d", collector.activeRequests)
	}

	// Test queue depth updates
	collector.UpdateQueueDepth(10)
	if collector.queueDepth != 10 {
		t.Errorf("Expected queue depth of 10, got %d", collector.queueDepth)
	}
}

func TestCollectMetrics(t *testing.T) {
	// Create mock sessions with different activity levels
	now := time.Now()
	mockStore := newMockSessionStore()
	mockStore.sessions = []sessions.Session{
		{
			Key:       "session1",
			UserID:    "user1",
			UpdatedAt: now.Add(-30 * time.Second), // Recent activity
		},
		{
			Key:       "session2",
			UserID:    "user2",
			UpdatedAt: now.Add(-3 * time.Minute), // Waiting
		},
		{
			Key:       "session3",
			UserID:    "user3",
			UpdatedAt: now.Add(-10 * time.Minute), // Idle
		},
	}

	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   mockStore,
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)
	collector.UpdateWebSocketConnections(2)
	collector.UpdateActiveRequests(1)
	collector.UpdateQueueDepth(5)

	ctx := context.Background()
	_, err := collector.CollectMetrics(ctx)
	if err != nil {
		t.Fatalf("Failed to collect metrics: %v", err)
	}

	// Since we update the shared gatewayMetrics object, the returned metrics might be empty
	// Check the shared object instead
	snapshot := gatewayMetrics.Snapshot()

	if snapshot.WebhookConnections != 2 {
		t.Errorf("Expected 2 WebSocket connections, got %d", snapshot.WebhookConnections)
	}

	if snapshot.QueueDepth != 5 {
		t.Errorf("Expected queue depth of 5, got %d", snapshot.QueueDepth)
	}

	if snapshot.TotalSessions != 3 {
		t.Errorf("Expected 3 total sessions, got %d", snapshot.TotalSessions)
	}

	// Test that system metrics are updated
	if snapshot.GoroutineCount == 0 {
		t.Error("Expected goroutine count to be greater than 0")
	}

	if snapshot.MemoryUsageMB == 0 {
		t.Error("Expected memory usage to be greater than 0")
	}
}

func TestSessionMetricsClassification(t *testing.T) {
	now := time.Now()
	mockStore := newMockSessionStore()
	mockStore.sessions = []sessions.Session{
		{Key: "active1", UpdatedAt: now.Add(-30 * time.Second)}, // Active
		{Key: "active2", UpdatedAt: now.Add(-90 * time.Second)}, // Active but processing
		{Key: "waiting1", UpdatedAt: now.Add(-3 * time.Minute)}, // Waiting
		{Key: "idle1", UpdatedAt: now.Add(-10 * time.Minute)},   // Idle
	}

	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   mockStore,
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	ctx := context.Background()
	sessionMetrics, err := collector.collectSessionMetrics(ctx)
	if err != nil {
		t.Fatalf("Failed to collect session metrics: %v", err)
	}

	if sessionMetrics.TotalSessions != 4 {
		t.Errorf("Expected 4 total sessions, got %d", sessionMetrics.TotalSessions)
	}

	if sessionMetrics.ActiveSessions != 3 {
		t.Errorf("Expected 3 active sessions (processing + waiting), got %d", sessionMetrics.ActiveSessions)
	}

	if sessionMetrics.WaitingSessions != 1 {
		t.Errorf("Expected 1 waiting session, got %d", sessionMetrics.WaitingSessions)
	}

	if sessionMetrics.IdleSessions != 1 {
		t.Errorf("Expected 1 idle session, got %d", sessionMetrics.IdleSessions)
	}
}

func TestDetectStuckSessions(t *testing.T) {
	now := time.Now()
	mockStore := newMockSessionStore()
	mockStore.sessions = []sessions.Session{
		{Key: "normal", UpdatedAt: now.Add(-30 * time.Second)}, // Normal
		{Key: "stuck1", UpdatedAt: now.Add(-3 * time.Minute)},  // Potentially stuck
		{Key: "stuck2", UpdatedAt: now.Add(-4 * time.Minute)},  // Potentially stuck
		{Key: "old", UpdatedAt: now.Add(-2 * time.Hour)},       // Too old to be stuck
	}

	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   mockStore,
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	ctx := context.Background()
	stuckSessions, err := collector.DetectStuckSessions(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("Failed to detect stuck sessions: %v", err)
	}

	expectedStuck := 2
	if len(stuckSessions) != expectedStuck {
		t.Errorf("Expected %d stuck sessions, got %d", expectedStuck, len(stuckSessions))
	}

	// Check that the right sessions are identified as stuck
	stuckMap := make(map[string]bool)
	for _, key := range stuckSessions {
		stuckMap[key] = true
	}

	if !stuckMap["stuck1"] || !stuckMap["stuck2"] {
		t.Error("Expected stuck1 and stuck2 to be identified as stuck")
	}

	if stuckMap["normal"] || stuckMap["old"] {
		t.Error("Expected normal and old sessions to not be identified as stuck")
	}
}

func TestGetDetailedStats(t *testing.T) {
	mockStore := newMockSessionStore()
	mockStore.sessions = []sessions.Session{
		{Key: "session1", UpdatedAt: time.Now().Add(-30 * time.Second)},
	}

	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   mockStore,
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	ctx := context.Background()
	stats, err := collector.GetDetailedStats(ctx)
	if err != nil {
		t.Fatalf("Failed to get detailed stats: %v", err)
	}

	// Check that all expected sections are present
	if _, exists := stats["timestamp"]; !exists {
		t.Error("Expected timestamp in detailed stats")
	}

	if _, exists := stats["sessions"]; !exists {
		t.Error("Expected sessions section in detailed stats")
	}

	if _, exists := stats["system"]; !exists {
		t.Error("Expected system section in detailed stats")
	}

	if _, exists := stats["network"]; !exists {
		t.Error("Expected network section in detailed stats")
	}

	// Check sessions section structure
	if sessionsData, ok := stats["sessions"].(map[string]int); ok {
		if _, exists := sessionsData["total"]; !exists {
			t.Error("Expected total sessions count")
		}
	} else {
		t.Error("Expected sessions to be a map[string]int")
	}
}

func TestCollectorConcurrency(t *testing.T) {
	gatewayMetrics := NewGatewayMetrics()
	deps := CollectorDependencies{
		SessionStore:   newMockSessionStore(),
		GatewayMetrics: gatewayMetrics,
	}

	collector := NewMetricsCollector(deps)

	// Test concurrent updates
	done := make(chan bool)

	// Goroutine 1: Update WebSocket connections
	go func() {
		for i := 0; i < 100; i++ {
			collector.UpdateWebSocketConnections(i)
			time.Sleep(1 * time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 2: Update active requests
	go func() {
		for i := 0; i < 100; i++ {
			collector.UpdateActiveRequests(i)
			time.Sleep(1 * time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 3: Mark activity
	go func() {
		for i := 0; i < 100; i++ {
			collector.MarkActivity()
			time.Sleep(1 * time.Millisecond)
		}
		done <- true
	}()

	// Wait for all goroutines to complete
	for i := 0; i < 3; i++ {
		<-done
	}

	// Verify that the collector is still functional
	collector.MarkActivity()
	if collector.IsIdle(10 * time.Minute) {
		t.Error("Expected collector to not be idle after concurrent operations")
	}
}
