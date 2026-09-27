package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/monitoring"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tui"
)

// DirectClientConfig holds configuration for creating a DirectClient.
type DirectClientConfig struct {
	ParentCtx    context.Context
	Approvals    *approval.Manager // conduit-31jg.43; nil => owner sends fail closed
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
	// Turns is the gateway's shared turn pipeline (conduit-31jg.35). nil
	// builds a private runner over Sessions/AI whose running-turn registry
	// is this client's activeRequests map.
	Turns *TurnRunner
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

	// turns runs chat turns (conduit-31jg.35).
	turns *TurnRunner

	// Dropped message tracking
	droppedMessages atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
}

// NewDirectClient creates a DirectClient that talks to gateway services in-process.
func NewDirectClient(cfg DirectClientConfig) *DirectClient {
	ctx, cancel := context.WithCancel(cfg.ParentCtx)
	c := &DirectClient{
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
		turns:            cfg.Turns,
	}
	if c.turns == nil {
		c.turns = NewTurnRunner(cfg.Sessions, cfg.AI, nil, nil,
			activeTurnRegistry{
				mu:  &c.activeRequestsMu,
				get: func() map[string]context.CancelFunc { return c.activeRequests },
			},
			func() monitoring.MetricsCollectorInterface { return cfg.Metrics }, nil)
	}
	return c
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
		dropped := c.droppedMessages.Add(1)
		log.Printf("[DirectClient] WARNING: inbox full, dropping message %T (total dropped: %d)", msg, dropped)
	}
}

// DroppedMessages returns the total number of messages dropped due to a full inbox.
func (c *DirectClient) DroppedMessages() int64 {
	return c.droppedMessages.Load()
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

	// conduit-31jg.43: consume approval replies before the transcript and the
	// per-session turn lock (see approval_wiring.go).
	if c.config.Approvals.HandleReply(c.ctx, approval.Inbound{
		ChannelID: session.ChannelID, UserID: c.userID, SessionKey: session.Key,
		Text: text, Notify: c.directApprovalNotifier(session.Key),
	}) {
		return nil
	}

	// Check for commands embedded in chat text
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/") {
		c.handleCommand(session.Key, trimmed)
		return nil
	}

	// conduit-31jg.35: persistence now happens inside the turn lock in the
	// shared TurnRunner (conduit-31jg.22), so the whole turn runs async.
	go c.streamChatWithID(session, text, requestID)

	return nil
}

// streamChatWithID runs one turn through the shared TurnRunner and renders it
// into the TUI inbox.
func (c *DirectClient) streamChatWithID(session *sessions.Session, text, requestID string) {
	// Track activity
	if c.metricsCollector != nil {
		c.metricsCollector.MarkActivity()
	}
	c.turns.Run(c.ctx, TurnRequest{
		Session:   session,
		ChannelID: session.ChannelID,
		UserID:    c.userID,
		Text:      text,
		Origin: &approval.Origin{ // conduit-31jg.43
			Source: "tui", ChannelID: session.ChannelID, UserID: c.userID,
			SessionKey: session.Key, Notify: c.directApprovalNotifier(session.Key),
		},
		SanitizeStored: true,
	}, &directTurnSink{c: c, session: session, requestID: requestID})
}

// directTurnSink renders a TurnRunner turn as TUI inbox messages.
type directTurnSink struct {
	c         *DirectClient
	session   *sessions.Session
	requestID string
}

// Queued: the TUI never had a busy-ack; the queued turn's StreamStart arrives
// once the previous turn finished.
func (s *directTurnSink) Queued(context.Context) {}

func (s *directTurnSink) Begin(context.Context) ai.StreamCallback {
	s.c.send(tui.StreamStartMsg{SessionKey: s.session.Key, RequestID: s.requestID})
	return func(delta string, done bool) {
		if delta != "" {
			s.c.send(tui.StreamDeltaMsg{SessionKey: s.session.Key, RequestID: s.requestID, Delta: delta})
		}
	}
}

func (s *directTurnSink) Progress(string) {}

func (s *directTurnSink) ToolEvent(_ context.Context, event tools.ToolEventInfo) {
	s.c.send(tui.ToolEventMsg{
		ToolEvent: protocol.ToolEvent{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeToolEvent,
				ID:        fmt.Sprintf("te_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: s.session.Key,
			RequestID:  s.requestID,
			ToolName:   event.ToolName,
			EventType:  event.EventType,
			Args:       formatToolArgs(event.Args),
			Result:     event.Result,
			Error:      event.Error,
			Duration:   event.Duration,
		},
	})
}

func (s *directTurnSink) Finish(_ context.Context, res *TurnResult) {
	key := s.session.Key
	switch {
	case res.Dropped:
		return // stopped while queued; nothing was started
	case res.Cancelled:
		log.Printf("[DirectClient] Request cancelled for session: %s", key)
		return
	case res.Err != nil:
		log.Printf("[DirectClient] AI error: %v", res.Err)
		s.c.send(tui.ErrorMsg{SessionKey: key, RequestID: s.requestID, Code: "ai_error", Message: ai.UserFriendlyError(res.Err)})
		s.c.send(tui.StreamEndMsg{SessionKey: key, RequestID: s.requestID, Content: ""})
		return
	}

	end := tui.StreamEndMsg{
		SessionKey:    key,
		RequestID:     s.requestID,
		Model:         res.Model,
		ContextWindow: s.c.getContextWindow(s.session.Context["provider"], res.Model),
		RequestCost:   res.RequestCost,
		SessionCost:   res.SessionCost,
	}
	if res.Usage != nil {
		end.PromptTokens = res.Usage.PromptTokens
		end.CompletionTokens = res.Usage.CompletionTokens
		end.TotalTokens = res.Usage.TotalTokens
	}
	if res.Delivered() {
		// Sanitize internal markers before sending to TUI
		end.Content = channels.SanitizeOutgoingText(res.Content)
	} else {
		// Silent/empty: StreamEnd with empty content so the TUI stops its
		// streaming state.
		log.Printf("[DirectClient] Silent response detected (%d chars), suppressing", len(res.Raw))
	}
	s.c.send(end)
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
		_ = c.sessions.SetSessionContextBatch(sessionKey, map[string]string{
			"last_prompt_tokens":        "",
			"last_completion_tokens":    "",
			"last_total_tokens":         "",
			"session_total_cost":        "",
			"session_request_count":     "",
			"session_unpriced_requests": "", // conduit-31jg.57
		})
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
		sendResponse(formatStatusResponse(session, len(messages), c.ai.GetUsageTracker(), c.ai.DefaultModel()))

	case text == "/help" || text == "/commands":
		help := "Available Commands:\n\n" +
			"/reset - Clear conversation history\n" +
			"/status - Show session info\n" +
			"/help - Show this message\n" +
			"/model [alias] - View/switch model\n" +
			"/provider [name] - View/switch provider\n" +
			"/context - Show context window usage\n" +
			"/cost - Show detailed cost breakdown\n" +
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
		sendResponse(formatContextUsage(session, c.ai.DefaultModel()))

	case text == "/cost" || strings.HasPrefix(text, "/cost "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		session, err := c.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		sendResponse(formatCostResponse(session, c.ai.GetUsageTracker()))

	case text == "/provider" || strings.HasPrefix(text, "/provider "):
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

		currentProvider := session.Context["provider"]
		if currentProvider == "" {
			currentProvider = c.ai.DefaultProviderName() + " (default)"
		}

		if len(parts) == 1 {
			providers := c.ai.ListProviders()
			var lines []string
			for _, p := range providers {
				lines = append(lines, fmt.Sprintf("  %s — %s (model: %s)", p.Name, p.Type, p.DefaultModel))
			}
			sendResponse(fmt.Sprintf("Current Provider: %s\n\nAvailable providers:\n%s\n\nUse /provider <name> to switch.", currentProvider, strings.Join(lines, "\n")))
			return
		}

		requested := parts[1]
		meta, exists := c.ai.GetProviderMeta(requested)
		if !exists {
			providers := c.ai.ListProviders()
			var names []string
			for _, p := range providers {
				names = append(names, p.Name)
			}
			sendResponse(fmt.Sprintf("Unknown provider: %s\n\nAvailable: %s", requested, strings.Join(names, ", ")))
			return
		}

		if err := c.sessions.SetSessionContext(sessionKey, "provider", requested); err != nil {
			sendResponse(fmt.Sprintf("Failed to switch provider: %v", err))
			return
		}
		// Also switch the model to the new provider's default so we don't send
		// an incompatible model name (e.g. a Claude model to Ollama).
		if meta.DefaultModel != "" {
			_ = c.sessions.SetSessionContext(sessionKey, "model", meta.DefaultModel)
		}
		sendResponse(fmt.Sprintf("Switched to provider %s (%s, model: %s)", meta.Name, meta.Type, meta.DefaultModel))

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
			currentProvider := session.Context["provider"]
			if currentProvider == "" {
				currentProvider = c.ai.DefaultProviderName()
			}
			aliasDisplay := c.formatAliasDisplayWithProvider(c.modelAliases, "  ", " -> ")
			response := fmt.Sprintf("Current Model: %s\nProvider: %s\n\nAvailable aliases:\n%s\n\nUse /model <alias> to switch.", currentModel, currentProvider, aliasDisplay)
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

		// Helper to set model and auto-resolve provider
		dcSetModelAndResolve := func(model string) (string, error) {
			if err := c.sessions.SetSessionContext(sessionKey, "model", model); err != nil {
				return "", err
			}
			resolvedProvider := c.ai.ResolveProviderForModel(model)
			if resolvedProvider != "" {
				_ = c.sessions.SetSessionContext(sessionKey, "provider", resolvedProvider)
			}
			return resolvedProvider, nil
		}

		if fullModel, exists := c.modelAliases[requested]; exists {
			resolvedProvider, err := dcSetModelAndResolve(fullModel)
			if err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			if fullModel == "" {
				sendModelResponse("Switched to default model (sonnet)", "")
			} else {
				suffix := ""
				if resolvedProvider != "" {
					suffix = " on " + resolvedProvider
				}
				sendModelResponse(fmt.Sprintf("Switched to %s (%s)%s", requested, fullModel, suffix), fullModel)
			}
		} else if strings.Contains(requested, "/") || len(requested) > 3 {
			resolvedProvider, err := dcSetModelAndResolve(requested)
			if err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			suffix := ""
			if resolvedProvider != "" {
				suffix = " on " + resolvedProvider
			}
			sendModelResponse(fmt.Sprintf("Switched to %s%s", requested, suffix), requested)
		} else {
			sendResponse(fmt.Sprintf("Unknown model alias: %s\n\nAvailable: %s", requested, formatAliasKeys(c.modelAliases)))
		}

	case text == "/stop":
		// conduit-31jg.23: running turn + queued turns (TurnRunner.Stop).
		resp, _ := stopResponse(c.turns.Stop(sessionKey))
		sendResponse(resp)

	default:
		command := strings.Fields(text)[0]
		sendResponse(fmt.Sprintf("Unknown command: %s\nType /help for available commands.", command))
	}
}

// formatAliasDisplayWithProvider returns a multi-line display of aliases with resolved providers.
func (c *DirectClient) formatAliasDisplayWithProvider(aliases map[string]string, prefix, arrow string) string {
	var lines []string
	for alias, model := range aliases {
		display := model
		if display == "" {
			display = "reset to default"
		}
		providerName := c.ai.ResolveProviderForModel(model)
		if providerName == "" {
			providerName = c.ai.DefaultProviderName()
		}
		lines = append(lines, fmt.Sprintf("%s%s %s %s (%s)", prefix, alias, arrow, display, providerName))
	}
	return strings.Join(lines, "\n")
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

// getContextWindow returns the context window size for a provider/model combination.
// It prefers the provider's configured context_window, falling back to model-based detection.
func (c *DirectClient) getContextWindow(providerName, model string) int {
	// Resolve provider if not specified
	if providerName == "" && model != "" {
		providerName = c.ai.ResolveProviderForModel(model)
	}
	if providerName == "" {
		providerName = c.ai.DefaultProviderName()
	}

	// Check provider config for explicit context_window
	if meta, ok := c.ai.GetProviderMeta(providerName); ok && meta.ContextWindow > 0 {
		return meta.ContextWindow
	}

	// Fall back to model-based detection
	return ai.ContextWindowForModel(model)
}
