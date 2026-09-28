package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"conduit/internal/protocol"
)

// ShellSecurityConfig holds shell escape security settings for the TUI
type ShellSecurityConfig struct {
	// Enabled controls whether shell escape is available
	Enabled bool
	// CommandAllowlist, if non-empty, restricts shell commands to only those matching these prefixes
	CommandAllowlist []string
	// CommandBlocklist blocks commands matching these prefixes
	CommandBlocklist []string
}

// ModelConfig holds the configuration for creating a new TUI model
type ModelConfig struct {
	Client        GatewayClient
	UserID        string
	GatewayURL    string
	AssistantName string
	// Location is the timezone for rendering timestamps. If nil, times render as-is.
	Location *time.Location
	// Renderer is the Lip Gloss renderer to use for styling. Over SSH, pass the
	// renderer from wishbubbletea.MakeRenderer so colors work correctly. If nil,
	// the default renderer (local terminal) is used.
	Renderer *lipgloss.Renderer
	// ShellSecurity configures the shell escape (! prefix) feature
	ShellSecurity ShellSecurityConfig
}

// SessionState tracks the state of a single session tab
type SessionState struct {
	Key       string
	Label     string
	Chat      ChatViewModel
	Tools     []ToolActivityInfo
	HasUnread bool
	// Per-tab AI state
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	ContextWindow    int
	ContextPercent   float64
	RequestCost      float64
	SessionCost      float64
	// Session lifecycle
	CreatedAt        time.Time
	LastRequestStart time.Time     // set on send, cleared on StreamEnd
	LastResponseTime time.Duration // time from send to StreamEnd
	State            string        // "idle", "processing", "tool: X", "error"
	// Shell escape state
	Shell ShellState
}

// Model is the root BubbleTea model
type Model struct {
	config ModelConfig
	client GatewayClient
	styles Styles

	// Sub-models
	tabBar    TabBarModel
	sidebar   SidebarModel
	statusBar StatusBarModel
	input     textarea.Model

	// Session state per tab
	sessions []SessionState

	// Global state
	width            int
	height           int
	connected        bool
	reconnectAttempt int
	quitting         bool
	assistantName    string

	// Request correlation for async responses
	pendingRequests map[string]int // requestID -> tab index

	// Chat request correlation for stream routing
	chatRequests map[string]int // requestID -> tab index
}

// NewModel creates the root TUI model
func NewModel(config ModelConfig) Model {
	r := config.Renderer
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	styles := NewStyles(r)

	// Create text input
	ti := textarea.New()
	ti.Placeholder = "Type a message... (Enter to send, Alt+Enter for new line)"
	ti.ShowLineNumbers = false
	ti.SetHeight(3)
	ti.SetWidth(80)
	ti.Focus()
	ti.CharLimit = 4000
	ti.Cursor.SetChar("█")
	ti.Cursor.Style = styles.WhiteCursor // Back to obnoxious but visible purple - should show on black background // Back to obnoxious but visible purple
	ti.Cursor.Blink = false              // Disable blinking for SSH compatibility

	// Initialize with one session
	assistantName := config.AssistantName
	if assistantName == "" {
		assistantName = "Assistant"
	}

	initialChat := NewChatViewModel(styles, assistantName, config.Location)
	initialChat.UserName = config.UserID
	sessions := []SessionState{
		{
			Label: "Chat 1",
			Chat:  initialChat,
			Shell: NewShellState(),
		},
	}

	return Model{
		config:          config,
		client:          config.Client,
		styles:          styles,
		tabBar:          NewTabBarModel(styles),
		sidebar:         NewSidebarModel(styles),
		statusBar:       NewStatusBarModel(styles),
		input:           ti,
		sessions:        sessions,
		assistantName:   assistantName,
		pendingRequests: make(map[string]int), // Initialize request correlation map
		chatRequests:    make(map[string]int), // Initialize chat request correlation map
	}
}

// Init initializes the model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.client.ConnectCmd(),
		// Request session list on connect
	)
}

// activeSession returns the current active session state
func (m *Model) activeSession() *SessionState {
	idx := m.tabBar.ActiveIdx
	if idx >= 0 && idx < len(m.sessions) {
		return &m.sessions[idx]
	}
	return nil
}

// sessionByKey returns the session with the given key, or nil if not found
func (m *Model) sessionByKey(sessionKey string) *SessionState {
	for i := range m.sessions {
		if m.sessions[i].Key == sessionKey {
			return &m.sessions[i]
		}
	}
	return nil
}

// resolveTab finds the correct tab for a message using RequestID, then SessionKey, then active tab.
func (m *Model) resolveTab(requestID, sessionKey string) (*SessionState, int) {
	if requestID != "" {
		if idx, exists := m.chatRequests[requestID]; exists {
			if idx >= 0 && idx < len(m.sessions) {
				return &m.sessions[idx], idx
			}
		}
	}
	if sessionKey != "" {
		for i := range m.sessions {
			if m.sessions[i].Key == sessionKey {
				return &m.sessions[i], i
			}
		}
	}
	return m.activeSession(), m.tabBar.ActiveIdx
}

// updateActiveSession syncs sidebar/statusbar with current tab
func (m *Model) updateActiveSession() {
	s := m.activeSession()
	if s == nil {
		return
	}
	m.statusBar.SessionKey = s.Key
	m.sidebar.SessionKey = s.Key
	m.sidebar.MessageCount = len(s.Chat.Messages)
	m.sidebar.ActiveTools = s.Tools
	m.sidebar.Model = s.Model
	m.statusBar.Model = s.Model
	m.sidebar.PromptTokens = s.PromptTokens
	m.sidebar.CompletionTokens = s.CompletionTokens
	m.sidebar.TotalTokens = s.TotalTokens
	m.sidebar.ContextWindow = s.ContextWindow
	m.sidebar.ContextPercent = s.ContextPercent
	m.statusBar.ContextPercent = s.ContextPercent
	if s.ContextWindow > 0 {
		m.statusBar.ContextProjected = float64(s.TotalTokens) / float64(s.ContextWindow) * 100
	} else {
		m.statusBar.ContextProjected = 0
	}
	// Sync cost
	m.sidebar.RequestCost = s.RequestCost
	m.sidebar.SessionCost = s.SessionCost
	m.statusBar.SessionCost = s.SessionCost
	// Sync session state, response time, and age
	m.sidebar.SessionState = s.State
	m.statusBar.SessionState = s.State
	m.sidebar.LastResponseTime = s.LastResponseTime
	m.statusBar.LastResponseTime = s.LastResponseTime
	m.sidebar.SessionCreatedAt = s.CreatedAt
	m.updateLayout()
}

// handleSessionList processes the session list from the gateway
func (m *Model) handleSessionList(sessions []protocol.SessionInfo) {
	// If we have no session key on the first tab, adopt the most recent session
	if len(sessions) > 0 {
		if s := m.activeSession(); s != nil && s.Key == "" {
			s.Key = sessions[0].Key
			if tab := m.tabBar.ActiveTab(); tab != nil {
				tab.SessionKey = sessions[0].Key
			}
			m.statusBar.SessionKey = sessions[0].Key
			m.sidebar.SessionKey = sessions[0].Key
			m.sidebar.MessageCount = sessions[0].MessageCount
			// Switch to load history
			if m.client != nil {
				m.client.SwitchSession(sessions[0].Key)
			}
		}
	}
}

// updateLayout recalculates sub-model dimensions
func (m *Model) updateLayout() {
	tabBarHeight := 2 // tab bar + border
	statusBarHeight := 1
	inputHeight := 4 // textarea + border

	sidebarWidth := m.sidebar.TotalSidebarWidth()
	chatWidth := m.width - sidebarWidth
	chatHeight := m.height - tabBarHeight - statusBarHeight - inputHeight

	if chatWidth < 20 {
		chatWidth = 20
	}
	if chatHeight < 5 {
		chatHeight = 5
	}

	// Update sub-model dimensions
	m.tabBar.Width = m.width
	m.sidebar.Height = chatHeight
	m.statusBar.Width = m.width
	m.input.SetWidth(chatWidth - 2)

	// Update chat views for all sessions
	for i := range m.sessions {
		m.sessions[i].Chat.SetSize(chatWidth, chatHeight)
	}
}

// View renders the entire TUI
func (m Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	var sections []string

	// Tab bar
	sections = append(sections, m.tabBar.View())

	// Main content area: chat + optional sidebar
	s := m.activeSession()
	var chatView string
	if s != nil {
		chatView = s.Chat.View()
	}

	if m.sidebar.Visible {
		mainArea := lipgloss.JoinHorizontal(lipgloss.Top,
			chatView,
			m.sidebar.View(),
		)
		sections = append(sections, mainArea)
	} else {
		sections = append(sections, chatView)
	}

	// Input area
	sections = append(sections, m.styles.InputStyle.Width(m.width).Render(m.input.View()))

	// Status bar
	sections = append(sections, m.statusBar.View())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// SetSSHUser sets the SSH user for display in the status bar and chat views
func (m *Model) SetSSHUser(user string) {
	m.statusBar.SSHUser = user
	for i := range m.sessions {
		m.sessions[i].Chat.SetUserName(user)
	}
}

// SetGatewayURL sets the gateway URL for display
func (m *Model) SetGatewayURL(url string) {
	m.statusBar.GatewayURL = url
	m.sidebar.GatewayURL = url
}
