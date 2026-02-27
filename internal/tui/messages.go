package tui

import (
	"time"

	"conduit/pkg/protocol"
)

// BubbleTea message types produced by the WebSocket read pump

// ConnectedMsg signals successful WebSocket connection
type ConnectedMsg struct{}

// DisconnectedMsg signals WebSocket disconnection
type DisconnectedMsg struct {
	Err error
}

// ReconnectingMsg signals a reconnection attempt
type ReconnectingMsg struct {
	Attempt int
}

// StreamStartMsg signals the beginning of a streaming AI response
type StreamStartMsg struct {
	SessionKey string
	RequestID  string
}

// StreamDeltaMsg delivers a text chunk during streaming
type StreamDeltaMsg struct {
	SessionKey string
	RequestID  string
	Delta      string
}

// StreamEndMsg signals completion of a streaming response
type StreamEndMsg struct {
	SessionKey       string
	RequestID        string
	Content          string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Model            string
	RequestCost      float64
	SessionCost      float64
}

// ToolEventMsg wraps a tool execution lifecycle event
type ToolEventMsg struct {
	protocol.ToolEvent
}

// CommandResponseMsg delivers the result of a slash command
type CommandResponseMsg struct {
	SessionKey string
	RequestID  string
	Command    string
	Response   string
	Model      string
}

// SessionListMsg delivers the list of available sessions
type SessionListMsg struct {
	Sessions []protocol.SessionInfo
}

// SessionCreatedMsg signals that a new session was created
type SessionCreatedMsg struct {
	Key       string
	RequestID string // For correlating with the tab that made the request
	CreatedAt time.Time
}

// SessionSwitchedMsg signals that the session was switched, with history
type SessionSwitchedMsg struct {
	Key       string
	History   []protocol.MessageInfo
	Model     string
	CreatedAt time.Time
}

// GatewayInfoMsg delivers server metadata (e.g. agent name) from the gateway
type GatewayInfoMsg struct {
	AssistantName string
	Version       string
	GitCommit     string
	UptimeSeconds int64
	ModelAliases  map[string]string
	ToolCount     int
	SkillCount    int
}

// ThinkingTickMsg drives the KITT scanner animation in the chat view.
type ThinkingTickMsg struct{}

// ErrorMsg signals an error from the gateway
type ErrorMsg struct {
	SessionKey string
	RequestID  string
	Code       string
	Message    string
}
