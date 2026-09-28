package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// handleKeyMsg processes keyboard input.
// Returns (cmd, handled) where handled=true prevents the textarea from also processing the key.
func (m *Model) handleKeyMsg(msg tea.KeyMsg) (tea.Cmd, bool) {
	key := msg.String()

	switch key {
	case "ctrl+c":
		// First, try to cancel any running shell command
		if s := m.activeSession(); s != nil {
			if s.Shell.CancelRunningCommand() {
				s.Chat.AddMessage("system", "^C")
				return nil, true
			}
		}
		// No running command, quit the TUI
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
			Shell: NewShellState(),
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
			s.Chat.Viewport.HalfPageUp()
		}
		return nil, true

	case "pgdown":
		if s := m.activeSession(); s != nil {
			s.Chat.Viewport.HalfPageDown()
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

		// Handle shell escape (! prefix)
		if strings.HasPrefix(text, "!") {
			cmdLine := strings.TrimSpace(strings.TrimPrefix(text, "!"))
			if cmdLine != "" {
				// Check if shell escape is enabled
				if !m.config.ShellSecurity.Enabled {
					s.Chat.AddMessage("system", "Shell escape is disabled")
					return nil, true
				}

				// Check command against security rules
				if err := m.validateShellCommand(cmdLine); err != nil {
					s.Chat.AddMessage("system", "Command blocked: "+err.Error())
					return nil, true
				}

				// Handle cd command specially (shell builtin that needs state tracking)
				if isCd, args := IsCdCommand(cmdLine); isCd {
					// Expand variables in args
					args = s.Shell.ExpandVariables(args)
					newState, errMsg := s.Shell.HandleCdCommand(args)
					if errMsg != "" {
						s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine+"\n"+errMsg)
					} else {
						// Show the cd command with the old prompt, then show the new directory
						s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
						s.Shell = newState
						// If cd - was used, show the new directory
						if args == "-" {
							s.Chat.AddMessage("system", newState.CurrentDir)
						}
					}
					return nil, true
				}

				// Handle export command
				if isExport, varName, value, showOnly := IsExportCommand(cmdLine); isExport {
					newState, output := s.Shell.HandleExportCommand(varName, value, showOnly)
					s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
					if output != "" {
						s.Chat.AddMessage("system", output)
					}
					s.Shell = newState
					return nil, true
				}

				// Handle unset command
				if isUnset, varName := IsUnsetCommand(cmdLine); isUnset {
					newState, output := s.Shell.HandleUnsetCommand(varName)
					s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
					if output != "" {
						s.Chat.AddMessage("system", output)
					}
					s.Shell = newState
					return nil, true
				}

				// Handle jobs command (show background jobs)
				if IsJobsCommand(cmdLine) {
					s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
					s.Chat.AddMessage("system", s.Shell.FormatJobsList())
					return nil, true
				}

				// Handle kill %N command (cancel background job)
				if isKill, jobID := IsKillCommand(cmdLine); isKill {
					s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
					if jobID == 0 {
						s.Chat.AddMessage("system", "Usage: kill %N (where N is the job number)")
						return nil, true
					}
					if s.Shell.Jobs == nil {
						s.Chat.AddMessage("system", "No jobs")
						return nil, true
					}
					if err := s.Shell.Jobs.CancelJob(jobID); err != nil {
						s.Chat.AddMessage("system", err.Error())
					} else {
						s.Chat.AddMessage("system", fmt.Sprintf("[%d] cancelled", jobID))
					}
					return nil, true
				}

				// Check if this is a background command (ends with &)
				if isBg, bgCmd := IsBackgroundCommand(cmdLine); isBg {
					expandedCmd := s.Shell.ExpandVariables(bgCmd)
					s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
					if s.Shell.Jobs == nil {
						s.Shell.Jobs = NewJobManager()
					}
					return executeBackgroundCmd(sessionKey, expandedCmd, s.Shell.CurrentDir, s.Shell.Jobs), true
				}

				// Expand variables in command before execution
				expandedCmd := s.Shell.ExpandVariables(cmdLine)

				// Regular command - show prompt with current directory and execute
				s.Chat.AddMessage("system", s.Shell.FormatPrompt(true)+cmdLine)
				return executeShellCmdWithEnv(sessionKey, expandedCmd, s.Shell.CurrentDir, s.Shell.EnvVars), true
			}
			return nil, true
		}

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
						"Shell Escape (! prefix):\n"+
						"! <cmd> - Execute shell command (tracks cwd)\n"+
						"! <cmd> & - Run command in background\n"+
						"! cd <dir> - Change working directory\n"+
						"! export VAR=value - Set environment variable\n"+
						"! export [VAR] - Show env var(s)\n"+
						"! unset VAR - Remove environment variable\n"+
						"! jobs - List background jobs\n"+
						"! kill %N - Cancel background job N\n\n"+
						"Variable expansion: $VAR or ${VAR} in commands\n"+
						"Output auto-truncates at 100 lines\n"+
						"Commands timeout after 5 minutes\n\n"+
						"Ctrl+T: New tab | Ctrl+W: Close tab\n"+
						"Alt+Left/Right: Switch tabs\n"+
						"Alt+Enter: Insert new line\n"+
						"Tab: Toggle sidebar | Shift+Tab: Cycle sidebar\n"+
						"PgUp/PgDn: Scroll chat\n"+
						"Ctrl+C: Cancel running command or quit")
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

// validateShellCommand checks if a command is allowed based on the security config
func (m *Model) validateShellCommand(cmdLine string) error {
	security := &m.config.ShellSecurity

	// Normalize command for matching (lowercase, trimmed)
	cmdLower := strings.ToLower(strings.TrimSpace(cmdLine))

	// Check allowlist first (if set, only allowed commands can run)
	if len(security.CommandAllowlist) > 0 {
		allowed := false
		for _, prefix := range security.CommandAllowlist {
			if strings.HasPrefix(cmdLower, strings.ToLower(prefix)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("command not in allowlist")
		}
	}

	// Check blocklist (always applied if present)
	for _, prefix := range security.CommandBlocklist {
		if strings.HasPrefix(cmdLower, strings.ToLower(prefix)) {
			return fmt.Errorf("command matches blocklist pattern: %s", prefix)
		}
	}

	return nil
}
