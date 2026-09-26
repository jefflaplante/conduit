package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"conduit/internal/ai"
)

// conduit-31jg.13: per-turn tool-loop state.
//
// Before this bead the gateway's single ExecutionEngine owned one
// PatternTracker and one FailureTracker that were never reset, so a heartbeat
// tool failing three times injected "Multiple failures detected" into
// unrelated users' turns, and circular-pattern detection interleaved calls
// from different sessions. chainState is created once per top-level
// HandleToolCallFlow call, travels through the recursion on the turnBudget,
// and reaches executeSingle via the context. Nothing in it outlives the turn.
//
// Cross-session learning (SPAR, conduit-17wz / conduit-2ngi) is preserved by
// forwarding each threshold crossing / circular detection to engine-level
// hooks (SetPivotHook / SetCircularHook) that persist an aggregate record —
// the store, not the prompt, is where other sessions learn from it.

// PivotHook receives a per-turn failure threshold crossing. ctx carries the
// originating session (types.RequestSessionKey). Must be safe for concurrent
// use; runs synchronously on the tool-execution goroutine.
type PivotHook func(ctx context.Context, toolName string, failCount int, lastError string)

// CircularHook receives a per-turn circular-pattern detection. ctx carries the
// originating session. Must be safe for concurrent use.
type CircularHook func(ctx context.Context, pattern string, signatureHash string)

// loopGuidanceMarker prefixes the ephemeral user-role guidance message
// (pivot + think-step). Shared by the injection and strip sites so they
// cannot drift apart (same pattern as progressReminderMarker).
const loopGuidanceMarker = "[Tool-loop guidance] "

type chainStateKey struct{}

// chainState is the per-turn tracker bundle (conduit-31jg.13).
type chainState struct {
	patterns *PatternTracker
	failures *FailureTracker

	mu            sync.Mutex
	pendingPivots []string // tools that crossed the failure threshold since the last guidance injection
}

// newChainState builds fresh trackers for one turn. ctx is the turn's
// context; it is captured so the SPAR hooks can attribute the event to the
// originating session.
func (e *ExecutionEngine) newChainState(ctx context.Context) *chainState {
	cs := &chainState{
		patterns: NewPatternTracker(10),
		failures: NewFailureTracker(3),
	}
	pivotHook := e.pivotHook
	circularHook := e.circularHook
	cs.failures.OnPivot = func(toolName string, failCount int, lastError string) {
		cs.mu.Lock()
		cs.pendingPivots = append(cs.pendingPivots, toolName)
		cs.mu.Unlock()
		if pivotHook != nil {
			pivotHook(ctx, toolName, failCount, lastError)
		}
	}
	if circularHook != nil {
		cs.patterns.OnCircular = func(pattern, signatureHash string) {
			circularHook(ctx, pattern, signatureHash)
		}
	}
	return cs
}

func withChainState(ctx context.Context, cs *chainState) context.Context {
	if cs == nil {
		return ctx
	}
	return context.WithValue(ctx, chainStateKey{}, cs)
}

func chainStateFrom(ctx context.Context) *chainState {
	cs, _ := ctx.Value(chainStateKey{}).(*chainState)
	return cs
}

// recordOutcome classifies one tool execution for the per-turn trackers.
// A Go error OR a result with Success=false counts as a failure
// (conduit-31jg.13: soft failures used to reset the counter instead).
func (cs *chainState) recordOutcome(call ai.ToolCall, result *ToolResult, err error) {
	if cs == nil {
		return
	}
	if err != nil {
		cs.failures.RecordFailure(call.Name, err.Error())
		return
	}
	// Pattern detection keeps its historical input: every call that did not
	// return a Go error.
	cs.patterns.RecordCall(call.Name, call.Args)
	if result != nil && !result.Success {
		msg := result.Error
		if msg == "" {
			msg = result.Content
		}
		cs.failures.RecordFailure(call.Name, msg)
		return
	}
	cs.failures.RecordSuccess(call.Name)
}

// takeGuidance drains this round's pivot triggers and checks for a circular
// pattern, returning the combined guidance text ("" when nothing fired).
// Each trigger yields guidance exactly once: pivots fire on the threshold
// crossing (FailureTracker.OnPivot), and the pattern tracker is reset after a
// detection so the same trailing calls cannot re-trigger it.
func (cs *chainState) takeGuidance() string {
	if cs == nil {
		return ""
	}
	var parts []string

	cs.mu.Lock()
	pivots := cs.pendingPivots
	cs.pendingPivots = nil
	cs.mu.Unlock()
	seen := make(map[string]bool, len(pivots))
	for _, tool := range pivots {
		if seen[tool] {
			continue
		}
		seen[tool] = true
		parts = append(parts, fmt.Sprintf(
			"Multiple failures detected with '%s'. Consider a different approach or tool.", tool))
	}

	if detected, pattern := cs.patterns.DetectCircular(); detected {
		parts = append(parts, InjectThinkStep(pattern))
		cs.patterns.Reset()
	}

	if len(parts) == 0 {
		return ""
	}
	return loopGuidanceMarker + strings.Join(parts, "\n\n")
}

// isEphemeralInjection reports whether m is a per-round-trip injection that
// must not be carried into the next depth: the conduit-8ba7 progress reminder
// (system role) or the conduit-31jg.13 loop guidance (user role).
func isEphemeralInjection(m ai.ChatMessage) bool {
	if m.Role == "system" && strings.Contains(m.Content, progressReminderMarker) {
		return true
	}
	if m.Role == "user" && strings.HasPrefix(m.Content, loopGuidanceMarker) {
		return true
	}
	return false
}

// stripEphemeral returns a copy of req without ephemeral injections. The
// original request is never mutated (its pointer may already be recorded by
// mocks/telemetry).
func stripEphemeral(req *ai.GenerateRequest) *ai.GenerateRequest {
	stripped := make([]ai.ChatMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		if isEphemeralInjection(m) {
			continue
		}
		stripped = append(stripped, m)
	}
	next := *req
	next.Messages = stripped
	return &next
}
