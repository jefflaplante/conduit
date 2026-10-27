package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.66: the gateway sends a "queued" command response when a chat
// message waits behind the session's running turn.
func TestModel_QueuedNotice_RendersAndMarksTab(t *testing.T) {
	model := NewModel(ModelConfig{Client: &mockGatewayClient{connected: true}, UserID: "testuser"})
	model.sessions[0].Key = "session-1"

	newModel, _ := model.Update(CommandResponseMsg{
		SessionKey: "session-1",
		RequestID:  "r2",
		Command:    QueuedNoticeCommand,
		Response:   "Queued — I'll handle this right after the current request.",
	})
	m := newModel.(Model)
	s := m.sessions[0]
	require.NotEmpty(t, s.Chat.Messages)
	last := s.Chat.Messages[len(s.Chat.Messages)-1]
	assert.Equal(t, "system", last.Role)
	assert.Contains(t, last.Content, "Queued")
	assert.Equal(t, "queued", s.State)
	assert.Equal(t, "queued", m.statusBar.SessionState)

	// The queued turn's StreamStart replaces the state.
	newModel, _ = m.Update(StreamStartMsg{SessionKey: "session-1", RequestID: "r2"})
	m = newModel.(Model)
	assert.Equal(t, "processing", m.sessions[0].State)
}

// While the tab is still streaming the previous reply, the notice is shown
// but the streaming state is left alone.
func TestModel_QueuedNotice_WhileStreaming(t *testing.T) {
	model := NewModel(ModelConfig{Client: &mockGatewayClient{connected: true}, UserID: "testuser"})
	model.sessions[0].Key = "session-1"
	newModel, _ := model.Update(StreamStartMsg{SessionKey: "session-1"})
	m := newModel.(Model)

	newModel, _ = m.Update(CommandResponseMsg{SessionKey: "session-1", Command: QueuedNoticeCommand, Response: "Queued"})
	m = newModel.(Model)
	assert.Equal(t, "processing", m.sessions[0].State)
	assert.True(t, m.sessions[0].Chat.Streaming)
}

// conduit-5uw3: a "steered" notice ends a queued request that was folded
// into the running turn: the tab leaves "queued" and the correlation is
// dropped, since that request never streams.
func TestModel_SteeredNotice_ClearsQueued(t *testing.T) {
	model := NewModel(ModelConfig{Client: &mockGatewayClient{connected: true}, UserID: "testuser"})
	model.sessions[0].Key = "session-1"
	model.chatRequests["r2"] = 0

	newModel, _ := model.Update(CommandResponseMsg{SessionKey: "session-1", RequestID: "r2", Command: QueuedNoticeCommand, Response: "Got it"})
	m := newModel.(Model)
	require.Equal(t, "queued", m.sessions[0].State)

	newModel, _ = m.Update(CommandResponseMsg{SessionKey: "session-1", RequestID: "r2", Command: SteeredNoticeCommand, Response: "Folded"})
	m = newModel.(Model)
	assert.Equal(t, "processing", m.sessions[0].State)
	assert.Equal(t, "processing", m.statusBar.SessionState)
	_, still := m.chatRequests["r2"]
	assert.False(t, still, "steered request correlation must be dropped")
}

// While the tab streams the turn it joined, the steered notice leaves the
// streaming state alone.
func TestModel_SteeredNotice_WhileStreaming(t *testing.T) {
	model := NewModel(ModelConfig{Client: &mockGatewayClient{connected: true}, UserID: "testuser"})
	model.sessions[0].Key = "session-1"
	newModel, _ := model.Update(StreamStartMsg{SessionKey: "session-1"})
	m := newModel.(Model)
	newModel, _ = m.Update(CommandResponseMsg{SessionKey: "session-1", RequestID: "r2", Command: SteeredNoticeCommand, Response: "Folded"})
	m = newModel.(Model)
	assert.Equal(t, "processing", m.sessions[0].State)
	assert.True(t, m.sessions[0].Chat.Streaming)
}
