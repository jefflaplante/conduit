package types

import (
	"context"
	"time"
)

// Context key types for per-request values (replaces shared mutable fields).
type ctxKeyChannelID struct{}

type ctxKeyUserID struct{}

type ctxKeySessionKey struct{}

type ctxKeyWakeSource struct{}

// Wake source values attached to the context when a session is woken. Lets the
// agent system and tools tell a normal user message apart from a callback, and
// further tell announced sub-agent results (user already saw them) from silent
// ones (user hasn't — parent must decide whether to surface).
const (
	WakeSourceInterSession      = "inter_session"
	WakeSourceSubAgentCallback  = "sub_agent_callback"  // kept for backwards compatibility
	WakeSourceSubAgentAnnounced = "sub_agent_announced" // raw result already posted to channel
	WakeSourceSubAgentSilent    = "sub_agent_silent"    // raw result NOT posted; parent must decide
	WakeSourceSubAgentFailed    = "sub_agent_failed"    // sub-agent encountered an error
	WakeSourceSubAgentCanceled  = "sub_agent_canceled"  // sub-agent canceled via SessionsCancel (conduit-38cz)
	WakeSourceHeartbeat         = "heartbeat"
)

// WithRequestContext attaches per-request channel, user, and session info to ctx.
func WithRequestContext(ctx context.Context, channelID, userID, sessionKey string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyChannelID{}, channelID)
	ctx = context.WithValue(ctx, ctxKeyUserID{}, userID)
	ctx = context.WithValue(ctx, ctxKeySessionKey{}, sessionKey)
	return ctx
}

// RequestChannelID returns the channel ID from ctx, or "" if unset.
func RequestChannelID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyChannelID{}).(string); ok {
		return v
	}
	return ""
}

// RequestUserID returns the user ID from ctx, or "" if unset.
func RequestUserID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyUserID{}).(string); ok {
		return v
	}
	return ""
}

// RequestSessionKey returns the session key from ctx, or "" if unset.
func RequestSessionKey(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeySessionKey{}).(string); ok {
		return v
	}
	return ""
}

// WithWakeSource attaches a wake-source tag to ctx (e.g. "sub_agent_callback").
// An empty source is a no-op so callers don't have to branch on it.
func WithWakeSource(ctx context.Context, source string) context.Context {
	if source == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyWakeSource{}, source)
}

// WakeSource returns the wake-source tag from ctx, or "" if the request is not a wake.
func WakeSource(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyWakeSource{}).(string); ok {
		return v
	}
	return ""
}

// WakeSourceRestartResume tags the wake that auto-resumes a turn interrupted
// by a gateway restart (conduit-31jg.88, restart_resume: "auto").
const WakeSourceRestartResume = "restart_resume"

type ctxKeyDrainDeadline struct{}

// WithDrainDeadline attaches fn, which reports the end of the gateway's
// shutdown drain once one has begun (ok=false while running normally). The
// TurnRunner sets it on every turn so tools can see a drain that starts
// mid-turn without importing the gateway (conduit-31jg.88).
func WithDrainDeadline(ctx context.Context, fn func() (time.Time, bool)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyDrainDeadline{}, fn)
}

// DrainDeadline reports the shutdown drain deadline, if the gateway is
// draining. ok is false when not draining or when ctx carries no drain hook.
func DrainDeadline(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	fn, ok := ctx.Value(ctxKeyDrainDeadline{}).(func() (time.Time, bool))
	if !ok || fn == nil {
		return time.Time{}, false
	}
	return fn()
}
