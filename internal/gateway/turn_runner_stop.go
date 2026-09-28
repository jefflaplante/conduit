package gateway

import (
	"fmt"
)

// Stop cancels the running turn for sessionKey and drops every turn queued
// behind it (conduit-31jg.23). Every turn in flight at the moment of the call
// is cancelled — a turn that is mid-hand-off from queued to running is in one
// of the two sets, both under r.mu. Turns that arrive after Stop are not
// affected.
func (r *TurnRunner) Stop(sessionKey string) (stoppedRunning bool, droppedQueued int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, q := range r.queued[sessionKey] {
		q.cancel()
	}
	droppedQueued = len(r.queued[sessionKey])
	delete(r.queued, sessionKey)

	r.active.mu.RLock()
	cancel, ok := r.active.get()[sessionKey]
	r.active.mu.RUnlock()
	if ok && cancel != nil {
		cancel()
		stoppedRunning = true
	}
	return stoppedRunning, droppedQueued
}

// stopResponse renders StopTree's outcome for the /stop commands,
// including how many sub-agents the cascade stopped (conduit-31jg.84).
func stopResponse(res StopResult) (string, bool) {
	sub := subAgentsLine(res.SubAgents)
	switch {
	case res.StoppedRunning && res.DroppedQueued > 0:
		return fmt.Sprintf("Stopping current operation... (%d queued message(s) dropped)", res.DroppedQueued) + sub, true
	case res.StoppedRunning:
		return "Stopping current operation..." + sub, true
	case res.DroppedQueued > 0:
		return fmt.Sprintf("Dropped %d queued message(s).", res.DroppedQueued) + sub, true
	case res.SubAgents > 0:
		return fmt.Sprintf("Stopped %d sub-agent(s).", res.SubAgents), true
	}
	return "No active operation to stop.", false
}

// StopResult is what /stop did (conduit-31jg.84).
type StopResult struct {
	StoppedRunning bool
	DroppedQueued  int
	// SubAgents is the number of running sub-agents (children,
	// grandchildren, …) canceled by the cascade.
	SubAgents int
}

// StopTree is /stop: Stop(sessionKey) plus a cascade to every running
// sub-agent sessionKey spawned, recursively (conduit-31jg.84). Cascaded
// cancels are quiet: the parent is not woken (the human just said stop).
func (r *TurnRunner) StopTree(sessionKey string) StopResult {
	var res StopResult
	res.StoppedRunning, res.DroppedQueued = r.Stop(sessionKey)
	keys, cancels := r.subagents.markTree(sessionKey, false, "/stop", true)
	r.cancelTree(keys, cancels)
	res.SubAgents = len(keys)
	return res
}

// subAgentsLine renders a /stop sub-agent count suffix.
func subAgentsLine(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d sub-agent(s) stopped)", n)
}
