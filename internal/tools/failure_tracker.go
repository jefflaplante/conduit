package tools

import "sync"

// FailureTracker tracks consecutive failures for tools and suggests approach pivots
// when a tool repeatedly fails.
type FailureTracker struct {
	failures   map[string]int    // tool name -> consecutive failure count
	lastErrors map[string]string // tool name -> most recent error message
	threshold  int               // failures before suggesting pivot (default 3)
	mu         sync.Mutex

	// OnPivot is called when a tool reaches the failure threshold (ShouldPivot becomes true).
	// It fires once per threshold crossing — the call that causes failCount to equal threshold.
	// The callback receives the tool name, current failure count, and the last error message.
	// Safe to leave nil; RecordFailure simply won't invoke it.
	OnPivot func(toolName string, failCount int, lastError string)
}

// NewFailureTracker creates a new FailureTracker with the specified threshold.
// If threshold is <= 0, it defaults to 3.
func NewFailureTracker(threshold int) *FailureTracker {
	if threshold <= 0 {
		threshold = 3
	}
	return &FailureTracker{
		failures:   make(map[string]int),
		lastErrors: make(map[string]string),
		threshold:  threshold,
	}
}

// RecordFailure increments the failure count for the given tool and returns the current count.
// The errMsg is stored as the last error for the tool and passed to the OnPivot callback
// if the failure threshold is reached on this call.
func (ft *FailureTracker) RecordFailure(toolName string, errMsg string) int {
	ft.mu.Lock()
	ft.failures[toolName]++
	ft.lastErrors[toolName] = errMsg
	count := ft.failures[toolName]
	shouldFire := count == ft.threshold && ft.OnPivot != nil
	ft.mu.Unlock()

	if shouldFire {
		ft.OnPivot(toolName, count, errMsg)
	}

	return count
}

// RecordSuccess resets the failure count and last error for the given tool.
func (ft *FailureTracker) RecordSuccess(toolName string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	delete(ft.failures, toolName)
	delete(ft.lastErrors, toolName)
}

// ShouldPivot returns true if the tool has reached or exceeded the failure threshold.
func (ft *FailureTracker) ShouldPivot(toolName string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.failures[toolName] >= ft.threshold
}

// Reset clears all failure counts and last errors.
func (ft *FailureTracker) Reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.failures = make(map[string]int)
	ft.lastErrors = make(map[string]string)
}

// GetFailedTools returns a list of tools that have hit the failure threshold.
// This is useful for injecting pivot messages for multiple tools at once.
func (ft *FailureTracker) GetFailedTools() []string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var failed []string
	for tool, count := range ft.failures {
		if count >= ft.threshold {
			failed = append(failed, tool)
		}
	}
	return failed
}
