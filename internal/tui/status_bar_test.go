package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewStatusBarModel(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)

	assert.False(t, statusBar.Connected)
	assert.False(t, statusBar.Reconnecting)
	assert.Equal(t, 0, statusBar.Attempt)
}

func TestStatusBarModel_View_Connected(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 80
	statusBar.Connected = true

	view := statusBar.View()
	assert.NotEmpty(t, view)
	assert.Contains(t, view, "connected")
}

func TestStatusBarModel_View_Disconnected(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 80
	statusBar.Connected = false
	statusBar.Reconnecting = false

	view := statusBar.View()
	assert.NotEmpty(t, view)
	assert.Contains(t, view, "disconnected")
}

func TestStatusBarModel_View_Reconnecting(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 80
	statusBar.Connected = false
	statusBar.Reconnecting = true
	statusBar.Attempt = 3

	view := statusBar.View()
	assert.NotEmpty(t, view)
	assert.Contains(t, view, "reconnecting")
	assert.Contains(t, view, "3")
}

func TestStatusBarModel_View_WithGatewayURL(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.GatewayURL = "ws://localhost:18789/ws"

	view := statusBar.View()
	assert.Contains(t, view, "localhost:18789")
}

func TestStatusBarModel_View_WithLongURL(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.GatewayURL = "wss://very-long-hostname.example.com:18789/ws/path/to/endpoint"

	view := statusBar.View()
	// URL should be truncated
	assert.Contains(t, view, "...")
}

func TestStatusBarModel_View_WithModel(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.Model = "claude-3-sonnet"

	view := statusBar.View()
	assert.Contains(t, view, "claude-3-sonnet")
}

func TestStatusBarModel_View_WithSessionState(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SessionState = "processing"

	view := statusBar.View()
	assert.Contains(t, view, "processing")
}

func TestStatusBarModel_View_IdleState(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SessionState = "idle"

	view := statusBar.View()
	// "idle" state should not appear in status bar
	assert.NotContains(t, view, "idle")
}

func TestStatusBarModel_View_WithResponseTime(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.LastResponseTime = 1500 * time.Millisecond

	view := statusBar.View()
	assert.Contains(t, view, "1.5s")
}

func TestStatusBarModel_View_WithSessionCost(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SessionCost = 0.0123

	view := statusBar.View()
	assert.Contains(t, view, "$0.0123")
}

func TestStatusBarModel_View_WithContextUsage(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.ContextPercent = 50.0
	statusBar.ContextProjected = 60.0

	view := statusBar.View()
	// Should show context bar
	assert.Contains(t, view, "[")
	assert.Contains(t, view, "]")
}

func TestStatusBarModel_View_WithSessionKey(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SessionKey = "short-key"

	view := statusBar.View()
	assert.Contains(t, view, "short-key")
}

func TestStatusBarModel_View_WithLongSessionKey(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SessionKey = "this-is-a-very-long-session-key-that-should-be-truncated"

	view := statusBar.View()
	// Long session key should be truncated
	assert.Contains(t, view, "...")
}

func TestStatusBarModel_View_WithSSHUser(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 120
	statusBar.Connected = true
	statusBar.SSHUser = "testuser"

	view := statusBar.View()
	assert.Contains(t, view, "SSH")
	assert.Contains(t, view, "testuser")
}

func TestStatusBarModel_View_AllFields(t *testing.T) {
	styles := DefaultStyles()
	statusBar := NewStatusBarModel(styles)
	statusBar.Width = 200
	statusBar.Connected = true
	statusBar.GatewayURL = "ws://localhost:18789/ws"
	statusBar.Model = "claude-3-opus"
	statusBar.SessionState = "tool: search"
	statusBar.LastResponseTime = 2 * time.Second
	statusBar.SessionCost = 0.05
	statusBar.ContextPercent = 25.0
	statusBar.ContextProjected = 30.0
	statusBar.SessionKey = "session-abc123"
	statusBar.SSHUser = "admin"

	view := statusBar.View()
	assert.NotEmpty(t, view)
	// All components should be visible
	assert.Contains(t, view, "connected")
	assert.Contains(t, view, "localhost")
	assert.Contains(t, view, "claude-3-opus")
	assert.Contains(t, view, "tool: search")
	assert.Contains(t, view, "SSH")
}
