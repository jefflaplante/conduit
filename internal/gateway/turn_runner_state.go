package gateway

import (
	"conduit/internal/sessions"
)

// Session processing state (conduit-3kgo).
//
// The runner is the single place that moves a session through the
// sessions.SessionStateTracker states, because every turn — channel
// ingress, WebSocket, the in-process TUI client, session wakes, sub-agents
// and scheduler jobs — goes through Run:
//
//   - Processing once the turn holds the session lock and is registered as
//     running (a turn queued behind another never becomes Processing on its
//     own: the session already is);
//   - Idle when the turn ends without error, including /stop or shutdown
//     cancellation, silent and empty replies;
//   - Error when the turn ends with a non-cancellation error, or when the
//     turn body panicked (the panic still propagates; the deferred
//     transition guarantees the session is never left in Processing).
//
// Canceled is terminal (conduit-38cz) and set by recordSubAgentEnd after
// Run returns; the runner never overwrites it.
//
// Waiting is not set by the runner. Nothing in a turn blocks on an external
// party: approvals (conduit-31jg.43) are non-blocking tickets that resolve
// on a later inbound message, and sub-agents report back through session
// wakes. A pending approval is therefore reported by /status as Idle.
//
// State writes are best-effort: a failed write is logged and never fails
// the turn. Each write is an in-memory tracker update plus one updated_at
// UPDATE on the sessions row, so a turn costs two small writes.

// setTurnState moves key to state unless the session is terminally
// Canceled. err describes a failed turn (Error state) and may be nil.
func (r *TurnRunner) setTurnState(key string, state sessions.SessionState, err error) {
	if r.sessions == nil || key == "" {
		return
	}
	if cur, ok := r.sessions.GetSessionState(key); ok && cur == sessions.SessionStateCanceled {
		return
	}
	var md map[string]interface{}
	if err != nil {
		md = map[string]interface{}{"error": err.Error()}
	}
	if uerr := r.sessions.UpdateSessionState(key, state, md); uerr != nil {
		r.logger.Warn("turn: failed to update session state", "session_key", key, "state", state, "error", uerr)
	}
}

// endTurnState picks the state a finished turn leaves its session in.
// completed is false when the turn body did not return normally (panic).
func (r *TurnRunner) endTurnState(key string, res *TurnResult, completed bool) {
	switch {
	case !completed:
		r.setTurnState(key, sessions.SessionStateError, errTurnPanicked)
	case res != nil && res.Err != nil && !res.Cancelled:
		r.setTurnState(key, sessions.SessionStateError, res.Err)
	default:
		r.setTurnState(key, sessions.SessionStateIdle, nil)
	}
}
