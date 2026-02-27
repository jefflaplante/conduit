package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"conduit/internal/ai"
	"conduit/pkg/protocol"
)

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

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()

	case tea.KeyMsg:
		cmd, handled := m.handleKeyMsg(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if m.quitting {
			return m, tea.Quit
		}
		if handled {
			return m, tea.Batch(cmds...)
		}

	// Connection messages
	case ConnectedMsg:
		m.connected = true
		m.reconnectAttempt = 0
		m.statusBar.Connected = true
		m.statusBar.Reconnecting = false
		m.sidebar.Connected = true
		// Request session list
		if m.client != nil {
			m.client.ListSessions()
		}
		// Add system message
		if s := m.activeSession(); s != nil {
			s.Chat.AddMessage("system", "Connected to gateway.")
		}

	case DisconnectedMsg:
		m.connected = false
		m.statusBar.Connected = false
		m.sidebar.Connected = false
		if msg.Err != nil {
			if s := m.activeSession(); s != nil {
				s.Chat.AddMessage("system", "Disconnected: "+msg.Err.Error())
			}
		}
		// Auto-reconnect
		m.reconnectAttempt++
		m.statusBar.Reconnecting = true
		m.statusBar.Attempt = m.reconnectAttempt
		cmds = append(cmds, m.client.ReconnectCmd(m.reconnectAttempt))

	case ReconnectingMsg:
		m.statusBar.Reconnecting = true
		m.statusBar.Attempt = msg.Attempt

	// Stream messages
	case StreamStartMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			s.Chat.StartStreaming()
			s.State = "processing"
			if tabIdx == m.tabBar.ActiveIdx {
				m.sidebar.SessionState = s.State
				m.statusBar.SessionState = s.State
			}
			if tabIdx != m.tabBar.ActiveIdx && tabIdx < len(m.tabBar.Tabs) {
				m.tabBar.Tabs[tabIdx].HasUnread = true
			}
			// Start thinking animation if no text has arrived yet
			if s.Chat.StreamBuf.Len() == 0 {
				cmds = append(cmds, thinkingTickCmd())
			}
		}

	case ThinkingTickMsg:
		// Advance animation only if active session is still streaming with empty buffer
		if s := m.activeSession(); s != nil && s.Chat.Streaming && s.Chat.StreamBuf.Len() == 0 {
			s.Chat.ThinkingTick()
			cmds = append(cmds, thinkingTickCmd())
		}

	case StreamDeltaMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			s.Chat.AppendDelta(msg.Delta)
			if tabIdx != m.tabBar.ActiveIdx && tabIdx < len(m.tabBar.Tabs) {
				m.tabBar.Tabs[tabIdx].HasUnread = true
			}
		}

	case StreamEndMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			s.Chat.EndStreaming(msg.Content)
			s.State = "idle"
			if !s.LastRequestStart.IsZero() {
				s.LastResponseTime = time.Since(s.LastRequestStart)
				s.LastRequestStart = time.Time{}
			}
			if tabIdx == m.tabBar.ActiveIdx {
				m.sidebar.SessionState = s.State
				m.statusBar.SessionState = s.State
				m.sidebar.LastResponseTime = s.LastResponseTime
				m.statusBar.LastResponseTime = s.LastResponseTime
			}
			if tabIdx != m.tabBar.ActiveIdx && tabIdx < len(m.tabBar.Tabs) {
				m.tabBar.Tabs[tabIdx].HasUnread = true
			}
		}
		// Clean up the chat request correlation
		if msg.RequestID != "" {
			delete(m.chatRequests, msg.RequestID)
		}
		// Update context usage in status bar, sidebar, and per-tab state
		m.sidebar.Model = msg.Model
		m.statusBar.Model = msg.Model
		if msg.PromptTokens > 0 {
			contextWindow := ai.ContextWindowForModel(msg.Model)
			contextWindowF := float64(contextWindow)
			m.statusBar.ContextPercent = float64(msg.PromptTokens) / contextWindowF * 100
			m.statusBar.ContextProjected = float64(msg.TotalTokens) / contextWindowF * 100
			m.sidebar.PromptTokens = msg.PromptTokens
			m.sidebar.CompletionTokens = msg.CompletionTokens
			m.sidebar.TotalTokens = msg.TotalTokens
			m.sidebar.ContextWindow = contextWindow
			m.sidebar.ContextPercent = float64(msg.PromptTokens) / contextWindowF * 100
		}
		// Update cost in sidebar and status bar
		m.sidebar.RequestCost = msg.RequestCost
		m.sidebar.SessionCost = msg.SessionCost
		m.statusBar.SessionCost = msg.SessionCost
		// Save per-tab AI state
		if s != nil {
			s.Model = msg.Model
			s.PromptTokens = msg.PromptTokens
			s.CompletionTokens = msg.CompletionTokens
			s.TotalTokens = msg.TotalTokens
			s.ContextWindow = ai.ContextWindowForModel(msg.Model)
			s.ContextPercent = m.sidebar.ContextPercent
			s.RequestCost = msg.RequestCost
			s.SessionCost = msg.SessionCost
		}

	// Tool event messages
	case ToolEventMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			info := ToolActivityInfo{
				Name:     msg.ToolName,
				Status:   msg.EventType,
				Args:     msg.Args,
				Result:   msg.Result,
				Error:    msg.ToolEvent.Error,
				Duration: msg.Duration,
			}
			m.sidebar.ActiveTools = updateToolList(m.sidebar.ActiveTools, info)
			s.Tools = updateToolList(s.Tools, info)
			if msg.EventType == "start" {
				s.State = "tool: " + msg.ToolName
				if tabIdx == m.tabBar.ActiveIdx {
					m.sidebar.SessionState = s.State
					m.statusBar.SessionState = s.State
				}
			}
			if tabIdx != m.tabBar.ActiveIdx && tabIdx < len(m.tabBar.Tabs) {
				m.tabBar.Tabs[tabIdx].HasUnread = true
			}
		}

	// Command response
	case CommandResponseMsg:
		s, _ := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			s.Chat.AddMessage("system", msg.Response)
		}
		if msg.Command == "/model" && msg.Model != "" {
			m.sidebar.Model = msg.Model
			m.statusBar.Model = msg.Model
			if s := m.activeSession(); s != nil {
				s.Model = msg.Model
			}
		}
		if msg.Command == "/reset" || msg.Command == "/new" {
			m.statusBar.ContextPercent = 0
			m.statusBar.ContextProjected = 0
			m.statusBar.SessionCost = 0
			m.sidebar.PromptTokens = 0
			m.sidebar.CompletionTokens = 0
			m.sidebar.TotalTokens = 0
			m.sidebar.ContextWindow = 0
			m.sidebar.ContextPercent = 0
			m.sidebar.RequestCost = 0
			m.sidebar.SessionCost = 0
			if s := m.activeSession(); s != nil {
				s.PromptTokens = 0
				s.CompletionTokens = 0
				s.TotalTokens = 0
				s.ContextWindow = 0
				s.ContextPercent = 0
				s.RequestCost = 0
				s.SessionCost = 0
			}
		}

	// Session messages
	case SessionListMsg:
		m.handleSessionList(msg.Sessions)

	case SessionCreatedMsg:
		// Use request ID to find the correct tab that made the request
		if msg.RequestID != "" {
			if tabIdx, exists := m.pendingRequests[msg.RequestID]; exists {
				// Clean up the pending request
				delete(m.pendingRequests, msg.RequestID)

				// Update the specific tab that made the request
				if tabIdx < len(m.sessions) && tabIdx < len(m.tabBar.Tabs) {
					m.sessions[tabIdx].Key = msg.Key
					m.sessions[tabIdx].CreatedAt = msg.CreatedAt
					m.tabBar.Tabs[tabIdx].SessionKey = msg.Key

					// Update status and sidebar if this is the active tab
					if tabIdx == m.tabBar.ActiveIdx {
						m.statusBar.SessionKey = msg.Key
						m.sidebar.SessionKey = msg.Key
						m.sidebar.SessionCreatedAt = msg.CreatedAt
					}
				}
			}
		} else {
			// Fallback to old behavior if no RequestID (backwards compatibility)
			if s := m.activeSession(); s != nil && s.Key == "" {
				s.Key = msg.Key
				s.CreatedAt = msg.CreatedAt
				if tab := m.tabBar.ActiveTab(); tab != nil {
					tab.SessionKey = msg.Key
				}
				m.statusBar.SessionKey = msg.Key
				m.sidebar.SessionKey = msg.Key
				m.sidebar.SessionCreatedAt = msg.CreatedAt
			}
		}

	case SessionSwitchedMsg:
		if s := m.activeSession(); s != nil {
			s.Key = msg.Key
			s.CreatedAt = msg.CreatedAt
			s.Chat.ClearMessages()
			// Replay history
			for _, h := range msg.History {
				s.Chat.AddMessage(h.Role, h.Content)
			}
			m.statusBar.SessionKey = msg.Key
			m.sidebar.SessionKey = msg.Key
			m.sidebar.Model = msg.Model
			m.statusBar.Model = msg.Model
			m.sidebar.SessionCreatedAt = msg.CreatedAt
			s.Model = msg.Model
			// Reset context fields — new session has no token data until first AI response
			m.sidebar.PromptTokens = 0
			m.sidebar.CompletionTokens = 0
			m.sidebar.TotalTokens = 0
			m.sidebar.ContextWindow = 0
			m.sidebar.ContextPercent = 0
			m.sidebar.RequestCost = 0
			m.sidebar.SessionCost = 0
			m.statusBar.SessionCost = 0
			s.PromptTokens = 0
			s.CompletionTokens = 0
			s.TotalTokens = 0
			s.ContextWindow = 0
			s.ContextPercent = 0
			s.RequestCost = 0
			s.SessionCost = 0
		}

	// Gateway info
	case GatewayInfoMsg:
		if msg.AssistantName != "" {
			m.assistantName = msg.AssistantName
			for i := range m.sessions {
				m.sessions[i].Chat.SetAssistantName(msg.AssistantName)
			}
		}
		m.sidebar.Version = msg.Version
		m.sidebar.GitCommit = msg.GitCommit
		m.sidebar.UptimeSeconds = msg.UptimeSeconds
		m.sidebar.ModelAliases = msg.ModelAliases
		m.sidebar.ToolCount = msg.ToolCount
		m.sidebar.SkillCount = msg.SkillCount

	// Error messages
	case ErrorMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			errText := "Error: " + msg.Message
			if msg.Code != "" {
				errText = fmt.Sprintf("Error [%s]: %s", msg.Code, msg.Message)
			}
			s.Chat.AddMessage("system", errText)
			s.State = "error"
			if tabIdx == m.tabBar.ActiveIdx {
				m.sidebar.SessionState = s.State
				m.statusBar.SessionState = s.State
			}
		}
	}

	// Re-subscribe to client messages after processing any client-originated message
	switch msg.(type) {
	case ConnectedMsg, StreamStartMsg, StreamDeltaMsg, StreamEndMsg,
		ToolEventMsg, CommandResponseMsg, SessionListMsg,
		SessionCreatedMsg, SessionSwitchedMsg, GatewayInfoMsg, ErrorMsg:
		if m.connected {
			cmds = append(cmds, m.client.ListenCmd())
		}
	}

	// Update textarea
	var tiCmd tea.Cmd
	m.input, tiCmd = m.input.Update(msg)
	if tiCmd != nil {
		cmds = append(cmds, tiCmd)
	}

	return m, tea.Batch(cmds...)
}

// handleKeyMsg processes keyboard input.
// Returns (cmd, handled) where handled=true prevents the textarea from also processing the key.
func (m *Model) handleKeyMsg(msg tea.KeyMsg) (tea.Cmd, bool) {
	key := msg.String()

	switch key {
	case "ctrl+c":
		m.quitting = true
		if m.client != nil {
			m.client.Close()
		}
		return tea.Quit, true

	case "ctrl+t":
		// New session tab
		idx := m.tabBar.AddTab("", "")
		chat := NewChatViewModel(m.styles, m.assistantName, m.config.Location)
		chat.UserName = m.statusBar.SSHUser
		m.sessions = append(m.sessions, SessionState{
			Label: m.tabBar.Tabs[idx].Label,
			Chat:  chat,
		})
		m.tabBar.ActiveIdx = idx
		m.updateActiveSession()
		if m.client != nil {
			// Generate request ID and track which tab made the request
			requestID := fmt.Sprintf("tab_%d_%d", idx, time.Now().UnixNano())
			m.pendingRequests[requestID] = idx
			m.client.CreateSessionWithID(requestID)
		}
		return nil, true

	case "ctrl+w":
		// Close current tab
		if len(m.tabBar.Tabs) > 1 {
			idx := m.tabBar.ActiveIdx

			// Clean up pending requests for this tab
			for requestID, tabIdx := range m.pendingRequests {
				if tabIdx == idx {
					delete(m.pendingRequests, requestID)
				} else if tabIdx > idx {
					// Adjust indices for tabs that will shift down
					m.pendingRequests[requestID] = tabIdx - 1
				}
			}

			// Clean up chat requests for this tab
			for requestID, tabIdx := range m.chatRequests {
				if tabIdx == idx {
					delete(m.chatRequests, requestID)
				} else if tabIdx > idx {
					// Adjust indices for tabs that will shift down
					m.chatRequests[requestID] = tabIdx - 1
				}
			}

			m.tabBar.RemoveTab(idx)
			if idx < len(m.sessions) {
				m.sessions = append(m.sessions[:idx], m.sessions[idx+1:]...)
			}
			m.updateActiveSession()
		}
		return nil, true

	case "alt+left":
		// Previous tab
		if m.tabBar.ActiveIdx > 0 {
			m.tabBar.ActiveIdx--
			m.tabBar.Tabs[m.tabBar.ActiveIdx].HasUnread = false
			m.updateActiveSession()
		}
		return nil, true

	case "alt+right":
		// Next tab
		if m.tabBar.ActiveIdx < len(m.tabBar.Tabs)-1 {
			m.tabBar.ActiveIdx++
			m.tabBar.Tabs[m.tabBar.ActiveIdx].HasUnread = false
			m.updateActiveSession()
		}
		return nil, true

	case "tab":
		// Toggle sidebar
		m.sidebar.Visible = !m.sidebar.Visible
		m.updateLayout()
		return nil, true

	case "shift+tab":
		// Cycle sidebar tabs
		m.sidebar.CycleTab()
		return nil, true

	case "pgup":
		if s := m.activeSession(); s != nil {
			s.Chat.Viewport.HalfViewUp()
		}
		return nil, true

	case "pgdown":
		if s := m.activeSession(); s != nil {
			s.Chat.Viewport.HalfViewDown()
		}
		return nil, true

	case "alt+enter":
		// Insert newline into textarea
		m.input.InsertString("\n")
		return nil, true

	case "enter":
		// Send message
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return nil, true
		}

		// Handle local commands
		if text == "/quit" || text == "/exit" {
			m.quitting = true
			if m.client != nil {
				m.client.Close()
			}
			return tea.Quit, true
		}

		m.input.Reset()

		s := m.activeSession()
		if s == nil {
			return nil, true
		}

		sessionKey := s.Key

		// Check if it's a slash command
		if strings.HasPrefix(text, "/") {
			// /help and /commands are handled locally
			if text == "/help" || text == "/commands" {
				s.Chat.AddMessage("system",
					"Available Commands:\n\n"+
						"/reset - Clear conversation history\n"+
						"/status - Show session info\n"+
						"/help - Show this message\n"+
						"/model [alias] - View/switch model\n"+
						"/context - Show context window usage\n"+
						"/stop - Stop current operation\n"+
						"/quit, /exit - Exit TUI\n\n"+
						"Ctrl+T: New tab | Ctrl+W: Close tab\n"+
						"Alt+Left/Right: Switch tabs\n"+
						"Alt+Enter: Insert new line\n"+
						"Tab: Toggle sidebar | Shift+Tab: Cycle sidebar\n"+
						"PgUp/PgDn: Scroll chat | Ctrl+C: Quit")
				return nil, true
			}
			// Send command to gateway
			parts := strings.SplitN(text, " ", 2)
			cmd := parts[0]
			args := ""
			if len(parts) > 1 {
				args = parts[1]
			}
			s.Chat.AddMessage("system", text)
			if m.client != nil {
				m.client.SendCommand(sessionKey, cmd, args)
			}
		} else {
			// Regular chat message
			s.Chat.AddMessage("user", text)
			s.LastRequestStart = time.Now()
			if m.client != nil {
				// Generate request ID and track which tab made the request
				requestID := fmt.Sprintf("chat_%d_%d", m.tabBar.ActiveIdx, time.Now().UnixNano())
				m.chatRequests[requestID] = m.tabBar.ActiveIdx
				m.client.SendChatWithID(sessionKey, text, requestID)
			}
		}
		return nil, true
	}

	return nil, false
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

// thinkingTickCmd returns a command that fires a ThinkingTickMsg after a short delay.
func thinkingTickCmd() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return ThinkingTickMsg{}
	})
}

// updateToolList updates or adds a tool to the activity list
func updateToolList(tools []ToolActivityInfo, info ToolActivityInfo) []ToolActivityInfo {
	// Update existing entry for this tool
	for i, t := range tools {
		if t.Name == info.Name && t.Status == "running" {
			tools[i] = info
			return tools
		}
	}
	// Add new entry
	tools = append(tools, info)
	// Keep last 10 entries
	if len(tools) > 10 {
		tools = tools[len(tools)-10:]
	}
	return tools
}
