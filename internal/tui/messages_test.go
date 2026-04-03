package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"conduit/pkg/protocol"
)

func TestConnectedMsg(t *testing.T) {
	msg := ConnectedMsg{}
	// Ensure it can be used as a tea.Msg (interface{})
	var _ interface{} = msg
}

func TestDisconnectedMsg(t *testing.T) {
	err := assert.AnError
	msg := DisconnectedMsg{Err: err}
	assert.Equal(t, err, msg.Err)
}

func TestReconnectingMsg(t *testing.T) {
	msg := ReconnectingMsg{Attempt: 3}
	assert.Equal(t, 3, msg.Attempt)
}

func TestStreamStartMsg(t *testing.T) {
	msg := StreamStartMsg{
		SessionKey: "session-123",
		RequestID:  "req-456",
	}
	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "req-456", msg.RequestID)
}

func TestStreamDeltaMsg(t *testing.T) {
	msg := StreamDeltaMsg{
		SessionKey: "session-123",
		RequestID:  "req-456",
		Delta:      "Hello",
	}
	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "req-456", msg.RequestID)
	assert.Equal(t, "Hello", msg.Delta)
}

func TestStreamEndMsg(t *testing.T) {
	msg := StreamEndMsg{
		SessionKey:       "session-123",
		RequestID:        "req-456",
		Content:          "Hello world",
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
		Model:            "claude-3-opus",
		ContextWindow:    200000,
		RequestCost:      0.001,
		SessionCost:      0.01,
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "req-456", msg.RequestID)
	assert.Equal(t, "Hello world", msg.Content)
	assert.Equal(t, 100, msg.PromptTokens)
	assert.Equal(t, 20, msg.CompletionTokens)
	assert.Equal(t, 120, msg.TotalTokens)
	assert.Equal(t, "claude-3-opus", msg.Model)
	assert.Equal(t, 200000, msg.ContextWindow)
	assert.Equal(t, 0.001, msg.RequestCost)
	assert.Equal(t, 0.01, msg.SessionCost)
}

func TestToolEventMsg(t *testing.T) {
	event := protocol.ToolEvent{
		ToolName:  "search",
		EventType: "start",
		Args:      `{"query": "test"}`,
	}
	msg := ToolEventMsg{ToolEvent: event}

	assert.Equal(t, "search", msg.ToolName)
	assert.Equal(t, "start", msg.EventType)
	assert.Equal(t, `{"query": "test"}`, msg.Args)
}

func TestCommandResponseMsg(t *testing.T) {
	msg := CommandResponseMsg{
		SessionKey: "session-123",
		RequestID:  "req-456",
		Command:    "/model",
		Response:   "Switched to claude-3-opus",
		Model:      "claude-3-opus",
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "req-456", msg.RequestID)
	assert.Equal(t, "/model", msg.Command)
	assert.Equal(t, "Switched to claude-3-opus", msg.Response)
	assert.Equal(t, "claude-3-opus", msg.Model)
}

func TestSessionListMsg(t *testing.T) {
	sessions := []protocol.SessionInfo{
		{Key: "session-1", MessageCount: 10},
		{Key: "session-2", MessageCount: 5},
	}
	msg := SessionListMsg{Sessions: sessions}

	assert.Len(t, msg.Sessions, 2)
	assert.Equal(t, "session-1", msg.Sessions[0].Key)
	assert.Equal(t, 10, msg.Sessions[0].MessageCount)
}

func TestSessionCreatedMsg(t *testing.T) {
	now := time.Now()
	msg := SessionCreatedMsg{
		Key:       "session-new",
		RequestID: "req-123",
		CreatedAt: now,
	}

	assert.Equal(t, "session-new", msg.Key)
	assert.Equal(t, "req-123", msg.RequestID)
	assert.Equal(t, now, msg.CreatedAt)
}

func TestSessionSwitchedMsg(t *testing.T) {
	now := time.Now()
	history := []protocol.MessageInfo{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}
	msg := SessionSwitchedMsg{
		Key:       "session-old",
		History:   history,
		Model:     "claude-3-sonnet",
		CreatedAt: now,
	}

	assert.Equal(t, "session-old", msg.Key)
	assert.Len(t, msg.History, 2)
	assert.Equal(t, "claude-3-sonnet", msg.Model)
	assert.Equal(t, now, msg.CreatedAt)
}

func TestGatewayInfoMsg(t *testing.T) {
	msg := GatewayInfoMsg{
		AssistantName: "Claude",
		Version:       "1.0.0",
		GitCommit:     "abc1234567890",
		UptimeSeconds: 3600,
		ModelAliases: map[string]string{
			"sonnet": "claude-3-sonnet-20240229",
		},
		ToolCount:  15,
		SkillCount: 3,
	}

	assert.Equal(t, "Claude", msg.AssistantName)
	assert.Equal(t, "1.0.0", msg.Version)
	assert.Equal(t, "abc1234567890", msg.GitCommit)
	assert.Equal(t, int64(3600), msg.UptimeSeconds)
	assert.Len(t, msg.ModelAliases, 1)
	assert.Equal(t, 15, msg.ToolCount)
	assert.Equal(t, 3, msg.SkillCount)
}

func TestThinkingTickMsg(t *testing.T) {
	msg := ThinkingTickMsg{}
	// Just ensure it can be created
	var _ interface{} = msg
}

func TestErrorMsg(t *testing.T) {
	msg := ErrorMsg{
		SessionKey: "session-123",
		RequestID:  "req-456",
		Code:       "rate_limit",
		Message:    "Rate limit exceeded",
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "req-456", msg.RequestID)
	assert.Equal(t, "rate_limit", msg.Code)
	assert.Equal(t, "Rate limit exceeded", msg.Message)
}

func TestShellResultMsg(t *testing.T) {
	msg := ShellResultMsg{
		SessionKey: "session-123",
		Output:     "file1.txt\nfile2.txt\n",
		Err:        nil,
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "file1.txt\nfile2.txt\n", msg.Output)
	assert.Nil(t, msg.Err)
}

func TestShellResultMsg_WithError(t *testing.T) {
	err := assert.AnError
	msg := ShellResultMsg{
		SessionKey: "session-123",
		Output:     "",
		Err:        err,
	}

	assert.Equal(t, err, msg.Err)
}

func TestShellResultWithDirMsg(t *testing.T) {
	msg := ShellResultWithDirMsg{
		SessionKey: "session-123",
		Output:     "/home/user",
		Err:        nil,
		NewDir:     "/home/user",
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "/home/user", msg.Output)
	assert.Equal(t, "/home/user", msg.NewDir)
}

func TestShellStreamMsg(t *testing.T) {
	msg := ShellStreamMsg{
		SessionKey: "session-123",
		Line:       "Processing...",
		IsStderr:   false,
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, "Processing...", msg.Line)
	assert.False(t, msg.IsStderr)
}

func TestShellCommandCancelledMsg(t *testing.T) {
	msg := ShellCommandCancelledMsg{
		SessionKey: "session-123",
	}

	assert.Equal(t, "session-123", msg.SessionKey)
}

func TestBackgroundJobStartedMsg(t *testing.T) {
	msg := BackgroundJobStartedMsg{
		SessionKey: "session-123",
		JobID:      1,
		Command:    "sleep 60",
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, 1, msg.JobID)
	assert.Equal(t, "sleep 60", msg.Command)
}

func TestBackgroundJobCompletedMsg(t *testing.T) {
	msg := BackgroundJobCompletedMsg{
		SessionKey: "session-123",
		JobID:      1,
		Status:     JobCompleted,
		Output:     "Job output here",
		Error:      nil,
	}

	assert.Equal(t, "session-123", msg.SessionKey)
	assert.Equal(t, 1, msg.JobID)
	assert.Equal(t, JobCompleted, msg.Status)
	assert.Equal(t, "Job output here", msg.Output)
	assert.Nil(t, msg.Error)
}

func TestBackgroundJobCompletedMsg_WithError(t *testing.T) {
	err := assert.AnError
	msg := BackgroundJobCompletedMsg{
		SessionKey: "session-123",
		JobID:      1,
		Status:     JobFailed,
		Output:     "",
		Error:      err,
	}

	assert.Equal(t, JobFailed, msg.Status)
	assert.Equal(t, err, msg.Error)
}
