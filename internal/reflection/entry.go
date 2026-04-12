package reflection

import (
	"time"

	"github.com/google/uuid"
)

// ReflectionType categorizes the kind of reflection entry.
type ReflectionType string

const (
	TypeToolOutcome    ReflectionType = "tool_outcome"
	TypeSessionSummary ReflectionType = "session_summary"
	TypePattern        ReflectionType = "pattern"
	TypeLearned        ReflectionType = "learned"
)

// Outcome describes the result of an action being reflected upon.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
	OutcomePartial Outcome = "partial"
	OutcomeTimeout Outcome = "timeout"
)

// ReflectionEntry captures a single reflection data point from either the
// system (Go auto-capture) or the model (prompt-driven reflection).
type ReflectionEntry struct {
	ID          string         `json:"id"`              // UUID
	SessionKey  string         `json:"session_key"`     // Source session
	Timestamp   time.Time      `json:"timestamp"`
	Source      string         `json:"source"`          // "system" | "model"
	Type        ReflectionType `json:"type"`            // tool_outcome, session_summary, pattern, learned
	Tool        string         `json:"tool,omitempty"`  // Tool name (if tool-related)
	Outcome     Outcome        `json:"outcome"`         // success, failure, partial, timeout
	RetryCount  int            `json:"retry_count"`     // Retries before resolution
	Duration    time.Duration  `json:"duration"`        // Execution time
	Insight     string         `json:"insight"`         // Human-readable lesson
	Score       int            `json:"score"`           // 1-5 outcome rating (0 = unscored)
	Tags        []string       `json:"tags"`            // Free-form grouping tags
	RelatedKeys []string       `json:"related_keys"`    // Connected Brain keys
}

// NewEntry creates a ReflectionEntry with a generated UUID and current timestamp.
func NewEntry(source string, entryType ReflectionType, outcome Outcome) *ReflectionEntry {
	return &ReflectionEntry{
		ID:        uuid.New().String(),
		Timestamp: time.Now(),
		Source:    source,
		Type:      entryType,
		Outcome:   outcome,
	}
}
