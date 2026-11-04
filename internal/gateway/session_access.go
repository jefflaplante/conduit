package gateway

import (
	"context"
	"fmt"
	"strconv"

	"conduit/internal/sessions"
	"conduit/internal/tools/core"
	"conduit/internal/tools/types"
)

// Inter-session access control and the wake recursion guard (H4).
//
// SessionsSend injects a user-role message into another session and can wake
// it, and Find scope=session reads other sessions' transcripts. A turn may
// only reach a target session that
//   - is its own session,
//   - belongs to the same user on the same channel as the requesting turn,
//   - is in its sub-agent ancestry (a parent/ancestor or a child/descendant),
//   - or the turn was started by a live human message (isOwnerTurn).
//
// Everything else fails closed: automation tokens, cron and heartbeat turns
// (no request user), MCP and sibling sub-agents cannot reach other users'
// sessions.

// maxWakeDepth bounds chains of sessions waking each other: a wake carries
// the waking turn's depth + 1 and wakes deeper than this are refused. A turn
// started by a human message is depth 0.
const maxWakeDepth = 3

// metaWakeDepth is the inter-session message metadata key carrying the
// depth of the wake that delivered it.
const metaWakeDepth = "wake_depth"

type ctxKeyWakeDepth struct{}

// withWakeDepth marks ctx as a wake turn at depth d.
func withWakeDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, ctxKeyWakeDepth{}, d)
}

// wakeDepthFrom returns the wake depth of the turn in ctx (0 for a turn not
// started by a wake, e.g. a human message).
func wakeDepthFrom(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	d, _ := ctx.Value(ctxKeyWakeDepth{}).(int)
	return d
}

// messageWakeDepth reads the wake depth a message was delivered with. An
// inter-session or wake message without one (older rows, restart resumes)
// counts as depth 1; a human message is 0.
func messageWakeDepth(meta map[string]string) int {
	if d, err := strconv.Atoi(meta[metaWakeDepth]); err == nil && d >= 0 {
		return d
	}
	if meta["source"] == "inter_session" || meta["wake_source"] != "" {
		return 1
	}
	return 0
}

// Find scope=session filters message hits through CanAccessSession.
var _ core.SessionAccessChecker = (*Gateway)(nil)

// CanAccessSession reports whether the turn in ctx may read or message the
// session sessionKey (Find scope=session, SessionsSend). Unknown sessions
// are not accessible.
func (g *Gateway) CanAccessSession(ctx context.Context, sessionKey string) bool {
	if g == nil || g.sessions == nil || sessionKey == "" {
		return false
	}
	if isOwnerTurn(ctx) {
		return true
	}
	target, err := g.sessions.GetSession(sessionKey)
	if err != nil || target == nil {
		return false
	}
	return g.authorizeSessionAccess(ctx, target) == nil
}

// authorizeSessionAccess applies the rules above to target.
func (g *Gateway) authorizeSessionAccess(ctx context.Context, target *sessions.Session) error {
	if isOwnerTurn(ctx) {
		return nil
	}
	caller := types.RequestSessionKey(ctx)
	if caller != "" && caller == target.Key {
		return nil
	}
	if user := types.RequestUserID(ctx); user != "" &&
		user == target.UserID && types.RequestChannelID(ctx) == target.ChannelID {
		return nil
	}
	if caller != "" {
		reg := g.turns().subagents
		if reg.isAncestor(caller, target.Key) || reg.isAncestor(target.Key, caller) {
			return nil
		}
	}
	return fmt.Errorf("permission denied: session %s belongs to another user and is not in this session's sub-agent lineage", target.Key)
}
