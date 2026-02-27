package tui

import (
	"fmt"
	"strings"
	"time"
)

// StatusBarModel manages the bottom status bar
type StatusBarModel struct {
	Connected        bool
	Reconnecting     bool
	Attempt          int
	GatewayURL       string
	Model            string
	SessionKey       string
	SSHUser          string // set for SSH sessions
	Width            int
	Styles           Styles
	ContextPercent   float64 // prompt tokens as % of context window
	ContextProjected float64 // total tokens as % of context window
	LastResponseTime time.Duration
	SessionState     string
	SessionCost      float64
}

// NewStatusBarModel creates a new status bar
func NewStatusBarModel(styles Styles) StatusBarModel {
	return StatusBarModel{
		Styles: styles,
	}
}

// View renders the status bar
func (s StatusBarModel) View() string {
	var parts []string

	// Connection status indicator
	if s.Connected {
		parts = append(parts, s.Styles.StatusConnected.Render("* connected"))
	} else if s.Reconnecting {
		parts = append(parts, s.Styles.StatusReconnecting.Render(fmt.Sprintf("~ reconnecting (%d)", s.Attempt)))
	} else {
		parts = append(parts, s.Styles.StatusDisconnected.Render("x disconnected"))
	}

	// Gateway URL (shortened)
	if s.GatewayURL != "" {
		url := s.GatewayURL
		// Strip ws:// prefix for brevity
		url = strings.TrimPrefix(url, "ws://")
		url = strings.TrimPrefix(url, "wss://")
		if len(url) > 25 {
			url = url[:22] + "..."
		}
		parts = append(parts, s.Styles.Muted.Render(url))
	}

	// Model
	if s.Model != "" {
		parts = append(parts, s.Styles.Accent.Render(s.Model))
	}

	// Session state (when not idle)
	if s.SessionState != "" && s.SessionState != "idle" {
		parts = append(parts, s.Styles.Accent.Render(s.SessionState))
	}

	// Last response time
	if s.LastResponseTime > 0 {
		parts = append(parts, s.Styles.Muted.Render(formatResponseTime(s.LastResponseTime)))
	}

	// Session cost
	if s.SessionCost > 0 {
		parts = append(parts, s.Styles.Muted.Render(fmt.Sprintf("$%.4f", s.SessionCost)))
	}

	// Context usage
	if s.ContextPercent > 0 {
		parts = append(parts, renderContextBar(s.Styles, s.ContextProjected, 10))
	}

	// Session key (shortened)
	if s.SessionKey != "" {
		key := s.SessionKey
		if len(key) > 20 {
			key = key[:17] + "..."
		}
		parts = append(parts, s.Styles.Muted.Render(key))
	}

	// SSH indicator
	if s.SSHUser != "" {
		parts = append(parts, s.Styles.Accent.Render(fmt.Sprintf("SSH: %s", s.SSHUser)))
	}

	content := strings.Join(parts, "  |  ")
	return s.Styles.StatusBar.Width(s.Width).Render(content)
}
