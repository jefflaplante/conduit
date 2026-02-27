package sessions

import (
	"testing"
	"time"
)

func TestSessionStoreStateIntegration(t *testing.T) {
	// Use in-memory database for testing
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Test session creation with state tracking
	session, err := store.GetOrCreateSession("user123", "channel456")
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Verify state is tracked
	state, exists := store.GetSessionState(session.Key)
	if !exists {
		t.Fatal("Expected session state to be tracked")
	}
	if state != SessionStateIdle {
		t.Errorf("Expected initial state to be idle, got %s", state)
	}

	// Test state updates
	err = store.UpdateSessionState(session.Key, SessionStateProcessing, map[string]interface{}{
		"action": "test_processing",
	})
	if err != nil {
		t.Fatalf("Failed to update session state: %v", err)
	}

	state, exists = store.GetSessionState(session.Key)
	if !exists || state != SessionStateProcessing {
		t.Errorf("Expected state to be processing, got %s", state)
	}

	// Test activity marking
	store.MarkSessionActivity(session.Key)

	stateInfo, exists := store.GetSessionStateInfo(session.Key)
	if !exists {
		t.Fatal("Expected session state info to exist")
	}
	if time.Since(stateInfo.LastActivity) > time.Second {
		t.Error("Expected recent activity timestamp")
	}

	// Test message addition marks activity
	oldActivity := stateInfo.LastActivity
	time.Sleep(10 * time.Millisecond) // Ensure timestamp difference

	_, err = store.AddMessage(session.Key, "user", "Test message", nil)
	if err != nil {
		t.Fatalf("Failed to add message: %v", err)
	}

	newStateInfo, _ := store.GetSessionStateInfo(session.Key)
	if !newStateInfo.LastActivity.After(oldActivity) {
		t.Error("Expected activity timestamp to be updated after adding message")
	}
}

func TestSessionStateMetricsIntegration(t *testing.T) {
	// Use in-memory database for testing
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create multiple sessions in different states
	session1, _ := store.GetOrCreateSession("user1", "channel1")
	session2, _ := store.GetOrCreateSession("user2", "channel2")
	session3, _ := store.GetOrCreateSession("user3", "channel3")

	// Set different states
	store.UpdateSessionState(session1.Key, SessionStateProcessing, nil)
	store.UpdateSessionState(session2.Key, SessionStateWaiting, nil)
	// session3 remains idle (use _ to silence linter)
	_ = session3

	// Test metrics
	metrics := store.GetSessionStateMetrics()

	if metrics.ProcessingSessions != 1 {
		t.Errorf("Expected 1 processing session, got %d", metrics.ProcessingSessions)
	}
	if metrics.WaitingSessions != 1 {
		t.Errorf("Expected 1 waiting session, got %d", metrics.WaitingSessions)
	}
	if metrics.IdleSessions != 1 {
		t.Errorf("Expected 1 idle session, got %d", metrics.IdleSessions)
	}
	if metrics.TotalSessions != 3 {
		t.Errorf("Expected 3 total sessions, got %d", metrics.TotalSessions)
	}

	// Test queue depth tracking
	store.UpdateQueueDepth(10)
	metrics = store.GetSessionStateMetrics()
	if metrics.QueueDepth != 10 {
		t.Errorf("Expected queue depth 10, got %d", metrics.QueueDepth)
	}
}

func TestStuckSessionDetectionIntegration(t *testing.T) {
	// Use in-memory database for testing
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Create a session and make it stuck
	session, _ := store.GetOrCreateSession("user1", "channel1")
	store.UpdateSessionState(session.Key, SessionStateProcessing, nil)

	// Artificially make it stuck by manipulating the state tracker
	tracker := store.GetStateTracker()
	tracker.statesMutex.Lock()
	info := tracker.sessionStates[session.Key]
	info.mutex.Lock()
	info.ProcessingStart = time.Now().Add(-5 * time.Minute)
	info.mutex.Unlock()
	tracker.statesMutex.Unlock()

	// Test stuck session detection
	config := StuckSessionConfig{
		ProcessingTimeout: 2 * time.Minute,
		WaitingTimeout:    5 * time.Minute,
		ErrorRetryLimit:   3,
	}

	stuckSessions := store.DetectStuckSessions(config)
	if len(stuckSessions) != 1 {
		t.Errorf("Expected 1 stuck session, got %d", len(stuckSessions))
	}

	if stuckSessions[0].SessionKey != session.Key {
		t.Errorf("Expected stuck session %s, got %s", session.Key, stuckSessions[0].SessionKey)
	}
}

func TestStateChangeHooksIntegration(t *testing.T) {
	// Use in-memory database for testing
	store, err := NewStore(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Add a state change hook
	var hookCalled bool
	var hookEvent StateChangeEvent
	hookComplete := make(chan bool)

	store.AddStateChangeHook(func(event StateChangeEvent) {
		hookCalled = true
		hookEvent = event
		hookComplete <- true
	})

	// Create a session (triggers hook)
	session, _ := store.GetOrCreateSession("user1", "channel1")

	// Wait for hook to be called
	select {
	case <-hookComplete:
		// Hook was called
	case <-time.After(time.Second):
		t.Fatal("Hook was not called within timeout")
	}

	if !hookCalled {
		t.Error("Expected hook to be called")
	}

	if hookEvent.SessionKey != session.Key {
		t.Errorf("Expected hook event for session %s, got %s", session.Key, hookEvent.SessionKey)
	}

	// Test another state change
	hookCalled = false
	store.UpdateSessionState(session.Key, SessionStateProcessing, map[string]interface{}{
		"test": "metadata",
	})

	// Wait for hook to be called again
	select {
	case <-hookComplete:
		// Hook was called
	case <-time.After(time.Second):
		t.Fatal("Hook was not called for state update")
	}

	if !hookCalled {
		t.Error("Expected hook to be called for state update")
	}

	if hookEvent.NewState != SessionStateProcessing {
		t.Errorf("Expected hook event new state to be processing, got %s", hookEvent.NewState)
	}

	if hookEvent.Metadata["test"] != "metadata" {
		t.Error("Expected hook event to contain metadata")
	}
}
