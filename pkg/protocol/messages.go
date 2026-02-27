package protocol

import (
	"encoding/json"
	"time"
)

// MessageType defines the type of protocol message
type MessageType string

const (
	// Incoming message types (from channels to gateway)
	TypeIncomingMessage MessageType = "incoming_message"
	TypeChannelStatus   MessageType = "channel_status"

	// Outgoing message types (from gateway to channels)
	TypeOutgoingMessage MessageType = "outgoing_message"
	TypeChannelCommand  MessageType = "channel_command"

	// WebSocket API message types (gateway <-> clients)
	TypeAgentRequest  MessageType = "agent_request"
	TypeAgentResponse MessageType = "agent_response"
	TypeHealthCheck   MessageType = "health_check"
	TypeSessionList   MessageType = "session_list"

	// TUI/SSH chat message types (gateway <-> TUI/SSH clients)
	TypeChatMessage     MessageType = "chat_message"     // client -> gateway: user sends chat
	TypeCommandMessage  MessageType = "command_message"  // client -> gateway: user sends slash command
	TypeStreamStart     MessageType = "stream_start"     // gateway -> client: AI response stream begins
	TypeStreamDelta     MessageType = "stream_delta"     // gateway -> client: text chunk during streaming
	TypeStreamEnd       MessageType = "stream_end"       // gateway -> client: stream complete
	TypeToolEvent       MessageType = "tool_event"       // gateway -> client: tool execution lifecycle
	TypeSessionSwitch   MessageType = "session_switch"   // bidirectional: create/switch/list sessions
	TypeCommandResponse MessageType = "command_response" // gateway -> client: response to slash command
	TypeErrorResponse   MessageType = "error_response"   // gateway -> client: error notification
	TypeGatewayInfo     MessageType = "gateway_info"     // gateway -> client: server metadata on connect
)

// BaseMessage contains common fields for all protocol messages
type BaseMessage struct {
	Type      MessageType `json:"type"`
	ID        string      `json:"id"`
	Timestamp time.Time   `json:"timestamp"`
}

// IncomingMessage represents a message received from a channel
type IncomingMessage struct {
	BaseMessage
	ChannelID  string            `json:"channel_id"`
	SessionKey string            `json:"session_key"`
	UserID     string            `json:"user_id"`
	Text       string            `json:"text"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// OutgoingMessage represents a message to be sent through a channel
type OutgoingMessage struct {
	BaseMessage
	ChannelID  string            `json:"channel_id"`
	SessionKey string            `json:"session_key"`
	UserID     string            `json:"user_id"`
	Text       string            `json:"text"`
	MediaPath  string            `json:"media_path,omitempty"`
	MediaType  string            `json:"media_type,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ChannelStatus represents status information from a channel adapter
type ChannelStatus struct {
	BaseMessage
	ChannelID string                 `json:"channel_id"`
	Status    string                 `json:"status"` // "online", "offline", "error"
	Details   map[string]interface{} `json:"details,omitempty"`
}

// ChannelCommand represents a command sent to a channel adapter
type ChannelCommand struct {
	BaseMessage
	ChannelID string                 `json:"channel_id"`
	Command   string                 `json:"command"`
	Args      map[string]interface{} `json:"args,omitempty"`
}

// AgentRequest represents a request to process a message with AI
type AgentRequest struct {
	BaseMessage
	SessionKey string            `json:"session_key"`
	Message    string            `json:"message"`
	Model      string            `json:"model,omitempty"`
	Tools      []string          `json:"tools,omitempty"`
	Context    map[string]string `json:"context,omitempty"`
}

// AgentResponse represents the AI's response
type AgentResponse struct {
	BaseMessage
	SessionKey string            `json:"session_key"`
	RequestID  string            `json:"request_id"`
	Response   string            `json:"response"`
	ToolCalls  []ToolCall        `json:"tool_calls,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ToolCall represents a tool function call
type ToolCall struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	Args   map[string]interface{} `json:"args"`
	Result string                 `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

// HealthCheck represents a health check request/response
type HealthCheck struct {
	BaseMessage
	Status   string                 `json:"status"`
	Services map[string]interface{} `json:"services,omitempty"`
}

// SessionList represents a list of active sessions
type SessionList struct {
	BaseMessage
	Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo contains information about an active session
type SessionInfo struct {
	Key          string            `json:"key"`
	UserID       string            `json:"user_id"`
	ChannelID    string            `json:"channel_id"`
	CreatedAt    time.Time         `json:"created_at,omitempty"`
	LastMessage  time.Time         `json:"last_message"`
	MessageCount int               `json:"message_count"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// ChatMessage represents a chat message from a TUI/SSH client
type ChatMessage struct {
	BaseMessage
	SessionKey string `json:"session_key"`
	UserID     string `json:"user_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"` // For correlating stream responses
	Text       string `json:"text"`
}

// CommandMessage represents a slash command from a TUI/SSH client
type CommandMessage struct {
	BaseMessage
	SessionKey string `json:"session_key"`
	Command    string `json:"command"`
	Args       string `json:"args,omitempty"`
}

// StreamStart signals the beginning of a streaming AI response
type StreamStart struct {
	BaseMessage
	SessionKey string `json:"session_key"`
	RequestID  string `json:"request_id"`
}

// StreamDelta delivers a text chunk during streaming
type StreamDelta struct {
	BaseMessage
	SessionKey string `json:"session_key"`
	RequestID  string `json:"request_id"`
	Delta      string `json:"delta"`
}

// StreamEnd signals the completion of a streaming response
type StreamEnd struct {
	BaseMessage
	SessionKey       string  `json:"session_key"`
	RequestID        string  `json:"request_id"`
	Content          string  `json:"content"`                     // final complete content
	PromptTokens     int     `json:"prompt_tokens,omitempty"`     // tokens used for prompt/context
	CompletionTokens int     `json:"completion_tokens,omitempty"` // tokens used for completion
	TotalTokens      int     `json:"total_tokens,omitempty"`      // total tokens used
	Model            string  `json:"model,omitempty"`             // model that generated the response
	RequestCost      float64 `json:"request_cost,omitempty"`      // cost of this request in USD
	SessionCost      float64 `json:"session_cost,omitempty"`      // cumulative session cost in USD
}

// ToolEvent represents a tool execution lifecycle event
type ToolEvent struct {
	BaseMessage
	SessionKey string        `json:"session_key"`
	RequestID  string        `json:"request_id"`
	ToolName   string        `json:"tool_name"`
	EventType  string        `json:"event_type"` // "start", "complete", "error"
	Args       string        `json:"args,omitempty"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration,omitempty"`
}

// SessionSwitch represents a session management request/response
type SessionSwitch struct {
	BaseMessage
	SessionKey string        `json:"session_key,omitempty"`
	UserID     string        `json:"user_id,omitempty"`
	Action     string        `json:"action"`               // "create", "switch", "list"
	RequestID  string        `json:"request_id,omitempty"` // For correlating responses with specific tabs
	Model      string        `json:"model,omitempty"`
	CreatedAt  time.Time     `json:"created_at,omitempty"`
	Sessions   []SessionInfo `json:"sessions,omitempty"`
	History    []MessageInfo `json:"history,omitempty"`
}

// MessageInfo is a lightweight message representation for session history
type MessageInfo struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// CommandResponse delivers the result of a slash command
type CommandResponse struct {
	BaseMessage
	SessionKey string `json:"session_key"`
	Command    string `json:"command"`
	Response   string `json:"response"`
	Model      string `json:"model,omitempty"`
}

// ErrorResponse delivers an error notification to the client
type ErrorResponse struct {
	BaseMessage
	SessionKey string `json:"session_key,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

// GatewayInfo delivers server metadata to the client on connect
type GatewayInfo struct {
	BaseMessage
	AssistantName string            `json:"assistant_name"`
	Version       string            `json:"version,omitempty"`
	GitCommit     string            `json:"git_commit,omitempty"`
	UptimeSeconds int64             `json:"uptime_seconds,omitempty"`
	ModelAliases  map[string]string `json:"model_aliases,omitempty"`
	ToolCount     int               `json:"tool_count,omitempty"`
	SkillCount    int               `json:"skill_count,omitempty"`
}

// ParseMessage parses a JSON message into the appropriate struct
func ParseMessage(data []byte) (interface{}, error) {
	var base BaseMessage
	if err := json.Unmarshal(data, &base); err != nil {
		return nil, err
	}

	switch base.Type {
	case TypeIncomingMessage:
		var msg IncomingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeOutgoingMessage:
		var msg OutgoingMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeChannelStatus:
		var msg ChannelStatus
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeChannelCommand:
		var msg ChannelCommand
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeAgentRequest:
		var msg AgentRequest
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeAgentResponse:
		var msg AgentResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeHealthCheck:
		var msg HealthCheck
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeSessionList:
		var msg SessionList
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeChatMessage:
		var msg ChatMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeCommandMessage:
		var msg CommandMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeStreamStart:
		var msg StreamStart
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeStreamDelta:
		var msg StreamDelta
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeStreamEnd:
		var msg StreamEnd
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeToolEvent:
		var msg ToolEvent
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeSessionSwitch:
		var msg SessionSwitch
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeCommandResponse:
		var msg CommandResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeErrorResponse:
		var msg ErrorResponse
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	case TypeGatewayInfo:
		var msg GatewayInfo
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return &msg, nil

	default:
		return &base, nil
	}
}
