package protocol

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants
func TestMessageType_Constants(t *testing.T) {
	// Verify all message type constants have expected values
	assert.Equal(t, MessageType("incoming_message"), TypeIncomingMessage)
	assert.Equal(t, MessageType("channel_status"), TypeChannelStatus)
	assert.Equal(t, MessageType("outgoing_message"), TypeOutgoingMessage)
	assert.Equal(t, MessageType("channel_command"), TypeChannelCommand)
	assert.Equal(t, MessageType("agent_request"), TypeAgentRequest)
	assert.Equal(t, MessageType("agent_response"), TypeAgentResponse)
	assert.Equal(t, MessageType("health_check"), TypeHealthCheck)
	assert.Equal(t, MessageType("session_list"), TypeSessionList)
	assert.Equal(t, MessageType("chat_message"), TypeChatMessage)
	assert.Equal(t, MessageType("command_message"), TypeCommandMessage)
	assert.Equal(t, MessageType("stream_start"), TypeStreamStart)
	assert.Equal(t, MessageType("stream_delta"), TypeStreamDelta)
	assert.Equal(t, MessageType("stream_end"), TypeStreamEnd)
	assert.Equal(t, MessageType("tool_event"), TypeToolEvent)
	assert.Equal(t, MessageType("session_switch"), TypeSessionSwitch)
	assert.Equal(t, MessageType("command_response"), TypeCommandResponse)
	assert.Equal(t, MessageType("error_response"), TypeErrorResponse)
	assert.Equal(t, MessageType("gateway_info"), TypeGatewayInfo)
}

// Test BaseMessage JSON serialization
func TestBaseMessage_JSON(t *testing.T) {
	ts := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	msg := BaseMessage{
		Type:      TypeChatMessage,
		ID:        "msg-123",
		Timestamp: ts,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded BaseMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Type, decoded.Type)
	assert.Equal(t, msg.ID, decoded.ID)
	assert.True(t, msg.Timestamp.Equal(decoded.Timestamp))
}

// Test IncomingMessage
func TestIncomingMessage_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	msg := IncomingMessage{
		BaseMessage: BaseMessage{
			Type:      TypeIncomingMessage,
			ID:        "inc-001",
			Timestamp: ts,
		},
		ChannelID:  "telegram",
		SessionKey: "session-abc",
		UserID:     "user-123",
		Text:       "Hello, world!",
		Metadata:   map[string]string{"source": "mobile"},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded IncomingMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.ChannelID, decoded.ChannelID)
	assert.Equal(t, msg.SessionKey, decoded.SessionKey)
	assert.Equal(t, msg.UserID, decoded.UserID)
	assert.Equal(t, msg.Text, decoded.Text)
	assert.Equal(t, msg.Metadata, decoded.Metadata)
}

func TestIncomingMessage_OmitEmptyMetadata(t *testing.T) {
	msg := IncomingMessage{
		BaseMessage: BaseMessage{Type: TypeIncomingMessage},
		ChannelID:   "ws",
		Text:        "test",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// metadata should be omitted when nil
	assert.NotContains(t, string(data), "metadata")
}

// Test OutgoingMessage
func TestOutgoingMessage_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	msg := OutgoingMessage{
		BaseMessage: BaseMessage{
			Type:      TypeOutgoingMessage,
			ID:        "out-001",
			Timestamp: ts,
		},
		ChannelID:  "telegram",
		SessionKey: "session-abc",
		UserID:     "user-123",
		Text:       "Response text",
		MediaPath:  "/tmp/image.png",
		MediaType:  "image/png",
		Metadata:   map[string]string{"format": "markdown"},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded OutgoingMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.ChannelID, decoded.ChannelID)
	assert.Equal(t, msg.MediaPath, decoded.MediaPath)
	assert.Equal(t, msg.MediaType, decoded.MediaType)
}

func TestOutgoingMessage_OmitEmptyOptionalFields(t *testing.T) {
	msg := OutgoingMessage{
		BaseMessage: BaseMessage{Type: TypeOutgoingMessage},
		ChannelID:   "ws",
		Text:        "test",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	s := string(data)
	assert.NotContains(t, s, "media_path")
	assert.NotContains(t, s, "media_type")
	assert.NotContains(t, s, "metadata")
}

// Test ChannelStatus
func TestChannelStatus_JSON(t *testing.T) {
	msg := ChannelStatus{
		BaseMessage: BaseMessage{Type: TypeChannelStatus, ID: "status-001"},
		ChannelID:   "telegram",
		Status:      "online",
		Details:     map[string]interface{}{"connected_users": float64(42)},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ChannelStatus
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.ChannelID, decoded.ChannelID)
	assert.Equal(t, msg.Status, decoded.Status)
	assert.Equal(t, float64(42), decoded.Details["connected_users"])
}

// Test ChannelCommand
func TestChannelCommand_JSON(t *testing.T) {
	msg := ChannelCommand{
		BaseMessage: BaseMessage{Type: TypeChannelCommand, ID: "cmd-001"},
		ChannelID:   "telegram",
		Command:     "send_typing",
		Args:        map[string]interface{}{"duration": float64(5)},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ChannelCommand
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Command, decoded.Command)
	assert.Equal(t, float64(5), decoded.Args["duration"])
}

// Test AgentRequest
func TestAgentRequest_JSON(t *testing.T) {
	msg := AgentRequest{
		BaseMessage: BaseMessage{Type: TypeAgentRequest, ID: "req-001"},
		SessionKey:  "session-123",
		Message:     "What is 2+2?",
		Model:       "claude-sonnet-4",
		Tools:       []string{"calculator", "web_search"},
		Context:     map[string]string{"timezone": "UTC"},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded AgentRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.SessionKey, decoded.SessionKey)
	assert.Equal(t, msg.Message, decoded.Message)
	assert.Equal(t, msg.Model, decoded.Model)
	assert.Equal(t, msg.Tools, decoded.Tools)
	assert.Equal(t, msg.Context, decoded.Context)
}

// Test AgentResponse
func TestAgentResponse_JSON(t *testing.T) {
	msg := AgentResponse{
		BaseMessage: BaseMessage{Type: TypeAgentResponse, ID: "resp-001"},
		SessionKey:  "session-123",
		RequestID:   "req-001",
		Response:    "2+2 equals 4",
		ToolCalls: []ToolCall{
			{
				ID:     "tool-001",
				Name:   "calculator",
				Args:   map[string]interface{}{"expression": "2+2"},
				Result: "4",
			},
		},
		Metadata: map[string]string{"model_version": "1.0"},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded AgentResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Response, decoded.Response)
	require.Len(t, decoded.ToolCalls, 1)
	assert.Equal(t, "calculator", decoded.ToolCalls[0].Name)
	assert.Equal(t, "4", decoded.ToolCalls[0].Result)
}

// Test ToolCall
func TestToolCall_JSON(t *testing.T) {
	tc := ToolCall{
		ID:     "tc-001",
		Name:   "web_search",
		Args:   map[string]interface{}{"query": "golang testing"},
		Result: "Found 10 results",
		Error:  "",
	}

	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded ToolCall
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, tc.ID, decoded.ID)
	assert.Equal(t, tc.Name, decoded.Name)
	assert.Equal(t, "golang testing", decoded.Args["query"])
}

func TestToolCall_WithError(t *testing.T) {
	tc := ToolCall{
		ID:    "tc-002",
		Name:  "failing_tool",
		Args:  map[string]interface{}{},
		Error: "tool execution failed",
	}

	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded ToolCall
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "tool execution failed", decoded.Error)
	assert.Empty(t, decoded.Result)
}

// Test HealthCheck
func TestHealthCheck_JSON(t *testing.T) {
	msg := HealthCheck{
		BaseMessage: BaseMessage{Type: TypeHealthCheck, ID: "health-001"},
		Status:      "healthy",
		Services: map[string]interface{}{
			"database": "connected",
			"ai":       "ready",
		},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded HealthCheck
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "healthy", decoded.Status)
	assert.Equal(t, "connected", decoded.Services["database"])
}

// Test SessionList
func TestSessionList_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	msg := SessionList{
		BaseMessage: BaseMessage{Type: TypeSessionList, ID: "list-001"},
		Sessions: []SessionInfo{
			{
				Key:          "session-1",
				UserID:       "user-1",
				ChannelID:    "telegram",
				CreatedAt:    ts,
				LastMessage:  ts,
				MessageCount: 10,
				Metadata:     map[string]string{"name": "Main Chat"},
			},
			{
				Key:          "session-2",
				UserID:       "user-2",
				ChannelID:    "ws",
				LastMessage:  ts,
				MessageCount: 5,
			},
		},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded SessionList
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.Len(t, decoded.Sessions, 2)
	assert.Equal(t, "session-1", decoded.Sessions[0].Key)
	assert.Equal(t, 10, decoded.Sessions[0].MessageCount)
}

// Test SessionInfo
func TestSessionInfo_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	info := SessionInfo{
		Key:          "sess-001",
		UserID:       "user-123",
		ChannelID:    "telegram",
		CreatedAt:    ts,
		LastMessage:  ts,
		MessageCount: 42,
		Metadata:     map[string]string{"theme": "dark"},
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded SessionInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.Key, decoded.Key)
	assert.Equal(t, info.MessageCount, decoded.MessageCount)
}

// Test ChatMessage
func TestChatMessage_JSON(t *testing.T) {
	msg := ChatMessage{
		BaseMessage: BaseMessage{Type: TypeChatMessage, ID: "chat-001"},
		SessionKey:  "session-123",
		UserID:      "user-456",
		RequestID:   "req-789",
		Text:        "Hello from TUI",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ChatMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.SessionKey, decoded.SessionKey)
	assert.Equal(t, msg.UserID, decoded.UserID)
	assert.Equal(t, msg.RequestID, decoded.RequestID)
	assert.Equal(t, msg.Text, decoded.Text)
}

// Test CommandMessage
func TestCommandMessage_JSON(t *testing.T) {
	msg := CommandMessage{
		BaseMessage: BaseMessage{Type: TypeCommandMessage, ID: "cmd-001"},
		SessionKey:  "session-123",
		Command:     "help",
		Args:        "tools",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded CommandMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "help", decoded.Command)
	assert.Equal(t, "tools", decoded.Args)
}

// Test StreamStart
func TestStreamStart_JSON(t *testing.T) {
	msg := StreamStart{
		BaseMessage: BaseMessage{Type: TypeStreamStart, ID: "stream-001"},
		SessionKey:  "session-123",
		RequestID:   "req-456",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded StreamStart
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.SessionKey, decoded.SessionKey)
	assert.Equal(t, msg.RequestID, decoded.RequestID)
}

// Test StreamDelta
func TestStreamDelta_JSON(t *testing.T) {
	msg := StreamDelta{
		BaseMessage: BaseMessage{Type: TypeStreamDelta, ID: "delta-001"},
		SessionKey:  "session-123",
		RequestID:   "req-456",
		Delta:       "Here is some ",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded StreamDelta
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Here is some ", decoded.Delta)
}

// Test StreamEnd
func TestStreamEnd_JSON(t *testing.T) {
	msg := StreamEnd{
		BaseMessage:      BaseMessage{Type: TypeStreamEnd, ID: "end-001"},
		SessionKey:       "session-123",
		RequestID:        "req-456",
		Content:          "Here is the complete response.",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Model:            "claude-sonnet-4",
		ContextWindow:    200000,
		RequestCost:      0.0015,
		SessionCost:      0.025,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded StreamEnd
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Content, decoded.Content)
	assert.Equal(t, 100, decoded.PromptTokens)
	assert.Equal(t, 50, decoded.CompletionTokens)
	assert.Equal(t, 150, decoded.TotalTokens)
	assert.Equal(t, "claude-sonnet-4", decoded.Model)
	assert.Equal(t, 200000, decoded.ContextWindow)
	assert.InDelta(t, 0.0015, decoded.RequestCost, 0.0001)
	assert.InDelta(t, 0.025, decoded.SessionCost, 0.0001)
}

func TestStreamEnd_OmitEmptyOptionalFields(t *testing.T) {
	msg := StreamEnd{
		BaseMessage: BaseMessage{Type: TypeStreamEnd},
		SessionKey:  "session-123",
		RequestID:   "req-456",
		Content:     "Response",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	s := string(data)
	assert.NotContains(t, s, "prompt_tokens")
	assert.NotContains(t, s, "completion_tokens")
	assert.NotContains(t, s, "total_tokens")
	assert.NotContains(t, s, "model")
	assert.NotContains(t, s, "context_window")
	assert.NotContains(t, s, "request_cost")
	assert.NotContains(t, s, "session_cost")
}

// Test ToolEvent
func TestToolEvent_JSON(t *testing.T) {
	msg := ToolEvent{
		BaseMessage: BaseMessage{Type: TypeToolEvent, ID: "tool-001"},
		SessionKey:  "session-123",
		RequestID:   "req-456",
		ToolName:    "web_search",
		EventType:   "complete",
		Args:        `{"query": "golang"}`,
		Result:      "Found results",
		Duration:    500 * time.Millisecond,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ToolEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "web_search", decoded.ToolName)
	assert.Equal(t, "complete", decoded.EventType)
	assert.Equal(t, 500*time.Millisecond, decoded.Duration)
}

func TestToolEvent_WithError(t *testing.T) {
	msg := ToolEvent{
		BaseMessage: BaseMessage{Type: TypeToolEvent, ID: "tool-002"},
		SessionKey:  "session-123",
		RequestID:   "req-456",
		ToolName:    "failing_tool",
		EventType:   "error",
		Error:       "execution timeout",
		Duration:    30 * time.Second,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ToolEvent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "error", decoded.EventType)
	assert.Equal(t, "execution timeout", decoded.Error)
}

// Test SessionSwitch
func TestSessionSwitch_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	msg := SessionSwitch{
		BaseMessage: BaseMessage{Type: TypeSessionSwitch, ID: "switch-001"},
		SessionKey:  "session-new",
		UserID:      "user-123",
		Action:      "create",
		RequestID:   "req-456",
		Model:       "claude-sonnet-4",
		CreatedAt:   ts,
		Sessions: []SessionInfo{
			{Key: "session-1", MessageCount: 10},
		},
		History: []MessageInfo{
			{Role: "user", Content: "Hello", Timestamp: ts},
		},
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded SessionSwitch
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "create", decoded.Action)
	assert.Equal(t, "session-new", decoded.SessionKey)
	require.Len(t, decoded.Sessions, 1)
	require.Len(t, decoded.History, 1)
	assert.Equal(t, "user", decoded.History[0].Role)
}

// Test MessageInfo
func TestMessageInfo_JSON(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)
	info := MessageInfo{
		Role:      "assistant",
		Content:   "Hello, how can I help?",
		Timestamp: ts,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded MessageInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "assistant", decoded.Role)
	assert.Equal(t, "Hello, how can I help?", decoded.Content)
}

// Test CommandResponse
func TestCommandResponse_JSON(t *testing.T) {
	msg := CommandResponse{
		BaseMessage: BaseMessage{Type: TypeCommandResponse, ID: "cmdresp-001"},
		SessionKey:  "session-123",
		Command:     "help",
		Response:    "Available commands: /help, /clear, /model",
		Model:       "claude-sonnet-4",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded CommandResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "help", decoded.Command)
	assert.Contains(t, decoded.Response, "Available commands")
}

// Test ErrorResponse
func TestErrorResponse_JSON(t *testing.T) {
	msg := ErrorResponse{
		BaseMessage: BaseMessage{Type: TypeErrorResponse, ID: "err-001"},
		SessionKey:  "session-123",
		Code:        "RATE_LIMIT",
		Message:     "Too many requests",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ErrorResponse
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "RATE_LIMIT", decoded.Code)
	assert.Equal(t, "Too many requests", decoded.Message)
}

func TestErrorResponse_OmitEmptySessionKey(t *testing.T) {
	msg := ErrorResponse{
		BaseMessage: BaseMessage{Type: TypeErrorResponse},
		Code:        "INTERNAL_ERROR",
		Message:     "Something went wrong",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	assert.NotContains(t, string(data), "session_key")
}

// Test GatewayInfo
func TestGatewayInfo_JSON(t *testing.T) {
	msg := GatewayInfo{
		BaseMessage:   BaseMessage{Type: TypeGatewayInfo, ID: "info-001"},
		AssistantName: "Conduit",
		Version:       "1.2.3",
		GitCommit:     "abc123",
		UptimeSeconds: 3600,
		ModelAliases: map[string]string{
			"default": "claude-sonnet-4",
			"fast":    "claude-haiku",
		},
		ToolCount:  15,
		SkillCount: 5,
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded GatewayInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, "Conduit", decoded.AssistantName)
	assert.Equal(t, "1.2.3", decoded.Version)
	assert.Equal(t, "abc123", decoded.GitCommit)
	assert.Equal(t, int64(3600), decoded.UptimeSeconds)
	assert.Equal(t, "claude-sonnet-4", decoded.ModelAliases["default"])
	assert.Equal(t, 15, decoded.ToolCount)
	assert.Equal(t, 5, decoded.SkillCount)
}

// Test ParseMessage function
func TestParseMessage_IncomingMessage(t *testing.T) {
	input := `{"type":"incoming_message","id":"inc-001","channel_id":"telegram","text":"hello"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*IncomingMessage)
	require.True(t, ok, "expected *IncomingMessage, got %T", result)
	assert.Equal(t, "inc-001", msg.ID)
	assert.Equal(t, "telegram", msg.ChannelID)
	assert.Equal(t, "hello", msg.Text)
}

func TestParseMessage_OutgoingMessage(t *testing.T) {
	input := `{"type":"outgoing_message","id":"out-001","channel_id":"ws","text":"response"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*OutgoingMessage)
	require.True(t, ok, "expected *OutgoingMessage, got %T", result)
	assert.Equal(t, "response", msg.Text)
}

func TestParseMessage_ChannelStatus(t *testing.T) {
	input := `{"type":"channel_status","id":"status-001","channel_id":"telegram","status":"online"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*ChannelStatus)
	require.True(t, ok, "expected *ChannelStatus, got %T", result)
	assert.Equal(t, "online", msg.Status)
}

func TestParseMessage_ChannelCommand(t *testing.T) {
	input := `{"type":"channel_command","id":"cmd-001","channel_id":"telegram","command":"typing"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*ChannelCommand)
	require.True(t, ok, "expected *ChannelCommand, got %T", result)
	assert.Equal(t, "typing", msg.Command)
}

func TestParseMessage_AgentRequest(t *testing.T) {
	input := `{"type":"agent_request","id":"req-001","session_key":"sess-123","message":"What is 2+2?"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*AgentRequest)
	require.True(t, ok, "expected *AgentRequest, got %T", result)
	assert.Equal(t, "What is 2+2?", msg.Message)
}

func TestParseMessage_AgentResponse(t *testing.T) {
	input := `{"type":"agent_response","id":"resp-001","session_key":"sess-123","response":"4"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*AgentResponse)
	require.True(t, ok, "expected *AgentResponse, got %T", result)
	assert.Equal(t, "4", msg.Response)
}

func TestParseMessage_HealthCheck(t *testing.T) {
	input := `{"type":"health_check","id":"health-001","status":"healthy"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*HealthCheck)
	require.True(t, ok, "expected *HealthCheck, got %T", result)
	assert.Equal(t, "healthy", msg.Status)
}

func TestParseMessage_SessionList(t *testing.T) {
	input := `{"type":"session_list","id":"list-001","sessions":[{"key":"sess-1","message_count":5}]}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*SessionList)
	require.True(t, ok, "expected *SessionList, got %T", result)
	require.Len(t, msg.Sessions, 1)
	assert.Equal(t, "sess-1", msg.Sessions[0].Key)
}

func TestParseMessage_ChatMessage(t *testing.T) {
	input := `{"type":"chat_message","id":"chat-001","session_key":"sess-123","text":"Hello"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*ChatMessage)
	require.True(t, ok, "expected *ChatMessage, got %T", result)
	assert.Equal(t, "Hello", msg.Text)
}

func TestParseMessage_CommandMessage(t *testing.T) {
	input := `{"type":"command_message","id":"cmd-001","session_key":"sess-123","command":"help"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*CommandMessage)
	require.True(t, ok, "expected *CommandMessage, got %T", result)
	assert.Equal(t, "help", msg.Command)
}

func TestParseMessage_StreamStart(t *testing.T) {
	input := `{"type":"stream_start","id":"stream-001","session_key":"sess-123","request_id":"req-456"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*StreamStart)
	require.True(t, ok, "expected *StreamStart, got %T", result)
	assert.Equal(t, "req-456", msg.RequestID)
}

func TestParseMessage_StreamDelta(t *testing.T) {
	input := `{"type":"stream_delta","id":"delta-001","session_key":"sess-123","delta":"chunk"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*StreamDelta)
	require.True(t, ok, "expected *StreamDelta, got %T", result)
	assert.Equal(t, "chunk", msg.Delta)
}

func TestParseMessage_StreamEnd(t *testing.T) {
	input := `{"type":"stream_end","id":"end-001","session_key":"sess-123","content":"final","total_tokens":100}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*StreamEnd)
	require.True(t, ok, "expected *StreamEnd, got %T", result)
	assert.Equal(t, "final", msg.Content)
	assert.Equal(t, 100, msg.TotalTokens)
}

func TestParseMessage_ToolEvent(t *testing.T) {
	input := `{"type":"tool_event","id":"tool-001","tool_name":"web_search","event_type":"start"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*ToolEvent)
	require.True(t, ok, "expected *ToolEvent, got %T", result)
	assert.Equal(t, "web_search", msg.ToolName)
	assert.Equal(t, "start", msg.EventType)
}

func TestParseMessage_SessionSwitch(t *testing.T) {
	input := `{"type":"session_switch","id":"switch-001","session_key":"sess-new","action":"create"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*SessionSwitch)
	require.True(t, ok, "expected *SessionSwitch, got %T", result)
	assert.Equal(t, "create", msg.Action)
}

func TestParseMessage_CommandResponse(t *testing.T) {
	input := `{"type":"command_response","id":"cmdresp-001","command":"help","response":"Commands: /help"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*CommandResponse)
	require.True(t, ok, "expected *CommandResponse, got %T", result)
	assert.Equal(t, "help", msg.Command)
}

func TestParseMessage_ErrorResponse(t *testing.T) {
	input := `{"type":"error_response","id":"err-001","code":"INVALID","message":"Bad request"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*ErrorResponse)
	require.True(t, ok, "expected *ErrorResponse, got %T", result)
	assert.Equal(t, "INVALID", msg.Code)
	assert.Equal(t, "Bad request", msg.Message)
}

func TestParseMessage_GatewayInfo(t *testing.T) {
	input := `{"type":"gateway_info","id":"info-001","assistant_name":"Conduit","version":"1.0.0"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	msg, ok := result.(*GatewayInfo)
	require.True(t, ok, "expected *GatewayInfo, got %T", result)
	assert.Equal(t, "Conduit", msg.AssistantName)
	assert.Equal(t, "1.0.0", msg.Version)
}

func TestParseMessage_UnknownType(t *testing.T) {
	input := `{"type":"unknown_type","id":"unknown-001","custom_field":"value"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	// Unknown types should return BaseMessage
	msg, ok := result.(*BaseMessage)
	require.True(t, ok, "expected *BaseMessage for unknown type, got %T", result)
	assert.Equal(t, MessageType("unknown_type"), msg.Type)
	assert.Equal(t, "unknown-001", msg.ID)
}

func TestParseMessage_InvalidJSON(t *testing.T) {
	input := `{invalid json}`

	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_EmptyInput(t *testing.T) {
	_, err := ParseMessage([]byte(""))
	require.Error(t, err)
}

func TestParseMessage_NullInput(t *testing.T) {
	_, err := ParseMessage([]byte("null"))
	require.NoError(t, err) // null is valid JSON, parses to empty BaseMessage
}

func TestParseMessage_MalformedTypeField(t *testing.T) {
	// type field is not a string
	input := `{"type":123,"id":"test"}`

	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_MissingTypeField(t *testing.T) {
	input := `{"id":"test","content":"hello"}`

	result, err := ParseMessage([]byte(input))
	require.NoError(t, err)

	// Should parse as BaseMessage with empty type
	msg, ok := result.(*BaseMessage)
	require.True(t, ok)
	assert.Equal(t, MessageType(""), msg.Type)
}

// Test ParseMessage with invalid nested data for specific types
func TestParseMessage_InvalidIncomingMessage(t *testing.T) {
	// channel_id should be string, not number
	input := `{"type":"incoming_message","channel_id":123}`

	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidSessionList(t *testing.T) {
	// sessions should be array, not object
	input := `{"type":"session_list","sessions":{"invalid":"data"}}`

	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

// Test invalid data for each message type to cover all error branches in ParseMessage
func TestParseMessage_InvalidOutgoingMessage(t *testing.T) {
	input := `{"type":"outgoing_message","channel_id":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidChannelStatus(t *testing.T) {
	input := `{"type":"channel_status","status":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidChannelCommand(t *testing.T) {
	input := `{"type":"channel_command","command":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidAgentRequest(t *testing.T) {
	input := `{"type":"agent_request","message":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidAgentResponse(t *testing.T) {
	input := `{"type":"agent_response","response":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidHealthCheck(t *testing.T) {
	input := `{"type":"health_check","status":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidChatMessage(t *testing.T) {
	input := `{"type":"chat_message","text":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidCommandMessage(t *testing.T) {
	input := `{"type":"command_message","command":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidStreamStart(t *testing.T) {
	input := `{"type":"stream_start","session_key":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidStreamDelta(t *testing.T) {
	input := `{"type":"stream_delta","delta":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidStreamEnd(t *testing.T) {
	input := `{"type":"stream_end","content":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidToolEvent(t *testing.T) {
	input := `{"type":"tool_event","tool_name":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidSessionSwitch(t *testing.T) {
	input := `{"type":"session_switch","action":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidCommandResponse(t *testing.T) {
	input := `{"type":"command_response","command":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidErrorResponse(t *testing.T) {
	input := `{"type":"error_response","code":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

func TestParseMessage_InvalidGatewayInfo(t *testing.T) {
	input := `{"type":"gateway_info","assistant_name":123}`
	_, err := ParseMessage([]byte(input))
	require.Error(t, err)
}

// Test roundtrip serialization for all message types
func TestRoundtrip_AllMessageTypes(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name    string
		message interface{}
		msgType MessageType
	}{
		{
			name: "IncomingMessage",
			message: &IncomingMessage{
				BaseMessage: BaseMessage{Type: TypeIncomingMessage, ID: "rt-1", Timestamp: ts},
				ChannelID:   "telegram",
				Text:        "test",
			},
			msgType: TypeIncomingMessage,
		},
		{
			name: "OutgoingMessage",
			message: &OutgoingMessage{
				BaseMessage: BaseMessage{Type: TypeOutgoingMessage, ID: "rt-2", Timestamp: ts},
				ChannelID:   "ws",
				Text:        "response",
			},
			msgType: TypeOutgoingMessage,
		},
		{
			name: "ChannelStatus",
			message: &ChannelStatus{
				BaseMessage: BaseMessage{Type: TypeChannelStatus, ID: "rt-3", Timestamp: ts},
				ChannelID:   "telegram",
				Status:      "online",
			},
			msgType: TypeChannelStatus,
		},
		{
			name: "ChannelCommand",
			message: &ChannelCommand{
				BaseMessage: BaseMessage{Type: TypeChannelCommand, ID: "rt-4", Timestamp: ts},
				ChannelID:   "telegram",
				Command:     "typing",
			},
			msgType: TypeChannelCommand,
		},
		{
			name: "AgentRequest",
			message: &AgentRequest{
				BaseMessage: BaseMessage{Type: TypeAgentRequest, ID: "rt-5", Timestamp: ts},
				SessionKey:  "sess-123",
				Message:     "hello",
			},
			msgType: TypeAgentRequest,
		},
		{
			name: "AgentResponse",
			message: &AgentResponse{
				BaseMessage: BaseMessage{Type: TypeAgentResponse, ID: "rt-6", Timestamp: ts},
				SessionKey:  "sess-123",
				Response:    "hi there",
			},
			msgType: TypeAgentResponse,
		},
		{
			name: "HealthCheck",
			message: &HealthCheck{
				BaseMessage: BaseMessage{Type: TypeHealthCheck, ID: "rt-7", Timestamp: ts},
				Status:      "healthy",
			},
			msgType: TypeHealthCheck,
		},
		{
			name: "SessionList",
			message: &SessionList{
				BaseMessage: BaseMessage{Type: TypeSessionList, ID: "rt-8", Timestamp: ts},
				Sessions:    []SessionInfo{{Key: "s1"}},
			},
			msgType: TypeSessionList,
		},
		{
			name: "ChatMessage",
			message: &ChatMessage{
				BaseMessage: BaseMessage{Type: TypeChatMessage, ID: "rt-9", Timestamp: ts},
				SessionKey:  "sess-123",
				Text:        "chat",
			},
			msgType: TypeChatMessage,
		},
		{
			name: "CommandMessage",
			message: &CommandMessage{
				BaseMessage: BaseMessage{Type: TypeCommandMessage, ID: "rt-10", Timestamp: ts},
				SessionKey:  "sess-123",
				Command:     "help",
			},
			msgType: TypeCommandMessage,
		},
		{
			name: "StreamStart",
			message: &StreamStart{
				BaseMessage: BaseMessage{Type: TypeStreamStart, ID: "rt-11", Timestamp: ts},
				SessionKey:  "sess-123",
				RequestID:   "req-1",
			},
			msgType: TypeStreamStart,
		},
		{
			name: "StreamDelta",
			message: &StreamDelta{
				BaseMessage: BaseMessage{Type: TypeStreamDelta, ID: "rt-12", Timestamp: ts},
				SessionKey:  "sess-123",
				Delta:       "text",
			},
			msgType: TypeStreamDelta,
		},
		{
			name: "StreamEnd",
			message: &StreamEnd{
				BaseMessage: BaseMessage{Type: TypeStreamEnd, ID: "rt-13", Timestamp: ts},
				SessionKey:  "sess-123",
				Content:     "complete",
			},
			msgType: TypeStreamEnd,
		},
		{
			name: "ToolEvent",
			message: &ToolEvent{
				BaseMessage: BaseMessage{Type: TypeToolEvent, ID: "rt-14", Timestamp: ts},
				SessionKey:  "sess-123",
				ToolName:    "search",
				EventType:   "start",
			},
			msgType: TypeToolEvent,
		},
		{
			name: "SessionSwitch",
			message: &SessionSwitch{
				BaseMessage: BaseMessage{Type: TypeSessionSwitch, ID: "rt-15", Timestamp: ts},
				SessionKey:  "sess-new",
				Action:      "create",
			},
			msgType: TypeSessionSwitch,
		},
		{
			name: "CommandResponse",
			message: &CommandResponse{
				BaseMessage: BaseMessage{Type: TypeCommandResponse, ID: "rt-16", Timestamp: ts},
				SessionKey:  "sess-123",
				Command:     "help",
				Response:    "commands",
			},
			msgType: TypeCommandResponse,
		},
		{
			name: "ErrorResponse",
			message: &ErrorResponse{
				BaseMessage: BaseMessage{Type: TypeErrorResponse, ID: "rt-17", Timestamp: ts},
				Code:        "ERROR",
				Message:     "failed",
			},
			msgType: TypeErrorResponse,
		},
		{
			name: "GatewayInfo",
			message: &GatewayInfo{
				BaseMessage:   BaseMessage{Type: TypeGatewayInfo, ID: "rt-18", Timestamp: ts},
				AssistantName: "Conduit",
			},
			msgType: TypeGatewayInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := json.Marshal(tt.message)
			require.NoError(t, err)

			// Parse
			result, err := ParseMessage(data)
			require.NoError(t, err)

			// Re-marshal
			data2, err := json.Marshal(result)
			require.NoError(t, err)

			// Verify same JSON (normalized)
			var m1, m2 map[string]interface{}
			require.NoError(t, json.Unmarshal(data, &m1))
			require.NoError(t, json.Unmarshal(data2, &m2))
			assert.Equal(t, m1, m2, "roundtrip should produce identical JSON")
		})
	}
}

// Test edge cases with special characters and unicode
func TestChatMessage_Unicode(t *testing.T) {
	msg := ChatMessage{
		BaseMessage: BaseMessage{Type: TypeChatMessage},
		Text:        "Hello, \u4e16\u754c! \U0001F600 emoji test",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ChatMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Text, decoded.Text)
}

func TestChatMessage_SpecialCharacters(t *testing.T) {
	msg := ChatMessage{
		BaseMessage: BaseMessage{Type: TypeChatMessage},
		Text:        "Line1\nLine2\tTabbed\r\nCRLF \"quoted\" 'single' \\backslash",
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded ChatMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, msg.Text, decoded.Text)
}

// Test zero-value structs
func TestZeroValue_BaseMessage(t *testing.T) {
	var msg BaseMessage
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded BaseMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, MessageType(""), decoded.Type)
	assert.Equal(t, "", decoded.ID)
	assert.True(t, decoded.Timestamp.IsZero())
}

func TestZeroValue_ToolCall(t *testing.T) {
	var tc ToolCall
	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded ToolCall
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Empty(t, decoded.ID)
	assert.Empty(t, decoded.Name)
	assert.Nil(t, decoded.Args)
}

// Benchmark ParseMessage
func BenchmarkParseMessage(b *testing.B) {
	input := []byte(`{"type":"chat_message","id":"bench-001","session_key":"sess-123","text":"Hello, world!"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseMessage(input)
	}
}

func BenchmarkParseMessage_Complex(b *testing.B) {
	input := []byte(`{"type":"agent_response","id":"bench-002","session_key":"sess-123","response":"Here is the answer","tool_calls":[{"id":"tc-1","name":"web_search","args":{"query":"test"},"result":"found"}],"metadata":{"model":"claude"}}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseMessage(input)
	}
}
