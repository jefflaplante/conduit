// Package approval is Conduit's generic human-in-the-loop approval primitive
// (conduit-31jg.43). A tool that wants to perform a risky action registers a
// pending approval bound to the originating session and to a fingerprint of
// the exact action parameters. The gateway prompts the human in the channel
// the turn came from; the action runs only when that human replies
// "YES <code>" (typed or via an inline button) on that same channel/session.
//
// Approvals can only be granted by inbound human messages that the gateway
// hands to Manager.HandleReply before they are queued for the model. Nothing
// the model emits (tool args, assistant text, inter-session sends, wakes)
// travels that path, so the model can never approve its own request.
//
// The package is deliberately a leaf (stdlib only) so tools, skills, the
// gateway and future runbook gates (conduit-w3l7) can all import it.
package approval

import "context"

// Choice is an optional quick-reply button offered alongside a prompt. Reply
// is the exact text the button sends back as if the human typed it.
type Choice struct {
	Label string `json:"label"`
	Reply string `json:"reply"`
}

// Outgoing-message metadata keys used by channel adapters to render
// approval notices (conduit-31jg.43).
const (
	// MetaNotice marks a gateway-generated approval notice. Adapters must
	// render it verbatim (no model-output sanitizing/markdown conversion) so
	// the human sees exactly what they are approving.
	MetaNotice = "approval_notice"
	// MetaChoices holds a JSON []Choice for adapters that support buttons.
	MetaChoices = "approval_choices"
)

// Notice is a message delivered to the human on the originating channel.
type Notice struct {
	Text    string
	Choices []Choice
}

// Notifier delivers a Notice to the human who owns the originating turn.
// Returning an error means the channel cannot prompt; approvals then fail
// closed.
type Notifier func(ctx context.Context, n Notice) error

// Origin describes where the current turn came from.
type Origin struct {
	// Interactive is true only for turns started by a live human message on
	// a channel that can be prompted (Telegram, TUI, WebSocket chat).
	Interactive bool
	// Source labels the entry point ("telegram", "websocket", "tui", or a
	// non-interactive reason such as "heartbeat", "cron", "subagent", "wake").
	Source     string
	ChannelID  string
	UserID     string
	SessionKey string
	// Notify reaches the human on the originating channel. Nil means the
	// channel cannot prompt.
	Notify Notifier
}

type ctxKeyOrigin struct{}

// WithInteractiveOrigin marks ctx as a live, human-initiated turn. Only the
// gateway's human inbound paths should call this (conduit-31jg.43).
func WithInteractiveOrigin(ctx context.Context, o Origin) context.Context {
	o.Interactive = true
	return context.WithValue(ctx, ctxKeyOrigin{}, o)
}

// WithNonInteractive marks ctx as non-interactive, overriding any origin
// inherited from a parent context. source names the entry point for error
// messages and audit logs.
func WithNonInteractive(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, ctxKeyOrigin{}, Origin{Interactive: false, Source: source})
}

// OriginFrom returns the origin attached to ctx. ok is false when the
// origin is unknown, which callers must treat as non-interactive.
func OriginFrom(ctx context.Context) (Origin, bool) {
	if ctx == nil {
		return Origin{}, false
	}
	o, ok := ctx.Value(ctxKeyOrigin{}).(Origin)
	return o, ok
}
