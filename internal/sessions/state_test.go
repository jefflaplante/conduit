package sessions

import (
	"sync"
	"testing"
	"time"
)

func TestSessionStateTracker(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "test-session-123"

	// Test initial state
	state, exists := tracker.GetState(sessionKey)
	if exists {
		t.Errorf("Expected session to not exist initially")
	}

	// Test setting initial state
	err := tracker.UpdateState(sessionKey, SessionStateIdle, nil)
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	state, exists = tracker.GetState(sessionKey)
	if !exists {
		t.Fatalf("Expected session to exist after update")
	}
	if state != SessionStateIdle {
		t.Errorf("Expected state to be idle, got %s", state)
	}

	// Test state transition
	err = tracker.UpdateState(sessionKey, SessionStateProcessing, map[string]interface{}{
		"action": "test_processing",
	})
	if err != nil {
		t.Fatalf("Failed to update state to processing: %v", err)
	}

	state, exists = tracker.GetState(sessionKey)
	if !exists || state != SessionStateProcessing {
		t.Errorf("Expected state to be processing, got %s", state)
	}

	// Test metrics
	metrics := tracker.GetMetrics()
	if metrics.ProcessingSessions != 1 {
		t.Errorf("Expected 1 processing session, got %d", metrics.ProcessingSessions)
	}
	if metrics.TotalSessions != 1 {
		t.Errorf("Expected 1 total session, got %d", metrics.TotalSessions)
	}
}

func TestSessionStateInfo(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "test-session-info"

	// Update state and check detailed info
	err := tracker.UpdateState(sessionKey, SessionStateProcessing, nil)
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	info, exists := tracker.GetStateInfo(sessionKey)
	if !exists {
		t.Fatalf("Expected session info to exist")
	}

	if info.State != SessionStateProcessing {
		t.Errorf("Expected state to be processing, got %s", info.State)
	}

	if info.ProcessingStart.IsZero() {
		t.Errorf("Expected ProcessingStart to be set")
	}

	if time.Since(info.LastActivity) > time.Second {
		t.Errorf("Expected LastActivity to be recent")
	}
}

func TestStuckSessionDetection(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "stuck-session"

	// Create a session and simulate it being stuck
	err := tracker.UpdateState(sessionKey, SessionStateProcessing, nil)
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	// Artificially set an old processing start time
	tracker.statesMutex.Lock()
	info := tracker.sessionStates[sessionKey]
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

	stuckSessions := tracker.DetectStuckSessions(config)
	if len(stuckSessions) != 1 {
		t.Errorf("Expected 1 stuck session, got %d", len(stuckSessions))
	}

	if stuckSessions[0].SessionKey != sessionKey {
		t.Errorf("Expected stuck session key %s, got %s", sessionKey, stuckSessions[0].SessionKey)
	}

	if stuckSessions[0].Reason != StuckReasonProcessingTimeout {
		t.Errorf("Expected stuck reason to be processing timeout, got %s", stuckSessions[0].Reason)
	}
}

func TestStateChangeHooks(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "hook-test-session"

	var hookCalled bool
	var hookEvent StateChangeEvent
	var wg sync.WaitGroup

	// Add a state change hook
	tracker.AddStateHook(func(event StateChangeEvent) {
		defer wg.Done()
		hookCalled = true
		hookEvent = event
	})

	// Update state and wait for hook to be called
	wg.Add(1)
	err := tracker.UpdateState(sessionKey, SessionStateProcessing, map[string]interface{}{
		"test": "data",
	})
	if err != nil {
		t.Fatalf("Failed to update state: %v", err)
	}

	// Wait for hook to complete (with timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Hook completed successfully
	case <-time.After(time.Second):
		t.Fatal("Hook was not called within timeout")
	}

	if !hookCalled {
		t.Error("Expected hook to be called")
	}

	if hookEvent.SessionKey != sessionKey {
		t.Errorf("Expected hook event session key %s, got %s", sessionKey, hookEvent.SessionKey)
	}

	if hookEvent.NewState != SessionStateProcessing {
		t.Errorf("Expected hook event new state to be processing, got %s", hookEvent.NewState)
	}

	if hookEvent.Metadata["test"] != "data" {
		t.Errorf("Expected hook event metadata to contain test data")
	}
}

func TestConcurrentAccess(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "concurrent-test"

	var wg sync.WaitGroup
	numGoroutines := 10
	numUpdatesPerGoroutine := 100

	// Start multiple goroutines updating state concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < numUpdatesPerGoroutine; j++ {
				state := SessionStateIdle
				if j%2 == 0 {
					state = SessionStateProcessing
				}

				err := tracker.UpdateState(sessionKey, state, map[string]interface{}{
					"routine_id": routineID,
					"update_id":  j,
				})

				if err != nil {
					t.Errorf("Failed to update state: %v", err)
				}

				// Also test MarkActivity
				tracker.MarkActivity(sessionKey)
			}
		}(i)
	}

	wg.Wait()

	// Verify the session exists and has valid state
	state, exists := tracker.GetState(sessionKey)
	if !exists {
		t.Fatal("Expected session to exist after concurrent updates")
	}

	if !state.IsValid() {
		t.Errorf("Expected valid session state, got %s", state)
	}

	metrics := tracker.GetMetrics()
	if metrics.TotalSessions != 1 {
		t.Errorf("Expected 1 total session, got %d", metrics.TotalSessions)
	}
}

func TestQueueDepthTracking(t *testing.T) {
	tracker := NewSessionStateTracker()

	// Test initial queue depth
	metrics := tracker.GetMetrics()
	if metrics.QueueDepth != 0 {
		t.Errorf("Expected initial queue depth to be 0, got %d", metrics.QueueDepth)
	}

	// Update queue depth
	tracker.UpdateQueueDepth(5)

	metrics = tracker.GetMetrics()
	if metrics.QueueDepth != 5 {
		t.Errorf("Expected queue depth to be 5, got %d", metrics.QueueDepth)
	}

	// Test concurrent queue depth updates
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(depth int) {
			defer wg.Done()
			tracker.UpdateQueueDepth(depth)
		}(i)
	}
	wg.Wait()

	// Verify queue depth is one of the expected values
	metrics = tracker.GetMetrics()
	if metrics.QueueDepth < 0 || metrics.QueueDepth >= 10 {
		t.Errorf("Expected queue depth to be 0-9, got %d", metrics.QueueDepth)
	}
}

func TestInvalidStates(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "invalid-state-test"

	// Test invalid state
	err := tracker.UpdateState(sessionKey, "invalid-state", nil)
	if err == nil {
		t.Error("Expected error when setting invalid state")
	}

	// Verify session was not created
	_, exists := tracker.GetState(sessionKey)
	if exists {
		t.Error("Expected session to not exist after invalid state update")
	}
}

func TestSessionRemoval(t *testing.T) {
	tracker := NewSessionStateTracker()
	sessionKey := "removal-test"

	// Create session
	err := tracker.UpdateState(sessionKey, SessionStateProcessing, nil)
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	// Verify it exists
	_, exists := tracker.GetState(sessionKey)
	if !exists {
		t.Fatal("Expected session to exist")
	}

	// Remove it
	tracker.RemoveSession(sessionKey)

	// Verify it's gone
	_, exists = tracker.GetState(sessionKey)
	if exists {
		t.Error("Expected session to not exist after removal")
	}

	// Verify metrics are updated
	metrics := tracker.GetMetrics()
	if metrics.TotalSessions != 0 {
		t.Errorf("Expected 0 total sessions after removal, got %d", metrics.TotalSessions)
	}
	if metrics.ProcessingSessions != 0 {
		t.Errorf("Expected 0 processing sessions after removal, got %d", metrics.ProcessingSessions)
	}
}
