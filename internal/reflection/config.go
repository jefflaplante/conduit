package reflection

// ReflectionConfig holds configuration for the reflection subsystem.
type ReflectionConfig struct {
	Enabled      bool   `json:"enabled"`       // Master switch (default true)
	CaptureLevel string `json:"capture_level"` // "all" | "failures" | "anomalies"
	RetentionDays int   `json:"retention_days"` // Days before REM grooms old entries
}

// DefaultConfig returns sensible defaults for reflection.
// Reflection is on by default with no config required.
func DefaultConfig() *ReflectionConfig {
	return &ReflectionConfig{
		Enabled:       true,
		CaptureLevel:  "all",
		RetentionDays: 30,
	}
}

// ShouldCapture reports whether an entry with the given outcome should be
// recorded under the current capture level.
//
//   - "all"       → always true
//   - "failures"  → true for OutcomeFailure and OutcomeTimeout
//   - "anomalies" → true for OutcomeFailure, OutcomeTimeout, and OutcomePartial
//     (the caller is also expected to check for slow executions)
func (c *ReflectionConfig) ShouldCapture(outcome Outcome) bool {
	switch c.CaptureLevel {
	case "all":
		return true
	case "failures":
		return outcome == OutcomeFailure || outcome == OutcomeTimeout
	case "anomalies":
		return outcome == OutcomeFailure || outcome == OutcomeTimeout || outcome == OutcomePartial
	default:
		// Unknown level: capture everything as a safe fallback.
		return true
	}
}
