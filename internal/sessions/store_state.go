package sessions

import (
	"fmt"
	"time"

	"conduit/internal/database"
)

// Session State Tracking Methods

// GetStateTracker returns the session state tracker
func (s *Store) GetStateTracker() *SessionStateTracker {
	return s.stateTracker
}

// StartStateCleanup starts the background cleanup loop for idle session states.
// Returns a function to stop the cleanup loop. Should be called on shutdown.
func (s *Store) StartStateCleanup(idleTimeout, interval time.Duration) func() {
	return s.stateTracker.StartCleanupLoop(idleTimeout, interval)
}

// UpdateSessionState updates the state of a session with optional metadata
func (s *Store) UpdateSessionState(sessionKey string, state SessionState, metadata map[string]interface{}) error {
	if err := s.stateTracker.UpdateState(sessionKey, state, metadata); err != nil {
		return fmt.Errorf("failed to update session state: %w", err)
	}

	// Mark activity in the database record as well
	s.markSessionActivity(sessionKey)

	return nil
}

// MarkSessionActivity marks that activity occurred for a session
func (s *Store) MarkSessionActivity(sessionKey string) {
	s.stateTracker.MarkActivity(sessionKey)
	s.markSessionActivity(sessionKey)
}

// GetSessionState returns the current state of a session
func (s *Store) GetSessionState(sessionKey string) (SessionState, bool) {
	return s.stateTracker.GetState(sessionKey)
}

// GetSessionStateInfo returns detailed state information for a session
func (s *Store) GetSessionStateInfo(sessionKey string) (*SessionStateInfo, bool) {
	return s.stateTracker.GetStateInfo(sessionKey)
}

// GetSessionStateMetrics returns current session state metrics
func (s *Store) GetSessionStateMetrics() SessionStateMetrics {
	return s.stateTracker.GetMetrics()
}

// UpdateQueueDepth updates the queue depth metric
func (s *Store) UpdateQueueDepth(depth int) {
	s.stateTracker.UpdateQueueDepth(depth)
}

// DetectStuckSessions finds sessions that have been stuck for too long
func (s *Store) DetectStuckSessions(config StuckSessionConfig) []StuckSessionInfo {
	return s.stateTracker.DetectStuckSessions(config)
}

// AddStateChangeHook adds a hook that will be called on state changes
func (s *Store) AddStateChangeHook(hook StateChangeHook) {
	s.stateTracker.AddStateHook(hook)
}

// markSessionActivity is an internal helper to update the database last activity
func (s *Store) markSessionActivity(sessionKey string) {
	// Update the database record's updated_at timestamp
	// Best-effort — retry on BUSY so heartbeat-paced writers don't silently drop updates.
	_ = database.RetryOnBusy(5, func() error {
		_, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE key = ?`, nowUpdatedAt(), sessionKey)
		return err
	})
}
