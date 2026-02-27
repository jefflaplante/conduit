package sessions

import (
	"sync"
	"sync/atomic"
	"time"
)

// SessionState represents the current state of a session
type SessionState string

const (
	SessionStateIdle       SessionState = "idle"       // Session exists but not actively processing
	SessionStateProcessing SessionState = "processing" // Currently processing a request
	SessionStateWaiting    SessionState = "waiting"    // Waiting for external resources/response
	SessionStateError      SessionState = "error"      // Session encountered an error
)

// String returns the string representation of the session state
func (s SessionState) String() string {
	return string(s)
}

// IsValid returns true if the session state is a valid state
func (s SessionState) IsValid() bool {
	switch s {
	case SessionStateIdle, SessionStateProcessing, SessionStateWaiting, SessionStateError:
		return true
	default:
		return false
	}
}

// SessionStateTracker manages state and metrics for all sessions
type SessionStateTracker struct {
	// Session state storage
	sessionStates map[string]*SessionStateInfo
	statesMutex   sync.RWMutex

	// Atomic counters for high-frequency metrics
	idleCount       int64
	processingCount int64
	waitingCount    int64
	errorCount      int64
	queueDepth      int64

	// State change hooks
	stateHooks []StateChangeHook
	hooksMutex sync.RWMutex
}

// SessionStateInfo contains detailed state information for a session
type SessionStateInfo struct {
	SessionKey      string       `json:"session_key"`
	State           SessionState `json:"state"`
	LastActivity    time.Time    `json:"last_activity"`
	StateChanged    time.Time    `json:"state_changed"`
	ProcessingStart time.Time    `json:"processing_start,omitempty"`
	ErrorCount      int          `json:"error_count"`
	mutex           sync.RWMutex `json:"-"`
}

// StateChangeEvent represents a session state change
type StateChangeEvent struct {
	SessionKey string                 `json:"session_key"`
	OldState   SessionState           `json:"old_state"`
	NewState   SessionState           `json:"new_state"`
	Timestamp  time.Time              `json:"timestamp"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// StateChangeHook is called when a session state changes
type StateChangeHook func(event StateChangeEvent)

// StuckSessionConfig defines thresholds for detecting stuck sessions
type StuckSessionConfig struct {
	ProcessingTimeout time.Duration // How long before processing is considered stuck
	WaitingTimeout    time.Duration // How long before waiting is considered stuck
	ErrorRetryLimit   int           // Max errors before marking session as problematic
}

// DefaultStuckSessionConfig returns default configuration for stuck session detection
func DefaultStuckSessionConfig() StuckSessionConfig {
	return StuckSessionConfig{
		ProcessingTimeout: 2 * time.Minute,
		WaitingTimeout:    5 * time.Minute,
		ErrorRetryLimit:   3,
	}
}

// NewSessionStateTracker creates a new session state tracker
func NewSessionStateTracker() *SessionStateTracker {
	return &SessionStateTracker{
		sessionStates: make(map[string]*SessionStateInfo),
		stateHooks:    make([]StateChangeHook, 0),
	}
}

// UpdateState updates the state of a session and triggers hooks
func (t *SessionStateTracker) UpdateState(sessionKey string, newState SessionState, metadata map[string]interface{}) error {
	if !newState.IsValid() {
		return ErrInvalidState
	}

	t.statesMutex.Lock()
	defer t.statesMutex.Unlock()

	isNewSession := false
	info, exists := t.sessionStates[sessionKey]
	if !exists {
		isNewSession = true
		info = &SessionStateInfo{
			SessionKey:   sessionKey,
			State:        SessionStateIdle,
			LastActivity: time.Now(),
			StateChanged: time.Now(),
		}
		t.sessionStates[sessionKey] = info
	}

	info.mutex.Lock()
	defer info.mutex.Unlock()

	oldState := info.State
	// For new sessions, treat creation as a state change so hooks fire
	if isNewSession {
		oldState = ""
	}
	now := time.Now()

	// Update state information
	info.State = newState
	info.LastActivity = now

	// Handle state-specific logic
	if oldState != newState {
		info.StateChanged = now

		// Track processing start time
		if newState == SessionStateProcessing {
			info.ProcessingStart = now
		} else if oldState == SessionStateProcessing {
			info.ProcessingStart = time.Time{} // Clear processing start
		}

		// Update error count
		if newState == SessionStateError {
			info.ErrorCount++
		}

		// Update atomic counters
		t.updateCounters(oldState, newState)

		// Create state change event
		event := StateChangeEvent{
			SessionKey: sessionKey,
			OldState:   oldState,
			NewState:   newState,
			Timestamp:  now,
			Metadata:   metadata,
		}

		// Trigger hooks (without holding the main lock)
		go t.triggerStateHooks(event)
	} else {
		// Same state, just update activity time
		info.LastActivity = now
	}

	return nil
}

// MarkActivity updates the last activity time for a session without changing state
func (t *SessionStateTracker) MarkActivity(sessionKey string) {
	t.statesMutex.Lock()
	defer t.statesMutex.Unlock()

	info, exists := t.sessionStates[sessionKey]
	if !exists {
		// Create new session in idle state
		info = &SessionStateInfo{
			SessionKey:   sessionKey,
			State:        SessionStateIdle,
			LastActivity: time.Now(),
			StateChanged: time.Now(),
		}
		t.sessionStates[sessionKey] = info
		atomic.AddInt64(&t.idleCount, 1)
		return
	}

	info.mutex.Lock()
	defer info.mutex.Unlock()

	info.LastActivity = time.Now()
}

// GetState returns the current state of a session
func (t *SessionStateTracker) GetState(sessionKey string) (SessionState, bool) {
	t.statesMutex.RLock()
	defer t.statesMutex.RUnlock()

	info, exists := t.sessionStates[sessionKey]
	if !exists {
		return "", false
	}

	info.mutex.RLock()
	defer info.mutex.RUnlock()

	return info.State, true
}

// GetStateInfo returns detailed state information for a session
func (t *SessionStateTracker) GetStateInfo(sessionKey string) (*SessionStateInfo, bool) {
	t.statesMutex.RLock()
	defer t.statesMutex.RUnlock()

	info, exists := t.sessionStates[sessionKey]
	if !exists {
		return nil, false
	}

	info.mutex.RLock()
	defer info.mutex.RUnlock()

	// Return a copy to avoid race conditions
	return &SessionStateInfo{
		SessionKey:      info.SessionKey,
		State:           info.State,
		LastActivity:    info.LastActivity,
		StateChanged:    info.StateChanged,
		ProcessingStart: info.ProcessingStart,
		ErrorCount:      info.ErrorCount,
	}, true
}

// RemoveSession removes a session from tracking
func (t *SessionStateTracker) RemoveSession(sessionKey string) {
	t.statesMutex.Lock()
	defer t.statesMutex.Unlock()

	info, exists := t.sessionStates[sessionKey]
	if !exists {
		return
	}

	info.mutex.RLock()
	oldState := info.State
	info.mutex.RUnlock()

	// Update counters
	t.updateCounters(oldState, "")

	delete(t.sessionStates, sessionKey)
}

// GetMetrics returns current session state metrics
func (t *SessionStateTracker) GetMetrics() SessionStateMetrics {
	return SessionStateMetrics{
		IdleSessions:       atomic.LoadInt64(&t.idleCount),
		ProcessingSessions: atomic.LoadInt64(&t.processingCount),
		WaitingSessions:    atomic.LoadInt64(&t.waitingCount),
		ErrorSessions:      atomic.LoadInt64(&t.errorCount),
		QueueDepth:         atomic.LoadInt64(&t.queueDepth),
		TotalSessions:      t.getTotalSessionCount(),
	}
}

// UpdateQueueDepth updates the current queue depth
func (t *SessionStateTracker) UpdateQueueDepth(depth int) {
	atomic.StoreInt64(&t.queueDepth, int64(depth))
}

// DetectStuckSessions finds sessions that have been in processing state too long
func (t *SessionStateTracker) DetectStuckSessions(config StuckSessionConfig) []StuckSessionInfo {
	t.statesMutex.RLock()
	defer t.statesMutex.RUnlock()

	var stuckSessions []StuckSessionInfo
	now := time.Now()

	for sessionKey, info := range t.sessionStates {
		info.mutex.RLock()

		var reason StuckReason
		var stuckDuration time.Duration

		switch info.State {
		case SessionStateProcessing:
			if !info.ProcessingStart.IsZero() {
				stuckDuration = now.Sub(info.ProcessingStart)
				if stuckDuration > config.ProcessingTimeout {
					reason = StuckReasonProcessingTimeout
				}
			}
		case SessionStateWaiting:
			stuckDuration = now.Sub(info.StateChanged)
			if stuckDuration > config.WaitingTimeout {
				reason = StuckReasonWaitingTimeout
			}
		case SessionStateError:
			if info.ErrorCount >= config.ErrorRetryLimit {
				reason = StuckReasonTooManyErrors
				stuckDuration = now.Sub(info.StateChanged)
			}
		}

		if reason != "" {
			stuckSessions = append(stuckSessions, StuckSessionInfo{
				SessionKey:    sessionKey,
				State:         info.State,
				Reason:        reason,
				StuckDuration: stuckDuration,
				LastActivity:  info.LastActivity,
				ErrorCount:    info.ErrorCount,
			})
		}

		info.mutex.RUnlock()
	}

	return stuckSessions
}

// AddStateHook adds a hook that will be called on state changes
func (t *SessionStateTracker) AddStateHook(hook StateChangeHook) {
	t.hooksMutex.Lock()
	defer t.hooksMutex.Unlock()

	t.stateHooks = append(t.stateHooks, hook)
}

// triggerStateHooks calls all registered state change hooks
func (t *SessionStateTracker) triggerStateHooks(event StateChangeEvent) {
	t.hooksMutex.RLock()
	hooks := make([]StateChangeHook, len(t.stateHooks))
	copy(hooks, t.stateHooks)
	t.hooksMutex.RUnlock()

	for _, hook := range hooks {
		// Call hooks in separate goroutines to avoid blocking
		go func(h StateChangeHook) {
			defer func() {
				if r := recover(); r != nil {
					// Log hook panic but don't crash the system
					// TODO: Add proper logging when logger is available
				}
			}()
			h(event)
		}(hook)
	}
}

// updateCounters updates the atomic counters based on state transitions
func (t *SessionStateTracker) updateCounters(oldState, newState SessionState) {
	// Decrement old state counter
	switch oldState {
	case SessionStateIdle:
		atomic.AddInt64(&t.idleCount, -1)
	case SessionStateProcessing:
		atomic.AddInt64(&t.processingCount, -1)
	case SessionStateWaiting:
		atomic.AddInt64(&t.waitingCount, -1)
	case SessionStateError:
		atomic.AddInt64(&t.errorCount, -1)
	}

	// Increment new state counter (empty string means session removal)
	switch newState {
	case SessionStateIdle:
		atomic.AddInt64(&t.idleCount, 1)
	case SessionStateProcessing:
		atomic.AddInt64(&t.processingCount, 1)
	case SessionStateWaiting:
		atomic.AddInt64(&t.waitingCount, 1)
	case SessionStateError:
		atomic.AddInt64(&t.errorCount, 1)
	}
}

// getTotalSessionCount returns the total number of tracked sessions
func (t *SessionStateTracker) getTotalSessionCount() int64 {
	t.statesMutex.RLock()
	defer t.statesMutex.RUnlock()

	return int64(len(t.sessionStates))
}

// SessionStateMetrics contains metrics about session states
type SessionStateMetrics struct {
	IdleSessions       int64 `json:"idle_sessions"`
	ProcessingSessions int64 `json:"processing_sessions"`
	WaitingSessions    int64 `json:"waiting_sessions"`
	ErrorSessions      int64 `json:"error_sessions"`
	QueueDepth         int64 `json:"queue_depth"`
	TotalSessions      int64 `json:"total_sessions"`
}

// StuckReason represents why a session is considered stuck
type StuckReason string

const (
	StuckReasonProcessingTimeout StuckReason = "processing_timeout"
	StuckReasonWaitingTimeout    StuckReason = "waiting_timeout"
	StuckReasonTooManyErrors     StuckReason = "too_many_errors"
)

// StuckSessionInfo contains information about a stuck session
type StuckSessionInfo struct {
	SessionKey    string        `json:"session_key"`
	State         SessionState  `json:"state"`
	Reason        StuckReason   `json:"reason"`
	StuckDuration time.Duration `json:"stuck_duration"`
	LastActivity  time.Time     `json:"last_activity"`
	ErrorCount    int           `json:"error_count"`
}

// Errors
var (
	ErrInvalidState = &StateError{Message: "invalid session state"}
)

// StateError represents a session state error
type StateError struct {
	Message string
}

func (e *StateError) Error() string {
	return e.Message
}
