package tools

import (
	"fmt"
	"strings"
)

// PatternTracker detects circular tool call patterns that waste tokens.
// It tracks recent tool calls and identifies repeating sequences like A->B->A->B.
type PatternTracker struct {
	recentCalls []string // Last N tool names
	maxHistory  int      // How many to track
}

// NewPatternTracker creates a new pattern tracker.
// maxHistory determines how many recent calls to remember (default 10 if <= 0).
func NewPatternTracker(maxHistory int) *PatternTracker {
	if maxHistory <= 0 {
		maxHistory = 10
	}
	return &PatternTracker{
		recentCalls: make([]string, 0, maxHistory),
		maxHistory:  maxHistory,
	}
}

// RecordCall adds a tool call to the history.
func (pt *PatternTracker) RecordCall(toolName string) {
	pt.recentCalls = append(pt.recentCalls, toolName)
	// Trim to max history
	if len(pt.recentCalls) > pt.maxHistory {
		pt.recentCalls = pt.recentCalls[len(pt.recentCalls)-pt.maxHistory:]
	}
}

// DetectCircular checks for repeating patterns in recent tool calls.
// Returns true if a pattern is detected, along with a description of the pattern.
// Looks for 2-3 element patterns that appear 3+ times consecutively.
func (pt *PatternTracker) DetectCircular() (bool, string) {
	calls := pt.recentCalls
	n := len(calls)

	// Need at least 6 calls for a 2-element pattern repeated 3 times
	if n < 6 {
		return false, ""
	}

	// Check for 2-element patterns (need 6 elements: A B A B A B)
	if detected, pattern := pt.detectPatternOfLength(calls, 2); detected {
		return true, pattern
	}

	// Check for 3-element patterns (need 9 elements: A B C A B C A B C)
	if n >= 9 {
		if detected, pattern := pt.detectPatternOfLength(calls, 3); detected {
			return true, pattern
		}
	}

	return false, ""
}

// detectPatternOfLength checks if the last elements form a repeating pattern
// of the given length, appearing at least 3 times consecutively.
func (pt *PatternTracker) detectPatternOfLength(calls []string, patternLen int) (bool, string) {
	n := len(calls)
	minRequired := patternLen * 3 // Need 3 repetitions

	if n < minRequired {
		return false, ""
	}

	// Extract the candidate pattern from the most recent calls
	pattern := calls[n-patternLen:]

	// Count consecutive matches going backwards
	repetitions := 1
	for i := n - patternLen*2; i >= 0; i -= patternLen {
		// Check if this segment matches the pattern
		match := true
		for j := 0; j < patternLen; j++ {
			if i+j >= n-patternLen && calls[i+j] != pattern[j] {
				match = false
				break
			}
			if i+j < n-patternLen && calls[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			repetitions++
		} else {
			break
		}
	}

	if repetitions >= 3 {
		return true, formatPattern(pattern)
	}

	return false, ""
}

// formatPattern creates a human-readable description of a pattern.
func formatPattern(pattern []string) string {
	return strings.Join(pattern, " -> ")
}

// Reset clears the call history.
func (pt *PatternTracker) Reset() {
	pt.recentCalls = pt.recentCalls[:0]
}

// InjectWarning creates a warning message for detected circular patterns.
func InjectWarning(pattern string) string {
	return fmt.Sprintf("Warning: Detected circular tool call pattern (%s). "+
		"This pattern has repeated 3+ times. Try a different approach to avoid wasting tokens.", pattern)
}
