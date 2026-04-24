package reflection

import (
	"context"
	"log"
	"strings"
	"time"
)

// ToolOutcomeInfo holds the information the middleware needs to record a
// tool execution outcome. This mirrors the data available from
// ExecutionResult in the tools package without creating an import cycle.
type ToolOutcomeInfo struct {
	ToolName   string
	SessionKey string        // session key from request context (may be empty)
	Success    bool          // true when the tool returned no error and Result.Success is true
	Error      string        // non-empty on failure
	IsTimeout  bool          // true when the failure was a context deadline exceeded
	Duration   time.Duration // wall-clock execution time
	RetryCount int           // retries before resolution (from ToolResult.Retries)
}

// AfterExecutionHook is a callback signature that the execution engine can
// invoke after every tool execution. The tools package registers this via
// a simple function field to avoid a direct dependency on the reflection
// package's concrete types.
type AfterExecutionHook func(ctx context.Context, info ToolOutcomeInfo)

// Reflection insert deadline gating: when the caller's context is close to
// expiring we either skip or cap the detached insert timeout. This keeps
// best-effort reflection writes from piling up behind a slow DB after the
// caller has already given up.
const (
	reflectionInsertTimeout      = 5 * time.Second
	reflectionInsertMinRemaining = 1 * time.Second
	reflectionInsertSafetyMargin = 100 * time.Millisecond
)

// ReflectionMiddleware captures tool execution outcomes to the reflection
// store. It is designed to be minimally invasive: a single AfterExecution
// callback that the ExecutionEngine fires after each tool completes.
type ReflectionMiddleware struct {
	store  *ReflectionStore
	config *ReflectionConfig

	// insertFn is the function used to write a reflection entry. It defaults
	// to m.store.Insert; tests may swap it to verify gating behavior.
	insertFn func(ctx context.Context, entry *ReflectionEntry) error
}

// NewReflectionMiddleware creates a middleware that logs tool outcomes to
// the reflection store, respecting the capture-level policy.
func NewReflectionMiddleware(store *ReflectionStore, config *ReflectionConfig) *ReflectionMiddleware {
	m := &ReflectionMiddleware{
		store:  store,
		config: config,
	}
	if store != nil {
		m.insertFn = store.Insert
	}
	return m
}

// Hook returns an AfterExecutionHook suitable for registering on the
// ExecutionEngine. The returned function is safe for concurrent use.
func (m *ReflectionMiddleware) Hook() AfterExecutionHook {
	return func(ctx context.Context, info ToolOutcomeInfo) {
		m.recordOutcome(ctx, info)
	}
}

// recordOutcome determines the outcome, checks capture policy, builds a
// ReflectionEntry, and writes it to the store.
func (m *ReflectionMiddleware) recordOutcome(ctx context.Context, info ToolOutcomeInfo) {
	if m.store == nil || m.config == nil || !m.config.Enabled || m.insertFn == nil {
		return
	}

	outcome := classifyOutcome(info)

	if !m.config.ShouldCapture(outcome) {
		return
	}

	// Gate the detached insert on the caller's remaining deadline. If the
	// caller has already (nearly) timed out, skip the insert entirely rather
	// than tying up a goroutine for the full timeout against a slow DB.
	insertTimeout := reflectionInsertTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < reflectionInsertMinRemaining {
			log.Printf("[ReflectionMiddleware] skipping insert for tool %s: caller has %s remaining", info.ToolName, remaining)
			return
		}
		if capped := remaining - reflectionInsertSafetyMargin; capped < insertTimeout {
			insertTimeout = capped
		}
	}

	entry := NewEntry("system", TypeToolOutcome, outcome)
	entry.Tool = info.ToolName
	entry.Duration = info.Duration
	entry.RetryCount = info.RetryCount

	// Populate insight with error message on failure/timeout.
	if outcome == OutcomeFailure || outcome == OutcomeTimeout {
		entry.Insight = info.Error
	}

	// Set session key for cross-session correlation.
	entry.SessionKey = info.SessionKey

	// Use a detached context with a bounded timeout. The caller's context may
	// already be expired (e.g. tool hit its deadline), but we still want to
	// record the outcome. Reflection is best-effort and should not inherit
	// the tool execution's lifecycle.
	insertCtx, cancel := context.WithTimeout(context.Background(), insertTimeout)
	defer cancel()

	if err := m.insertFn(insertCtx, entry); err != nil {
		// Reflection is best-effort; never fail the tool call.
		log.Printf("[ReflectionMiddleware] failed to insert entry for tool %s: %v", info.ToolName, err)
	}
}

// classifyOutcome maps ToolOutcomeInfo fields to an Outcome constant.
func classifyOutcome(info ToolOutcomeInfo) Outcome {
	if info.IsTimeout {
		return OutcomeTimeout
	}
	if !info.Success || info.Error != "" {
		return OutcomeFailure
	}
	return OutcomeSuccess
}

// IsTimeoutError returns true when the error string indicates a context
// deadline or timeout. Useful for callers that need to populate IsTimeout.
func IsTimeoutError(errStr string) bool {
	if errStr == "" {
		return false
	}
	lower := strings.ToLower(errStr)
	return strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "timeout")
}
