package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockGatewayClient is a mock implementation of GatewayClient for testing
type mockGatewayClient struct {
	connected    bool
	sendChatCalls    []string
	commandCalls     []struct{ cmd, args string }
	createSessionCalls int
}

func (m *mockGatewayClient) ConnectCmd() tea.Cmd {
	return func() tea.Msg { return ConnectedMsg{} }
}

func (m *mockGatewayClient) ListenCmd() tea.Cmd {
	return nil
}

func (m *mockGatewayClient) ReconnectCmd(attempt int) tea.Cmd {
	return func() tea.Msg { return ConnectedMsg{} }
}

func (m *mockGatewayClient) IsConnected() bool {
	return m.connected
}

func (m *mockGatewayClient) Close() {
	m.connected = false
}

func (m *mockGatewayClient) SendChat(sessionKey, text string) error {
	m.sendChatCalls = append(m.sendChatCalls, text)
	return nil
}

func (m *mockGatewayClient) SendChatWithID(sessionKey, text, requestID string) error {
	m.sendChatCalls = append(m.sendChatCalls, text)
	return nil
}

func (m *mockGatewayClient) SendCommand(sessionKey, command, args string) error {
	m.commandCalls = append(m.commandCalls, struct{ cmd, args string }{command, args})
	return nil
}

func (m *mockGatewayClient) CreateSession() error {
	m.createSessionCalls++
	return nil
}

func (m *mockGatewayClient) CreateSessionWithID(requestID string) error {
	m.createSessionCalls++
	return nil
}

func (m *mockGatewayClient) SwitchSession(key string) error {
	return nil
}

func (m *mockGatewayClient) ListSessions() error {
	return nil
}

func TestNewModel(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client:        client,
		UserID:        "testuser",
		GatewayURL:    "ws://localhost:18789/ws",
		AssistantName: "Claude",
		ShellSecurity: ShellSecurityConfig{Enabled: true},
	}

	model := NewModel(config)

	assert.Equal(t, "Claude", model.assistantName)
	assert.NotNil(t, model.client)
	assert.Len(t, model.sessions, 1)
	assert.NotNil(t, model.pendingRequests)
	assert.NotNil(t, model.chatRequests)
	assert.False(t, model.connected)
}

func TestNewModel_DefaultAssistantName(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	assert.Equal(t, "Assistant", model.assistantName)
}

func TestModel_Init(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	cmd := model.Init()

	assert.NotNil(t, cmd)
}

func TestModel_ActiveSession(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)

	session := model.activeSession()
	require.NotNil(t, session)
	assert.Equal(t, "Chat 1", session.Label)
}

func TestModel_SessionByKey(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-123"

	// Found
	session := model.sessionByKey("session-123")
	require.NotNil(t, session)
	assert.Equal(t, "session-123", session.Key)

	// Not found
	session = model.sessionByKey("nonexistent")
	assert.Nil(t, session)
}

func TestModel_ResolveTab_ByRequestID(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.chatRequests["req-123"] = 0

	session, idx := model.resolveTab("req-123", "")
	require.NotNil(t, session)
	assert.Equal(t, 0, idx)
}

func TestModel_ResolveTab_BySessionKey(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-456"

	session, idx := model.resolveTab("", "session-456")
	require.NotNil(t, session)
	assert.Equal(t, 0, idx)
}

func TestModel_ResolveTab_Fallback(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)

	// Should fall back to active session
	session, idx := model.resolveTab("", "")
	require.NotNil(t, session)
	assert.Equal(t, 0, idx)
}

func TestModel_Update_WindowSize(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	newModel, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 50})

	m := newModel.(Model)
	assert.Equal(t, 100, m.width)
	assert.Equal(t, 50, m.height)
}

func TestModel_Update_ConnectedMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	newModel, _ := model.Update(ConnectedMsg{})

	m := newModel.(Model)
	assert.True(t, m.connected)
	assert.True(t, m.statusBar.Connected)
	assert.True(t, m.sidebar.Connected)
}

func TestModel_Update_DisconnectedMsg(t *testing.T) {
	client := &mockGatewayClient{connected: false}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.connected = true

	newModel, _ := model.Update(DisconnectedMsg{Err: nil})

	m := newModel.(Model)
	assert.False(t, m.connected)
	assert.False(t, m.statusBar.Connected)
}

func TestModel_Update_StreamStartMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-123"

	newModel, _ := model.Update(StreamStartMsg{
		SessionKey: "session-123",
		RequestID:  "",
	})

	m := newModel.(Model)
	assert.True(t, m.sessions[0].Chat.Streaming)
	assert.Equal(t, "processing", m.sessions[0].State)
}

func TestModel_Update_StreamDeltaMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-123"
	model.sessions[0].Chat.StartStreaming()

	newModel, _ := model.Update(StreamDeltaMsg{
		SessionKey: "session-123",
		Delta:      "Hello",
	})

	m := newModel.(Model)
	assert.Equal(t, "Hello", m.sessions[0].Chat.StreamBuf.String())
}

func TestModel_Update_StreamEndMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-123"
	model.sessions[0].Chat.StartStreaming()
	model.sessions[0].LastRequestStart = time.Now().Add(-1 * time.Second)

	newModel, _ := model.Update(StreamEndMsg{
		SessionKey:       "session-123",
		Content:          "Hello world",
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
		Model:            "claude-3-opus",
		ContextWindow:    200000,
	})

	m := newModel.(Model)
	assert.False(t, m.sessions[0].Chat.Streaming)
	assert.Equal(t, "idle", m.sessions[0].State)
	assert.Equal(t, "claude-3-opus", m.sidebar.Model)
}

func TestModel_Update_ErrorMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.sessions[0].Key = "session-123"

	newModel, _ := model.Update(ErrorMsg{
		SessionKey: "session-123",
		Code:       "rate_limit",
		Message:    "Rate limit exceeded",
	})

	m := newModel.(Model)
	assert.Equal(t, "error", m.sessions[0].State)
}

func TestModel_Update_GatewayInfoMsg(t *testing.T) {
	client := &mockGatewayClient{connected: true}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)

	newModel, _ := model.Update(GatewayInfoMsg{
		AssistantName: "Claude",
		Version:       "1.0.0",
		GitCommit:     "abc1234",
		ToolCount:     15,
		SkillCount:    3,
	})

	m := newModel.(Model)
	assert.Equal(t, "Claude", m.assistantName)
	assert.Equal(t, "1.0.0", m.sidebar.Version)
	assert.Equal(t, "abc1234", m.sidebar.GitCommit)
	assert.Equal(t, 15, m.sidebar.ToolCount)
	assert.Equal(t, 3, m.sidebar.SkillCount)
}

func TestModel_SetSSHUser(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.SetSSHUser("alice")

	assert.Equal(t, "alice", model.statusBar.SSHUser)
	assert.Equal(t, "alice", model.sessions[0].Chat.UserName)
}

func TestModel_SetGatewayURL(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.SetGatewayURL("ws://newhost:1234/ws")

	assert.Equal(t, "ws://newhost:1234/ws", model.statusBar.GatewayURL)
	assert.Equal(t, "ws://newhost:1234/ws", model.sidebar.GatewayURL)
}

func TestModel_View_Quitting(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.quitting = true

	view := model.View()
	assert.Contains(t, view, "Goodbye")
}

func TestModel_View_Normal(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.width = 100
	model.height = 50
	model.updateLayout()

	view := model.View()
	assert.NotEmpty(t, view)
}

func TestModel_UpdateLayout(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.width = 120
	model.height = 40

	model.updateLayout()

	assert.Equal(t, 120, model.tabBar.Width)
	assert.Equal(t, 120, model.statusBar.Width)
}

func TestModel_UpdateLayout_WithSidebar(t *testing.T) {
	client := &mockGatewayClient{}
	config := ModelConfig{
		Client: client,
		UserID: "testuser",
	}

	model := NewModel(config)
	model.width = 120
	model.height = 40
	model.sidebar.Visible = true

	model.updateLayout()

	// Layout should account for sidebar width
	assert.Greater(t, model.sidebar.Height, 0)
}

func TestUpdateToolList(t *testing.T) {
	tools := []ToolActivityInfo{}

	// Add a new tool
	tools = updateToolList(tools, ToolActivityInfo{Name: "search", Status: "running"})
	assert.Len(t, tools, 1)

	// Update existing running tool
	tools = updateToolList(tools, ToolActivityInfo{Name: "search", Status: "complete"})
	assert.Len(t, tools, 1)
	assert.Equal(t, "complete", tools[0].Status)

	// Add another tool
	tools = updateToolList(tools, ToolActivityInfo{Name: "fetch", Status: "running"})
	assert.Len(t, tools, 2)
}

func TestUpdateToolList_MaxEntries(t *testing.T) {
	tools := []ToolActivityInfo{}

	// Add more than 10 tools
	for i := 0; i < 15; i++ {
		tools = updateToolList(tools, ToolActivityInfo{
			Name:   "tool-" + string(rune('a'+i)),
			Status: "complete",
		})
	}

	// Should keep only last 10
	assert.Len(t, tools, 10)
}

func TestModelConfig_Fields(t *testing.T) {
	config := ModelConfig{
		Client:        nil,
		UserID:        "user123",
		GatewayURL:    "ws://localhost/ws",
		AssistantName: "Claude",
		Location:      time.UTC,
		Renderer:      nil,
		ShellSecurity: ShellSecurityConfig{Enabled: true},
	}

	assert.Equal(t, "user123", config.UserID)
	assert.Equal(t, "ws://localhost/ws", config.GatewayURL)
	assert.Equal(t, "Claude", config.AssistantName)
	assert.Equal(t, time.UTC, config.Location)
	assert.True(t, config.ShellSecurity.Enabled)
}

func TestSessionState_Fields(t *testing.T) {
	state := SessionState{
		Key:              "session-123",
		Label:            "Chat 1",
		HasUnread:        true,
		Model:            "claude-3-opus",
		PromptTokens:     100,
		CompletionTokens: 20,
		TotalTokens:      120,
		ContextWindow:    200000,
		ContextPercent:   0.06,
		RequestCost:      0.001,
		SessionCost:      0.01,
		State:            "idle",
	}

	assert.Equal(t, "session-123", state.Key)
	assert.Equal(t, "Chat 1", state.Label)
	assert.True(t, state.HasUnread)
	assert.Equal(t, "claude-3-opus", state.Model)
	assert.Equal(t, 100, state.PromptTokens)
	assert.Equal(t, 120, state.TotalTokens)
	assert.Equal(t, "idle", state.State)
}
