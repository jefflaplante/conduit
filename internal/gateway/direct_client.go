package gateway

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"conduit/internal/ai"
	"conduit/internal/monitoring"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
	"conduit/internal/tui"
	"conduit/pkg/protocol"
)

// DirectClientConfig holds configuration for creating a DirectClient.
type DirectClientConfig struct {
	ParentCtx    context.Context
	UserID       string
	Sessions     *sessions.Store
	AI           *ai.Router
	Tools        *tools.Registry
	Metrics      monitoring.MetricsCollectorInterface
	ModelAliases map[string]string
	AgentName    string
	Version      string
	GitCommit    string
	UptimeFunc   func() int64
	ToolCount    int
	SkillCount   int
}

// DirectClient implements tui.GatewayClient for in-process communication.
// It bypasses the WebSocket layer entirely, calling gateway services directly.
type DirectClient struct {
	config           DirectClientConfig
	userID           string
	agentName        string
	inbox            chan tea.Msg
	sessions         *sessions.Store
	ai               *ai.Router
	tools            *tools.Registry
	metricsCollector monitoring.MetricsCollectorInterface
	modelAliases     map[string]string

	// Active request tracking for /stop
	activeRequests   map[string]context.CancelFunc
	activeRequestsMu sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc
}

// NewDirectClient creates a DirectClient that talks to gateway services in-process.
func NewDirectClient(cfg DirectClientConfig) *DirectClient {
	ctx, cancel := context.WithCancel(cfg.ParentCtx)
	return &DirectClient{
		config:           cfg,
		userID:           cfg.UserID,
		agentName:        cfg.AgentName,
		inbox:            make(chan tea.Msg, 256),
		sessions:         cfg.Sessions,
		ai:               cfg.AI,
		tools:            cfg.Tools,
		metricsCollector: cfg.Metrics,
		modelAliases:     cfg.ModelAliases,
		activeRequests:   make(map[string]context.CancelFunc),
		ctx:              ctx,
		cancel:           cancel,
	}
}

// ConnectCmd returns ConnectedMsg immediately — we're always "connected" in-process.
// Also sends GatewayInfoMsg with server metadata so the TUI shows it.
func (c *DirectClient) ConnectCmd() tea.Cmd {
	return func() tea.Msg {
		info := tui.GatewayInfoMsg{
			AssistantName: c.agentName,
			Version:       c.config.Version,
			GitCommit:     c.config.GitCommit,
			ModelAliases:  c.modelAliases,
			ToolCount:     c.config.ToolCount,
			SkillCount:    c.config.SkillCount,
		}
		if c.config.UptimeFunc != nil {
			info.UptimeSeconds = c.config.UptimeFunc()
		}
		c.send(info)
		return tui.ConnectedMsg{}
	}
}

// ListenCmd blocks on the inbox channel until the next message arrives.
func (c *DirectClient) ListenCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-c.inbox:
			return msg
		case <-c.ctx.Done():
			return tui.DisconnectedMsg{}
		}
	}
}

// ReconnectCmd returns ConnectedMsg immediately — always connected in-process.
func (c *DirectClient) ReconnectCmd(attempt int) tea.Cmd {
	return func() tea.Msg {
		return tui.ConnectedMsg{}
	}
}

// IsConnected always returns true while the context is alive.
func (c *DirectClient) IsConnected() bool {
	return c.ctx.Err() == nil
}

// Close cancels the context, which unblocks ListenCmd.
func (c *DirectClient) Close() {
	c.cancel()
}

// send enqueues a tea.Msg into the inbox (non-blocking, drops if full).
func (c *DirectClient) send(msg tea.Msg) {
	select {
	case c.inbox <- msg:
	default:
		log.Printf("[DirectClient] inbox full, dropping message %T", msg)
	}
}

// SendChat processes a chat message in-process.
func (c *DirectClient) SendChat(sessionKey, text string) error {
	requestID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	return c.SendChatWithID(sessionKey, text, requestID)
}

// SendChatWithID processes a chat message with a specific request ID for correlation.
func (c *DirectClient) SendChatWithID(sessionKey, text, requestID string) error {
	// Retrieve existing session by key, or create a new one.
	var session *sessions.Session
	var err error
	if sessionKey != "" {
		session, err = c.sessions.GetSession(sessionKey)
	}
	if session == nil {
		channelID := fmt.Sprintf("tui_%s", c.userID)
		session, err = c.sessions.GetOrCreateSession(c.userID, channelID)
	}
	if err != nil {
		c.send(tui.ErrorMsg{SessionKey: sessionKey, RequestID: requestID, Code: "session_error", Message: fmt.Sprintf("Failed to get session: %v", err)})
		return err
	}

	// Check for commands embedded in chat text
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/") {
		c.handleCommand(session.Key, trimmed)
		return nil
	}

	// Save user message
	if _, err := c.sessions.AddMessage(session.Key, "user", text, nil); err != nil {
		c.send(tui.ErrorMsg{SessionKey: session.Key, RequestID: requestID, Code: "save_error", Message: fmt.Sprintf("Failed to save message: %v", err)})
		return err
	}

	// Launch streaming chat in a goroutine with the provided request ID
	go c.streamChatWithID(session, text, requestID)

	return nil
}

// streamChat runs the AI generation loop and sends results to the inbox.
func (c *DirectClient) streamChat(session *sessions.Session, text string) {
	requestID := fmt.Sprintf("req_%d", time.Now().UnixNano())
	c.streamChatWithID(session, text, requestID)
}

func (c *DirectClient) streamChatWithID(session *sessions.Session, text, requestID string) {

	// Track activity
	if c.metricsCollector != nil {
		c.metricsCollector.MarkActivity()
	}

	// Create cancellable context
	reqCtx, cancel := context.WithCancel(c.ctx)
	reqCtx = types.WithRequestContext(reqCtx, session.ChannelID, c.userID, session.Key)

	// Track active request for /stop
	c.activeRequestsMu.Lock()
	c.activeRequests[session.Key] = cancel
	c.activeRequestsMu.Unlock()

	defer func() {
		c.activeRequestsMu.Lock()
		delete(c.activeRequests, session.Key)
		c.activeRequestsMu.Unlock()
	}()

	// Send StreamStart
	c.send(tui.StreamStartMsg{
		SessionKey: session.Key,
		RequestID:  requestID,
	})

	// Attach tool event callback
	reqCtx = tools.WithToolEventCallback(reqCtx, func(event tools.ToolEventInfo) {
		c.send(tui.ToolEventMsg{
			ToolEvent: protocol.ToolEvent{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeToolEvent,
					ID:        fmt.Sprintf("te_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				SessionKey: session.Key,
				RequestID:  requestID,
				ToolName:   event.ToolName,
				EventType:  event.EventType,
				Args:       formatToolArgs(event.Args),
				Result:     event.Result,
				Error:      event.Error,
				Duration:   event.Duration,
			},
		})
	})

	// Get model override
	modelOverride := session.Context["model"]

	// Stream deltas
	onDelta := func(delta string, done bool) {
		if delta != "" {
			c.send(tui.StreamDeltaMsg{
				SessionKey: session.Key,
				RequestID:  requestID,
				Delta:      delta,
			})
		}
	}

	convResponse, err := c.ai.GenerateResponseStreaming(reqCtx, session, text, modelOverride, onDelta)
	if err != nil {
		if reqCtx.Err() == context.Canceled {
			log.Printf("[DirectClient] Request cancelled for session: %s", session.Key)
			return
		}
		log.Printf("[DirectClient] AI error: %v", err)
		c.send(tui.ErrorMsg{SessionKey: session.Key, RequestID: requestID, Code: "ai_error", Message: "Failed to generate response"})
		c.send(tui.StreamEndMsg{
			SessionKey: session.Key,
			RequestID:  requestID,
			Content:    "",
		})
		return
	}

	var responseContent string
	var promptTokens, completionTokens, totalTokens int
	var requestCost, sessionCost float64
	if convResponse != nil {
		responseContent = convResponse.GetContent()
		if usage := convResponse.GetUsage(); usage != nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			totalTokens = usage.TotalTokens
			_ = c.sessions.SetSessionContext(session.Key, "last_prompt_tokens", strconv.Itoa(promptTokens))
			_ = c.sessions.SetSessionContext(session.Key, "last_completion_tokens", strconv.Itoa(completionTokens))
			_ = c.sessions.SetSessionContext(session.Key, "last_total_tokens", strconv.Itoa(totalTokens))

			// Accumulate session cost
			requestCost = ai.CalculateCost(modelOverride, promptTokens, completionTokens)
			prevCost, _ := strconv.ParseFloat(session.Context["session_total_cost"], 64)
			sessionCost = prevCost + requestCost
			prevCount, _ := strconv.Atoi(session.Context["session_request_count"])
			_ = c.sessions.SetSessionContext(session.Key, "session_total_cost", fmt.Sprintf("%.6f", sessionCost))
			_ = c.sessions.SetSessionContext(session.Key, "session_request_count", strconv.Itoa(prevCount+1))
		}
	}

	// Check for silent response tokens (NO_REPLY, HEARTBEAT_OK)
	if isSilentResponse(responseContent) {
		log.Printf("[DirectClient] Silent response detected (%d chars), suppressing", len(responseContent))
		// Send StreamEnd with empty content so TUI stops its streaming state
		c.send(tui.StreamEndMsg{
			SessionKey:       session.Key,
			RequestID:        requestID,
			Content:          "",
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      totalTokens,
			Model:            modelOverride,
			RequestCost:      requestCost,
			SessionCost:      sessionCost,
		})
		return
	}

	// Send StreamEnd with usage
	c.send(tui.StreamEndMsg{
		SessionKey:       session.Key,
		RequestID:        requestID,
		Content:          responseContent,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		Model:            modelOverride,
		RequestCost:      requestCost,
		SessionCost:      sessionCost,
	})

	// Save assistant message
	if responseContent != "" {
		if _, err := c.sessions.AddMessage(session.Key, "assistant", responseContent, nil); err != nil {
			log.Printf("[DirectClient] Error saving AI message: %v", err)
		}
	}
}

// SendCommand processes a slash command in-process.
func (c *DirectClient) SendCommand(sessionKey, command, args string) error {
	commandText := command
	if args != "" {
		commandText = command + " " + args
	}
	if !strings.HasPrefix(commandText, "/") {
		commandText = "/" + commandText
	}
	c.handleCommand(sessionKey, commandText)
	return nil
}

// handleCommand replicates the ws_chat.go command handling logic.
func (c *DirectClient) handleCommand(sessionKey, text string) {
	text = strings.TrimSpace(text)

	sendResponse := func(response string) {
		c.send(tui.CommandResponseMsg{
			SessionKey: sessionKey,
			Command:    strings.Fields(text)[0],
			Response:   response,
		})
	}

	switch {
	case text == "/reset" || text == "/new" || strings.HasPrefix(text, "/reset ") || strings.HasPrefix(text, "/new "):
		if sessionKey == "" {
			sendResponse("No active session to reset.")
			return
		}
		if err := c.sessions.ClearSessionMessages(sessionKey); err != nil {
			log.Printf("[DirectClient] Error clearing session: %v", err)
			sendResponse("Failed to reset session.")
			return
		}
		// Clear persisted context usage so /context reflects the reset
		_ = c.sessions.SetSessionContext(sessionKey, "last_prompt_tokens", "")
		_ = c.sessions.SetSessionContext(sessionKey, "last_completion_tokens", "")
		_ = c.sessions.SetSessionContext(sessionKey, "last_total_tokens", "")
		_ = c.sessions.SetSessionContext(sessionKey, "session_total_cost", "")
		_ = c.sessions.SetSessionContext(sessionKey, "session_request_count", "")
		sendResponse("Session reset. Fresh start!")

	case text == "/status" || strings.HasPrefix(text, "/status "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		session, err := c.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		messages, _ := c.sessions.GetMessages(session.Key, 1000)
		sendResponse(formatStatusResponse(session, len(messages), c.ai.GetUsageTracker()))

	case text == "/help" || text == "/commands":
		help := "Available Commands:\n\n" +
			"/reset - Clear conversation history\n" +
			"/status - Show session info\n" +
			"/help - Show this message\n" +
			"/model [alias] - View/switch model\n" +
			"/context - Show context window usage\n" +
			"/stop - Stop current operation\n" +
			"/quit - Exit TUI\n\n" +
			"Alt+Enter: Insert new line"
		sendResponse(help)

	case text == "/context" || strings.HasPrefix(text, "/context "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		session, err := c.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		sendResponse(formatContextUsage(session))

	case text == "/model" || strings.HasPrefix(text, "/model "):
		parts := strings.Fields(text)
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}

		session, err := c.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session.")
			return
		}

		currentModel := session.Context["model"]
		if currentModel == "" {
			currentModel = "sonnet (default)"
		}

		if len(parts) == 1 {
			aliasDisplay := formatAliasDisplay(c.modelAliases, "  ", " -> ")
			response := fmt.Sprintf("Current Model: %s\n\nAvailable aliases:\n%s\n\nUse /model <alias> to switch.", currentModel, aliasDisplay)
			sendResponse(response)
			return
		}

		requested := strings.ToLower(parts[1])
		sendModelResponse := func(response, model string) {
			c.send(tui.CommandResponseMsg{
				SessionKey: sessionKey,
				Command:    "/model",
				Response:   response,
				Model:      model,
			})
		}
		if fullModel, exists := c.modelAliases[requested]; exists {
			if err := c.sessions.SetSessionContext(sessionKey, "model", fullModel); err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			if fullModel == "" {
				sendModelResponse("Switched to default model (sonnet)", "")
			} else {
				sendModelResponse(fmt.Sprintf("Switched to %s (%s)", requested, fullModel), fullModel)
			}
		} else if strings.Contains(requested, "/") || strings.HasPrefix(requested, "claude-") {
			if err := c.sessions.SetSessionContext(sessionKey, "model", requested); err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			sendModelResponse(fmt.Sprintf("Switched to %s", requested), requested)
		} else {
			sendResponse(fmt.Sprintf("Unknown model alias: %s\n\nAvailable: %s", requested, formatAliasKeys(c.modelAliases)))
		}

	case text == "/stop":
		c.activeRequestsMu.RLock()
		cancelFn, exists := c.activeRequests[sessionKey]
		c.activeRequestsMu.RUnlock()

		if exists && cancelFn != nil {
			cancelFn()
			sendResponse("Stopping current operation...")
		} else {
			sendResponse("No active operation to stop.")
		}

	default:
		command := strings.Fields(text)[0]
		sendResponse(fmt.Sprintf("Unknown command: %s\nType /help for available commands.", command))
	}
}

// formatToolArgs formats a tool args map as "key=value, key=value".
// Values longer than 60 characters are truncated with "...".
func formatToolArgs(args map[string]interface{}) string {
	if len(args) == 0 {
		return ""
	}
	var parts []string
	for k, v := range args {
		s := fmt.Sprintf("%v", v)
		if len(s) > 60 {
			s = s[:57] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
	}
	return strings.Join(parts, ", ")
}

// CreateSession creates a new session and sends SessionCreatedMsg.
func (c *DirectClient) CreateSession() error {
	channelID := fmt.Sprintf("tui_%s_%d", c.userID, time.Now().UnixNano())
	session, err := c.sessions.GetOrCreateSession(c.userID, channelID)
	if err != nil {
		c.send(tui.ErrorMsg{Code: "session_error", Message: fmt.Sprintf("Failed to create session: %v", err)})
		return err
	}
	c.send(tui.SessionCreatedMsg{Key: session.Key, CreatedAt: session.CreatedAt})
	return nil
}

// CreateSessionWithID creates a new session with request ID correlation and sends SessionCreatedMsg.
func (c *DirectClient) CreateSessionWithID(requestID string) error {
	channelID := fmt.Sprintf("tui_%s_%d", c.userID, time.Now().UnixNano())
	session, err := c.sessions.GetOrCreateSession(c.userID, channelID)
	if err != nil {
		c.send(tui.ErrorMsg{RequestID: requestID, Code: "session_error", Message: fmt.Sprintf("Failed to create session: %v", err)})
		return err
	}
	c.send(tui.SessionCreatedMsg{
		Key:       session.Key,
		RequestID: requestID,
		CreatedAt: session.CreatedAt,
	})
	return nil
}

// SwitchSession switches to a session and sends its history.
func (c *DirectClient) SwitchSession(key string) error {
	session, err := c.sessions.GetSession(key)
	if err != nil {
		c.send(tui.ErrorMsg{SessionKey: key, Code: "session_error", Message: fmt.Sprintf("Session not found: %v", err)})
		return err
	}

	messages, _ := c.sessions.GetMessages(session.Key, 100)
	var history []protocol.MessageInfo
	for _, m := range messages {
		history = append(history, protocol.MessageInfo{
			Role:      m.Role,
			Content:   m.Content,
			Timestamp: m.Timestamp,
		})
	}

	c.send(tui.SessionSwitchedMsg{
		Key:       session.Key,
		History:   history,
		Model:     session.Context["model"],
		CreatedAt: session.CreatedAt,
	})
	return nil
}

// ListSessions lists the user's sessions and sends SessionListMsg.
func (c *DirectClient) ListSessions() error {
	userSessions, err := c.sessions.GetSessionsByUser(c.userID, 50)
	if err != nil {
		c.send(tui.ErrorMsg{Code: "session_error", Message: fmt.Sprintf("Failed to list sessions: %v", err)})
		return err
	}

	var infos []protocol.SessionInfo
	for _, s := range userSessions {
		origin := "SSH"
		if strings.HasPrefix(s.ChannelID, "telegram") {
			origin = "TG"
		} else if strings.HasPrefix(s.ChannelID, "tui_") {
			origin = "TUI"
		}

		infos = append(infos, protocol.SessionInfo{
			Key:          s.Key,
			UserID:       s.UserID,
			ChannelID:    s.ChannelID,
			CreatedAt:    s.CreatedAt,
			LastMessage:  s.UpdatedAt,
			MessageCount: s.MessageCount,
			Metadata:     map[string]string{"origin": origin},
		})
	}

	c.send(tui.SessionListMsg{Sessions: infos})
	return nil
}
