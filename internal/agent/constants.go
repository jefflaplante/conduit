package agent

import "conduit/internal/constants"

// Re-export constants from the central constants package for backward compatibility
// and convenience within the agent package.

// SilentReplyToken is used when the AI has nothing to say to the user.
const SilentReplyToken = constants.SilentReplyToken

// HeartbeatOKToken is used when a heartbeat check finds nothing requiring attention.
const HeartbeatOKToken = constants.HeartbeatOKToken

// SilentResponseTokens contains all tokens that indicate a response should be suppressed.
var SilentResponseTokens = constants.SilentResponseTokens

// SupportedChannels is the canonical list of messaging channels that Conduit supports.
var SupportedChannels = constants.SupportedChannels
