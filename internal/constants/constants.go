// Package constants provides shared constant values used across the codebase.
// This package has no dependencies to avoid import cycles.
package constants

// Silent response tokens - single source of truth for response suppression markers.
// These tokens signal that the AI response should not be delivered to the user.
const (
	// SilentReplyToken is used when the AI has nothing to say to the user.
	// The response is suppressed and not delivered.
	SilentReplyToken = "NO_REPLY"

	// HeartbeatOKToken is used when a heartbeat check finds nothing requiring attention.
	// The response is suppressed and not delivered.
	HeartbeatOKToken = "HEARTBEAT_OK"
)

// SilentResponseTokens contains all tokens that indicate a response should be suppressed.
var SilentResponseTokens = []string{SilentReplyToken, HeartbeatOKToken}

// SupportedChannels is the canonical list of messaging channels that Conduit supports.
// This list is used in system prompts and for channel validation.
var SupportedChannels = []string{
	"telegram",
	"whatsapp",
	"discord",
	"googlechat",
	"slack",
	"signal",
	"imessage",
}
