package tools

import "sync"

// FailureTracker tracks consecutive failures for tools and suggests approach pivots
// when a tool repeatedly fails.
type FailureTracker struct {
	failures  map[string]int // tool name -> consecutive failure count
	threshold int            // failures before suggesting pivot (default 3)
	mu        sync.Mutex
}

// NewFailureTracker creates a new FailureTracker with the specified threshold.
// If threshold is <= 0, it defaults to 3.
func NewFailureTracker(threshold int) *FailureTracker {
	if threshold <= 0 {
		threshold = 3
	}
	return &FailureTracker{
		failures:  make(map[string]int),
		threshold: threshold,
	}
}

// RecordFailure increments the failure count for the given tool and returns the current count.
func (ft *FailureTracker) RecordFailure(toolName string) int {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.failures[toolName]++
	return ft.failures[toolName]
}

// RecordSuccess resets the failure count for the given tool.
func (ft *FailureTracker) RecordSuccess(toolName string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	delete(ft.failures, toolName)
}

// ShouldPivot returns true if the tool has reached or exceeded the failure threshold.
func (ft *FailureTracker) ShouldPivot(toolName string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return ft.failures[toolName] >= ft.threshold
}

// Reset clears all failure counts.
func (ft *FailureTracker) Reset() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.failures = make(map[string]int)
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
