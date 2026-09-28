package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/approval"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// Sub-agent cancel + lineage persistence (conduit-38cz part 2). The
// registry itself is in subagent_registry.go.

// Session context keys for persisted sub-agent lineage.
const (
	ctxKeyParentSessionKey = "parent_session_key"
	ctxKeySubAgentStatus   = "subagent_status"
	ctxKeySubAgentCancelBy = "subagent_canceled_by"
)

// CancelSubAgent cancels a running sub-agent (by session key, or by label)
// and every running sub-agent below it. It is the gateway side of the
// SessionsCancel tool.
//
// Guard: only an ancestor of the sub-agent (the session that spawned it, or
// one further up the chain) or the owner — a turn started by a live human
// message (isOwnerTurn) — may cancel it. A sub-agent cannot cancel
// itself or a sibling.
//
// The canceled sub-agent's turn ends with SessionStateCanceled and its
// parent is woken with WakeSourceSubAgentCanceled; descendants canceled
// along with it are quiet (their parent is gone too). It returns the
// canceled session key and how many sub-agents were canceled in total.
func (g *Gateway) CancelSubAgent(ctx context.Context, sessionKey, label, reason string) (string, int, error) {
	caller := types.RequestSessionKey(ctx)
	runner := g.turns()
	if sessionKey == "" {
		if label == "" {
			return "", 0, fmt.Errorf("either sessionKey or label must be provided")
		}
		info, ok := runner.subagents.findByLabel(caller, label)
		if !ok {
			return "", 0, fmt.Errorf("no running sub-agent labelled %q", label)
		}
		sessionKey = info.SessionKey
	}
	info, ok := runner.SubAgent(sessionKey)
	if !ok {
		return "", 0, fmt.Errorf("session %q is not a sub-agent spawned since the gateway started", sessionKey)
	}

	by := caller
	switch {
	case caller != "" && runner.subagents.isAncestor(caller, sessionKey):
	case isOwnerTurn(ctx):
		by = "owner"
		if caller != "" {
			by = "owner (" + caller + ")"
		}
	default:
		return "", 0, fmt.Errorf("permission denied: only the session that spawned sub-agent %s (%s) or the owner can cancel it", sessionKey, info.ParentSessionKey)
	}
	if info.Status != SubAgentRunning {
		return sessionKey, 0, fmt.Errorf("sub-agent %s already %s", sessionKey, info.Status)
	}
	if reason != "" {
		by += ": " + reason
	}
	n := runner.CancelSubAgent(sessionKey, by, false)
	log.Printf("[SubAgent] %s canceled by %s (%d sub-agent(s) incl. descendants)", sessionKey, by, n)
	return sessionKey, n, nil
}

// recordSubAgentEnd persists the sub-agent's final status in its session
// context and, when canceled, sets SessionStateCanceled.
func (g *Gateway) recordSubAgentEnd(sessionKey string, status SubAgentStatus, canceledBy string) {
	batch := map[string]string{ctxKeySubAgentStatus: string(status)}
	if status == SubAgentCanceled {
		batch[ctxKeySubAgentCancelBy] = canceledBy
	}
	if err := g.sessions.SetSessionContextBatch(sessionKey, batch); err != nil {
		log.Printf("[SubAgent] failed to record status for %s: %v", sessionKey, err)
	}
	if status == SubAgentCanceled {
		if err := g.sessions.UpdateSessionState(sessionKey, sessions.SessionStateCanceled, map[string]interface{}{
			"canceled_by": canceledBy,
		}); err != nil {
			log.Printf("[SubAgent] failed to set canceled state for %s: %v", sessionKey, err)
		}
	}
}

// subAgentParentGone reports whether parent is a sub-agent that was
// canceled: waking it would resurrect it.
func (g *Gateway) subAgentParentGone(parent string) bool {
	info, ok := g.turns().SubAgent(parent)
	return ok && info.Status == SubAgentCanceled
}

// wakeParentCanceled tells the parent its sub-agent was canceled
// (WakeSourceSubAgentCanceled), unless the cancel was quiet (/stop cascade,
// descendant of a canceled sub-agent).
func (g *Gateway) wakeParentCanceled(sessionKey string, sp subAgentSpawn, info SubAgentInfo, quiet bool) {
	if quiet || sp.parentSessionKey == "" {
		log.Printf("[SubAgent] %s canceled by %s (quiet: parent not woken)", sessionKey, info.CanceledBy)
		return
	}
	name := sessionKey
	if info.Label != "" {
		name = fmt.Sprintf("%s (%q)", sessionKey, info.Label)
	}
	msg := fmt.Sprintf("Sub-agent %s was canceled by %s after %s. It did not complete its task (%q); anything it produced before the cancel is in its session transcript.",
		name, info.CanceledBy, info.EndedAt.Sub(info.StartedAt).Round(time.Second), info.Task)
	if err := g.sendToSessionWakeWithSource(context.Background(), sp.parentSessionKey, "", msg, types.WakeSourceSubAgentCanceled); err != nil {
		log.Printf("[SubAgent] Failed to wake parent session %s on cancel: %v", sp.parentSessionKey, err)
	}
}

// isOwnerTurn reports whether ctx is a turn started by a live human message
// (the gateway's human inbound paths set the interactive origin; wakes,
// cron, heartbeats and sub-agents never do). Unlike Manager.Request
// it does not require a channel that can prompt.
func isOwnerTurn(ctx context.Context) bool {
	o, ok := approval.OriginFrom(ctx)
	return ok && o.Interactive
}

// subAgentStatusList renders a session's sub-agents for GetSessionStatus.
func subAgentStatusList(infos []SubAgentInfo) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(infos))
	for _, i := range infos {
		m := map[string]interface{}{
			"session_key": i.SessionKey,
			"status":      string(i.Status),
			"model":       i.Model,
			"task":        i.Task,
			"started_at":  i.StartedAt,
		}
		if i.Label != "" {
			m["label"] = i.Label
		}
		if !i.EndedAt.IsZero() {
			m["ended_at"] = i.EndedAt
		}
		if i.CanceledBy != "" {
			m["canceled_by"] = i.CanceledBy
		}
		out = append(out, m)
	}
	return out
}
