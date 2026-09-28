package tools

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/types"
)

// callTimeoutSlack lets a tool's own per-call timeout fire (and report a
// proper timeout result) before the engine deadline does.
const callTimeoutSlack = 5 * time.Second

// callTimeout returns the deadline for one call: the engine default, or
// longer when the tool honours a per-call `timeout` (Bash). conduit-31jg.39.
func (e *ExecutionEngine) callTimeout(call ai.ToolCall) time.Duration {
	d := e.timeout
	if d <= 0 {
		d = 60 * time.Second
	}
	if p, ok := e.registry.(interface {
		CallTimeout(name string, args map[string]interface{}) (time.Duration, bool)
	}); ok {
		if req, ok := p.CallTimeout(call.Name, call.Args); ok && req+callTimeoutSlack > d {
			d = req + callTimeoutSlack
		}
	}
	return d
}

// conduit-31jg.88: once the gateway drains for a restart, a tool call may
// run at most until the drain deadline minus drainToolMargin (never less
// than drainToolFloor): a `sleep 150` started mid-drain was force-cancelled
// with no chance for the model to wrap up.
const (
	drainToolMargin = 3 * time.Second
	drainToolFloor  = time.Second
)

// DrainToolHint is appended to every tool result produced during a drain.
const DrainToolHint = "[System: the gateway is restarting; wrap up now. Give the user a short status and do not start new long-running work. This tool call was limited to %s.]"

// drainCappedTimeout caps d to the remaining drain budget when ctx reports
// a shutdown drain (types.DrainDeadline). draining reports whether it did.
func drainCappedTimeout(ctx context.Context, d time.Duration) (time.Duration, bool) {
	deadline, ok := types.DrainDeadline(ctx)
	if !ok {
		return d, false
	}
	left := time.Until(deadline)
	capped := left - drainToolMargin
	if capped < drainToolFloor {
		capped = min(drainToolFloor, left)
	}
	if capped <= 0 {
		// Drain already over: the turn is being force-cancelled anyway.
		capped = drainToolFloor
	}
	return min(d, capped), true
}

// addDrainHint appends DrainToolHint to the result the model will see. The
// result is copied: tools may hand out shared (cached) results.
func addDrainHint(res *ExecutionResult, limit time.Duration) {
	hint := fmt.Sprintf(DrainToolHint, limit.Round(100*time.Millisecond))
	if res.Result == nil {
		res.Result = &ToolResult{Success: res.Error == nil, Content: hint}
		return
	}
	r := *res.Result
	if r.Content != "" {
		r.Content += "\n\n"
	}
	r.Content += hint
	res.Result = &r
}
