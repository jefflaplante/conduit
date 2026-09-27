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
// HandleToolCallFlow call, lives on the turnBudget for the whole tool loop,
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
// (pivot + think-step + progress reminder). The loop drops the message by
// position before the next round (turnState.advance, conduit-31jg.37).
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
//
// lead parts (the conduit-8ba7 progress reminder, conduit-31jg.14) come first
// and are emitted even when cs is nil.
func (cs *chainState) takeGuidance(lead ...string) string {
	var parts []string
	for _, l := range lead {
		if l != "" {
			parts = append(parts, l)
		}
	}
	if cs == nil {
		if len(parts) == 0 {
			return ""
		}
		return loopGuidanceMarker + strings.Join(parts, "\n\n")
	}

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
