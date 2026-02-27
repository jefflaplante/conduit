package tui

import tea "github.com/charmbracelet/bubbletea"

// GatewayClient abstracts the connection between the TUI and the gateway.
// WSClient implements this over WebSocket; DirectClient implements it in-process.
type GatewayClient interface {
	ConnectCmd() tea.Cmd
	ListenCmd() tea.Cmd
	ReconnectCmd(attempt int) tea.Cmd
	IsConnected() bool
	Close()
	SendChat(sessionKey, text string) error
	SendChatWithID(sessionKey, text, requestID string) error
	SendCommand(sessionKey, command, args string) error
	CreateSession() error
	CreateSessionWithID(requestID string) error
	SwitchSession(key string) error
	ListSessions() error
}
