package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// toolCallEntry stores both the signature (for comparison) and tool name (for display).
type toolCallEntry struct {
	signature string // "toolName:argsHash" for comparison
	toolName  string // Just the tool name for display
}

// PatternTracker detects circular tool call patterns that waste tokens.
// It tracks recent tool calls and identifies repeating sequences like A->B->A->B.
// Uses argument hashing to distinguish between same tool with different args.
type PatternTracker struct {
	recentCalls []toolCallEntry // Last N tool calls with signatures
	maxHistory  int             // How many to track

	// OnCircular is an optional callback invoked when DetectCircular finds a
	// repeating pattern. The first argument is the human-readable pattern
	// description (e.g. "Read -> Edit") and the second is a stable signature
	// hash derived from the repeating tool call signatures. The callback is
	// intended for external side-effects like writing to a reflection store or
	// Brain — the PatternTracker itself remains side-effect-free.
	OnCircular func(pattern string, signatureHash string)
}

// NewPatternTracker creates a new pattern tracker.
// maxHistory determines how many recent calls to remember (default 10 if <= 0).
func NewPatternTracker(maxHistory int) *PatternTracker {
	if maxHistory <= 0 {
		maxHistory = 10
	}
	return &PatternTracker{
		recentCalls: make([]toolCallEntry, 0, maxHistory),
		maxHistory:  maxHistory,
	}
}

// RecordCall adds a tool call to the history.
// Args are hashed to distinguish same tool with different arguments.
func (pt *PatternTracker) RecordCall(toolName string, args map[string]interface{}) {
	sig := pt.computeSignature(toolName, args)
	entry := toolCallEntry{signature: sig, toolName: toolName}
	pt.recentCalls = append(pt.recentCalls, entry)
	// Trim to max history
	if len(pt.recentCalls) > pt.maxHistory {
		pt.recentCalls = pt.recentCalls[len(pt.recentCalls)-pt.maxHistory:]
	}
}

// computeSignature creates a unique signature for a tool call.
// Returns "toolName" if no args, or "toolName:hash" with args.
func (pt *PatternTracker) computeSignature(toolName string, args map[string]interface{}) string {
	if len(args) == 0 {
		return toolName
	}
	// JSON serialize for consistent hashing (Go maps iterate in random order,
	// but json.Marshal sorts keys alphabetically)
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return toolName
	}
	h := sha256.Sum256(argsJSON)
	// Use first 8 bytes of hash (16 hex chars) - enough for uniqueness
	return fmt.Sprintf("%s:%x", toolName, h[:8])
}

// DetectCircular checks for repeating patterns in recent tool calls.
// Returns true if a pattern is detected, along with a description of the pattern.
// Looks for 2-3 element patterns that appear 3+ times consecutively.
// Uses argument hashing, so Bash("ls") repeated differs from Bash("make").
// When a pattern is detected and OnCircular is non-nil, the callback is
// invoked with the pattern description and a stable signature hash.
func (pt *PatternTracker) DetectCircular() (bool, string) {
	n := len(pt.recentCalls)

	// Need at least 6 calls for a 2-element pattern repeated 3 times
	if n < 6 {
		return false, ""
	}

	// Check for 2-element patterns (need 6 elements: A B A B A B)
	if detected, pattern, sigHash := pt.detectPatternOfLength(2); detected {
		pt.notifyCircular(pattern, sigHash)
		return true, pattern
	}

	// Check for 3-element patterns (need 9 elements: A B C A B C A B C)
	if n >= 9 {
		if detected, pattern, sigHash := pt.detectPatternOfLength(3); detected {
			pt.notifyCircular(pattern, sigHash)
			return true, pattern
		}
	}

	return false, ""
}

// notifyCircular invokes the OnCircular callback if set.
func (pt *PatternTracker) notifyCircular(pattern, signatureHash string) {
	if pt.OnCircular != nil {
		pt.OnCircular(pattern, signatureHash)
	}
}

// detectPatternOfLength checks if the last elements form a repeating pattern
// of the given length, appearing at least 3 times consecutively.
// Returns (detected, patternDescription, signatureHash).
func (pt *PatternTracker) detectPatternOfLength(patternLen int) (bool, string, string) {
	n := len(pt.recentCalls)
	minRequired := patternLen * 3 // Need 3 repetitions

	if n < minRequired {
		return false, "", ""
	}

	// Extract the candidate pattern signatures from the most recent calls
	patternSigs := make([]string, patternLen)
	patternNames := make([]string, patternLen)
	for i := 0; i < patternLen; i++ {
		entry := pt.recentCalls[n-patternLen+i]
		patternSigs[i] = entry.signature
		patternNames[i] = entry.toolName
	}

	// Count consecutive matches going backwards
	repetitions := 1
	for i := n - patternLen*2; i >= 0; i -= patternLen {
		// Check if this segment matches the pattern (by signature)
		match := true
		for j := 0; j < patternLen; j++ {
			if pt.recentCalls[i+j].signature != patternSigs[j] {
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
		sigHash := computePatternSignatureHash(patternSigs)
		return true, formatPattern(patternNames), sigHash
	}

	return false, "", ""
}

// computePatternSignatureHash produces a stable hex hash from the ordered
// tool-call signatures that form a repeating pattern.
func computePatternSignatureHash(sigs []string) string {
	combined := strings.Join(sigs, "|")
	h := sha256.Sum256([]byte(combined))
	return fmt.Sprintf("%x", h[:8])
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
// Deprecated: Use InjectThinkStep for better intervention.
func InjectWarning(pattern string) string {
	return fmt.Sprintf("Warning: Detected circular tool call pattern (%s). "+
		"This pattern has repeated 3+ times. Try a different approach to avoid wasting tokens.", pattern)
}

// InjectThinkStep creates a directive that forces the LLM to pause and reflect
// before continuing. This is more effective than a passive warning.
func InjectThinkStep(pattern string) string {
	return fmt.Sprintf(`STOP. Circular pattern detected: %s (repeated 3+ times).

Before making another tool call, you MUST:
1. State what you are trying to accomplish in one sentence
2. Explain why the previous attempts did not achieve the goal
3. Describe a DIFFERENT approach you will try

Do not repeat the same tool call with the same arguments. If you are stuck, ask the user for guidance instead of looping.`, pattern)
}
