package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"conduit/internal/ai"
)

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
			// Use context window from message (provider config), fall back to model-based detection
			contextWindow := msg.ContextWindow
			if contextWindow <= 0 {
				contextWindow = ai.ContextWindowForModel(msg.Model)
			}
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
			s.ContextWindow = m.sidebar.ContextWindow // use same value calculated above
			s.ContextPercent = m.sidebar.ContextPercent
			s.RequestCost = msg.RequestCost
			s.SessionCost = msg.SessionCost
		}

	// Tool event messages
	case ToolEventMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			if msg.EventType == "thinking" {
				s.State = msg.ToolName // e.g. "Thinking (step 2)..."
				if tabIdx == m.tabBar.ActiveIdx {
					m.sidebar.SessionState = s.State
					m.statusBar.SessionState = s.State
				}

			} else {
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
		}

	// Command response
	case CommandResponseMsg:
		s, tabIdx := m.resolveTab(msg.RequestID, msg.SessionKey)
		if s != nil {
			s.Chat.AddMessage("system", msg.Response)
		}
		// conduit-31jg.66: the message is queued behind a running turn; show
		// that in the tab state until its StreamStart arrives.
		if msg.Command == QueuedNoticeCommand && s != nil && !s.Chat.Streaming {
			s.State = "queued"
			if tabIdx == m.tabBar.ActiveIdx {
				m.sidebar.SessionState = s.State
				m.statusBar.SessionState = s.State
			}
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

	// Shell result messages
	case ShellResultMsg:
		if s := m.sessionByKey(msg.SessionKey); s != nil {
			// Clear running command state
			s.Shell.RunningCmd = nil
			s.Shell.RunningCancel = nil
			if msg.Err != nil {
				s.Chat.AddMessage("system", fmt.Sprintf("Error: %v\n%s", msg.Err, msg.Output))
			} else if msg.Output != "" {
				s.Chat.AddMessage("system", strings.TrimRight(msg.Output, "\n"))
			} else {
				s.Chat.AddMessage("system", "(no output)")
			}
		}

	case ShellCommandCancelledMsg:
		if s := m.sessionByKey(msg.SessionKey); s != nil {
			s.Chat.AddMessage("system", "^C")
		}

	case BackgroundJobStartedMsg:
		if s := m.sessionByKey(msg.SessionKey); s != nil {
			s.Chat.AddMessage("system", fmt.Sprintf("[%d] %s", msg.JobID, truncateCommand(msg.Command, 60)))
			// Start watching for job completion
			if s.Shell.Jobs != nil {
				cmds = append(cmds, watchBackgroundJobs(msg.SessionKey, s.Shell.Jobs))
			}
		}

	case BackgroundJobCompletedMsg:
		if s := m.sessionByKey(msg.SessionKey); s != nil {
			statusStr := msg.Status.String()
			s.Chat.AddMessage("system", fmt.Sprintf("[%d] %s", msg.JobID, statusStr))
			if msg.Output != "" {
				s.Chat.AddMessage("system", strings.TrimRight(msg.Output, "\n"))
			}
			if msg.Error != nil {
				s.Chat.AddMessage("system", fmt.Sprintf("Error: %v", msg.Error))
			}
			// Continue watching for more job completions
			if s.Shell.Jobs != nil {
				cmds = append(cmds, watchBackgroundJobs(msg.SessionKey, s.Shell.Jobs))
			}
		}

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
