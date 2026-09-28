package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tui"
)

// sendToClient sends a protocol message to a WebSocket client (non-blocking)
func (g *Gateway) sendToClient(client *Client, msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message for client %s: %v", client.ID, err)
		return
	}

	select {
	case client.Send <- data:
	default:
		log.Printf("Client %s send buffer full, dropping message", client.ID)
	}
}

// sendErrorToClient sends an error response to a WebSocket client
func (g *Gateway) sendErrorToClient(client *Client, sessionKey, code, message string) {
	g.sendToClient(client, &protocol.ErrorResponse{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeErrorResponse,
			ID:        fmt.Sprintf("err_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: sessionKey,
		Code:       code,
		Message:    message,
	})
}

// handleWebSocketChat processes a chat message from a WebSocket client
func (g *Gateway) handleWebSocketChat(ctx context.Context, client *Client, msg *protocol.ChatMessage) {
	log.Printf("WebSocket chat from %s: %d chars (session: %s)", client.ID, len(msg.Text), msg.SessionKey)

	// Track activity
	if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.MarkActivity()
	}

	// Determine user ID: prefer message field, fall back to client field
	userID := msg.UserID
	if userID == "" {
		userID = client.UserID
	}
	if userID == "" {
		userID = client.Role // fall back to client name
	}

	// Determine session key
	sessionKey := msg.SessionKey
	if sessionKey == "" {
		sessionKey = client.SessionKey()
	}

	// Retrieve existing session by key, or create a new one.
	var session *sessions.Session
	var err error
	if sessionKey != "" {
		session, err = g.sessions.GetSession(sessionKey)
	}
	if session == nil {
		channelID := fmt.Sprintf("tui_%s", userID)
		session, err = g.sessions.GetOrCreateSession(userID, channelID)
	}
	if err != nil {
		log.Printf("Error getting session for WS client %s: %v", client.ID, err)
		g.sendErrorToClient(client, sessionKey, "session_error", "Failed to get or create session")
		return
	}

	// Update client's active session
	client.SetSessionKey(session.Key) // conduit-31jg.25

	// conduit-31jg.43: consume approval replies before the transcript and the
	// per-session turn lock (see approval_wiring.go).
	notify := g.wsApprovalNotifier(client, session.Key)
	if g.approvals.HandleReply(ctx, approval.Inbound{
		ChannelID: session.ChannelID, UserID: userID, SessionKey: session.Key,
		Text: msg.Text, Notify: notify,
	}) {
		return
	}

	// Check for commands
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		g.handleWebSocketCommandFromChat(ctx, client, session.Key, text)
		return
	}

	if g.ai == nil {
		g.sendErrorToClient(client, session.Key, "ai_error", "AI is not available")
		return
	}

	requestID := msg.RequestID
	if requestID == "" {
		requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}

	// conduit-31jg.35: the shared TurnRunner owns the turn lock, transcript
	// persistence (inside the lock, conduit-31jg.22), /stop registration
	// (conduit-31jg.23), SPAR reflection, cost and compaction.
	g.turns().Run(ctx, TurnRequest{
		Session:   session,
		ChannelID: session.ChannelID,
		UserID:    userID,
		Text:      msg.Text,
		Origin: &approval.Origin{ // conduit-31jg.43
			Source: "websocket", ChannelID: session.ChannelID, UserID: userID,
			SessionKey: session.Key, Notify: notify,
		},
		SanitizeStored: true,
	}, &wsTurnSink{g: g, client: client, sessionKey: session.Key, requestID: requestID})
}

// wsTurnSink renders a TurnRunner turn as the WebSocket streaming protocol
// (StreamStart / StreamDelta / ToolEvent / StreamEnd).
type wsTurnSink struct {
	g          *Gateway
	client     *Client
	sessionKey string
	requestID  string
}

// queuedNoticeText is the WS/TUI queued notice (conduit-31jg.66).
const queuedNoticeText = "Queued — I'll handle this right after the current request."

// Queued tells the client its message is waiting behind the session's
// running turn (conduit-31jg.66); its StreamStart arrives once that turn has
// finished. Sent as a command_response with command "queued", which older
// clients show as a plain system line.
func (s *wsTurnSink) Queued(context.Context) {
	s.g.sendToClient(s.client, &protocol.CommandResponse{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeCommandResponse,
			ID:        fmt.Sprintf("cr_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		Command:    tui.QueuedNoticeCommand,
		Response:   queuedNoticeText,
	})
}

func (s *wsTurnSink) Begin(context.Context) ai.StreamCallback {
	s.g.sendToClient(s.client, &protocol.StreamStart{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamStart,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
	})
	return func(delta string, done bool) {
		if delta == "" {
			return
		}
		s.g.sendToClient(s.client, &protocol.StreamDelta{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeStreamDelta,
				ID:        fmt.Sprintf("sd_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: s.sessionKey,
			RequestID:  s.requestID,
			Delta:      delta,
		})
	}
}

func (s *wsTurnSink) Progress(string) {}

func (s *wsTurnSink) ToolEvent(_ context.Context, event tools.ToolEventInfo) {
	s.g.sendToClient(s.client, &protocol.ToolEvent{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeToolEvent,
			ID:        fmt.Sprintf("te_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
		ToolName:   event.ToolName,
		EventType:  event.EventType,
		Args:       fmt.Sprintf("%v", event.Args),
		Result:     event.Result,
		Error:      event.Error,
		Duration:   event.Duration,
	})
}

func (s *wsTurnSink) streamEnd(content string, res *TurnResult) {
	end := &protocol.StreamEnd{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamEnd,
			ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
		Content:    content,
	}
	if res != nil && res.Usage != nil {
		end.PromptTokens = res.Usage.PromptTokens
		end.CompletionTokens = res.Usage.CompletionTokens
		end.TotalTokens = res.Usage.TotalTokens
		end.Model = res.Model
		end.RequestCost = res.RequestCost
		end.SessionCost = res.SessionCost
	}
	s.g.sendToClient(s.client, end)
}

func (s *wsTurnSink) Finish(_ context.Context, res *TurnResult) {
	switch {
	case res.Dropped:
		return // stopped while queued; nothing was started
	case res.Cancelled:
		log.Printf("WS request cancelled for session: %s", s.sessionKey)
		return
	case res.Err != nil:
		log.Printf("Error generating AI response for WS client: %v", res.Err)
		s.g.sendErrorToClient(s.client, s.sessionKey, "ai_error", ai.UserFriendlyError(res.Err))
		// Send StreamEnd with empty content to signal completion
		s.streamEnd("", nil)
		return
	}
	if !res.Delivered() {
		// Silent/empty: StreamEnd with empty content so the client stops its
		// streaming state.
		log.Printf("Silent response detected in WS chat (%d chars), suppressing", len(res.Raw))
		s.streamEnd("", res)
		return
	}
	// Sanitize internal markers — TUI doesn't support reply threading
	s.streamEnd(channels.SanitizeOutgoingText(res.Content), res)
}

// handleWebSocketCommand processes a slash command from a WebSocket client
func (g *Gateway) handleWebSocketCommand(ctx context.Context, client *Client, msg *protocol.CommandMessage) {
	sessionKey := msg.SessionKey
	if sessionKey == "" {
		sessionKey = client.SessionKey()
	}

	commandText := msg.Command
	if msg.Args != "" {
		commandText = msg.Command + " " + msg.Args
	}
	if !strings.HasPrefix(commandText, "/") {
		commandText = "/" + commandText
	}

	g.handleWebSocketCommandFromChat(ctx, client, sessionKey, commandText)
}

// handleWebSocketCommandFromChat handles a slash command that was detected in chat text
func (g *Gateway) handleWebSocketCommandFromChat(ctx context.Context, client *Client, sessionKey, text string) {
	text = strings.TrimSpace(text)
	command := strings.Fields(text)[0]

	sendResponse := func(response string) {
		g.sendToClient(client, &protocol.CommandResponse{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeCommandResponse,
				ID:        fmt.Sprintf("cr_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: sessionKey,
			Command:    command,
			Response:   response,
		})
	}

	switch {
	case text == "/goodbye" || text == "/end":
		// SPAR reflection: fire high-confidence reflection, then end session
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		g.handleReflectiveSessionEnd(ctx, client, sessionKey, sendResponse)

	case text == "/reset" || text == "/new" || strings.HasPrefix(text, "/reset ") || strings.HasPrefix(text, "/new "):
		if sessionKey == "" {
			sendResponse("No active session to reset.")
			return
		}

		// SPAR reflection: fire reflection BEFORE clearing context.
		// The model still has the full conversation context at this point.
		if g.sessionReflector != nil {
			if session, sErr := g.sessions.GetSession(sessionKey); sErr == nil && session.MessageCount > 2 {
				reflCtx, reflCancel := context.WithTimeout(ctx, 10*time.Second)
				g.reflectHighConfidencePost(reflCtx, session)
				reflCancel()
				log.Printf("SPAR reflection: pre-reset reflection written for session %s", sessionKey)
			}
		}

		if err := g.sessions.ClearSessionMessages(sessionKey); err != nil {
			log.Printf("Error clearing session: %v", err)
			sendResponse("Failed to reset session.")
			return
		}
		// Clear persisted context usage so /context reflects the reset
		_ = g.sessions.SetSessionContextBatch(sessionKey, map[string]string{
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
		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		messages, _ := g.sessions.GetMessages(session.Key, 1000)
		sendResponse(formatStatusResponse(session, len(messages), g.ai.GetUsageTracker(), g.ai.DefaultModel()))

	case text == "/help" || text == "/commands":
		help := "Available Commands:\n\n" +
			"/reset - Clear conversation history\n" +
			"/goodbye - End session with reflection\n" +
			"/end - Alias for /goodbye\n" +
			"/status - Show session info\n" +
			"/help - Show this message\n" +
			"/model [alias] - View/switch model\n" +
			"/provider [name] - View/switch provider\n" +
			"/context - Show context window usage\n" +
			"/cost - Show detailed cost breakdown\n" +
			"/compact - Compact context by summarizing older messages\n" +
			"/stop - Stop current operation\n" +
			"/quit - Exit TUI"
		sendResponse(help)

	case text == "/context" || strings.HasPrefix(text, "/context "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		sendResponse(formatContextUsage(session, g.ai.DefaultModel()))

	case text == "/cost" || strings.HasPrefix(text, "/cost "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}
		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session info.")
			return
		}
		sendResponse(formatCostResponse(session, g.ai.GetUsageTracker()))

	case text == "/provider" || strings.HasPrefix(text, "/provider "):
		parts := strings.Fields(text)
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}

		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session.")
			return
		}

		currentProvider := session.Context["provider"]
		if currentProvider == "" {
			currentProvider = g.ai.DefaultProviderName() + " (default)"
		}

		if len(parts) == 1 {
			providers := g.ai.ListProviders()
			var lines []string
			for _, p := range providers {
				lines = append(lines, fmt.Sprintf("  %s — %s (model: %s)", p.Name, p.Type, p.DefaultModel))
			}
			sendResponse(fmt.Sprintf("Current Provider: %s\n\nAvailable providers:\n%s\n\nUse /provider <name> to switch.", currentProvider, strings.Join(lines, "\n")))
			return
		}

		requested := parts[1]
		meta, exists := g.ai.GetProviderMeta(requested)
		if !exists {
			providers := g.ai.ListProviders()
			var names []string
			for _, p := range providers {
				names = append(names, p.Name)
			}
			sendResponse(fmt.Sprintf("Unknown provider: %s\n\nAvailable: %s", requested, strings.Join(names, ", ")))
			return
		}

		if err := g.sessions.SetSessionContext(sessionKey, "provider", requested); err != nil {
			sendResponse(fmt.Sprintf("Failed to switch provider: %v", err))
			return
		}
		sendResponse(fmt.Sprintf("Switched to provider %s (%s, model: %s)", meta.Name, meta.Type, meta.DefaultModel))

	case text == "/model" || strings.HasPrefix(text, "/model "):
		parts := strings.Fields(text)
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}

		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session.")
			return
		}

		currentModel := session.Context["model"]
		if currentModel == "" {
			currentModel = "sonnet (default)"
		}

		aliases := g.getModelAliases()

		if len(parts) == 1 {
			currentProvider := session.Context["provider"]
			if currentProvider == "" {
				currentProvider = g.ai.DefaultProviderName()
			}
			aliasDisplay := g.formatAliasDisplayWithProvider(aliases, "  ", " -> ")
			response := fmt.Sprintf("Current Model: %s\nProvider: %s\n\nAvailable aliases:\n%s\n\nUse /model <alias> to switch.", currentModel, currentProvider, aliasDisplay)
			sendResponse(response)
			return
		}

		requested := strings.ToLower(parts[1])
		sendModelResponse := func(response, model string) {
			g.sendToClient(client, &protocol.CommandResponse{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeCommandResponse,
					ID:        fmt.Sprintf("cr_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				SessionKey: sessionKey,
				Command:    command,
				Response:   response,
				Model:      model,
			})
		}

		// Helper to set model and auto-resolve provider
		wsSetModelAndResolve := func(model string) (string, error) {
			if err := g.sessions.SetSessionContext(sessionKey, "model", model); err != nil {
				return "", err
			}
			resolvedProvider := g.ai.ResolveProviderForModel(model)
			if resolvedProvider != "" {
				_ = g.sessions.SetSessionContext(sessionKey, "provider", resolvedProvider)
			}
			return resolvedProvider, nil
		}

		if fullModel, exists := aliases[requested]; exists {
			resolvedProvider, err := wsSetModelAndResolve(fullModel)
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
			resolvedProvider, err := wsSetModelAndResolve(requested)
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
			sendResponse(fmt.Sprintf("Unknown model alias: %s\n\nAvailable: %s", requested, formatAliasKeys(aliases)))
		}

	case text == "/stop":
		// conduit-31jg.23: running turn + queued turns (TurnRunner.Stop).
		resp, _ := stopResponse(g.turns().StopTree(sessionKey)) // conduit-31jg.84: + sub-agents
		sendResponse(resp)

	case text == "/compact" || strings.HasPrefix(text, "/compact "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}

		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session.")
			return
		}

		if g.compactionEngine == nil {
			sendResponse("Context compaction is not configured. Enable it in the AI config.")
			return
		}

		result, err := g.compactionEngine.Compact(ctx, session)
		if err != nil {
			sendResponse(fmt.Sprintf("Compaction failed: %v", err))
			return
		}

		if result == nil {
			sendResponse("No compaction needed (not enough messages to compact).")
			return
		}

		sendResponse(fmt.Sprintf("Compacted %d messages into summary + %d recent messages.", result.SummarizedCount, result.KeptCount))

	default:
		sendResponse(fmt.Sprintf("Unknown command: %s\nType /help for available commands.", command))
	}
}

// handleReflectiveSessionEnd handles /goodbye and /end commands: it sends the
// reflection prompt to the model so it can assess the session, then writes
// Go-computed metrics and clears the session.
//
// conduit-31jg.66: the reflection turn runs on the shared TurnRunner, so the
// prompt and reply are persisted inside the session's turn lock, the turn is
// /stop-able, and the metrics + session clear happen inside the same lock
// (goodbyeTurnSink.Finish) — a message queued behind /goodbye starts on the
// cleared session instead of racing the clear.
func (g *Gateway) handleReflectiveSessionEnd(ctx context.Context, client *Client, sessionKey string, sendResponse func(string)) {
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		sendResponse("Could not retrieve session.")
		return
	}

	// If reflection is available and the session has enough history, let
	// the model reflect before we tear down the context.
	if g.sessionReflector != nil && session.MessageCount > 2 {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			// Short timeout to avoid blocking the client if the model is slow.
			reflCtx, reflCancel := context.WithTimeout(ctx, 30*time.Second)
			defer reflCancel()

			userID := client.UserID
			if userID == "" {
				userID = client.Role
			}
			sink := &goodbyeTurnSink{wsTurnSink: wsTurnSink{
				g: g, client: client, sessionKey: sessionKey,
				requestID: fmt.Sprintf("refl_%d", time.Now().UnixNano()),
			}}
			g.turns().Run(reflCtx, TurnRequest{
				Session:   session,
				ChannelID: session.ChannelID,
				UserID:    userID,
				Text:      reflPrompt,
				// Transcript shows what the user typed, not the internal
				// prompt (matters if the reflection is stopped and the
				// session continues).
				StoreText: "/goodbye",
				Origin: &approval.Origin{ // conduit-31jg.43: the user typed /goodbye
					Source: "websocket", ChannelID: session.ChannelID, UserID: userID,
					SessionKey: sessionKey, Notify: g.wsApprovalNotifier(client, sessionKey),
				},
				SanitizeStored: true,
				SkipReflection: true, // this turn IS the reflection
			}, sink)
			switch {
			case !sink.ran:
				sendResponse("Session end cancelled.")
			case sink.clearErr != nil:
				sendResponse("Session reflection complete, but failed to clear session.")
			default:
				sendResponse("Session reflection complete. Goodbye!")
			}
			return
		}
	} else if g.sessionReflector != nil {
		// Session too short for model reflection — write Go-only metrics
		reflCtx, reflCancel := context.WithTimeout(ctx, 5*time.Second)
		g.reflectOnSessionEnd(reflCtx, sessionKey)
		reflCancel()
	}

	if err := g.clearEndedSession(sessionKey); err != nil {
		sendResponse("Session reflection complete, but failed to clear session.")
		return
	}
	sendResponse("Session reflection complete. Goodbye!")
}

// clearEndedSession clears the transcript and per-session usage context
// (same as /reset) after /goodbye.
func (g *Gateway) clearEndedSession(sessionKey string) error {
	if err := g.sessions.ClearSessionMessages(sessionKey); err != nil {
		log.Printf("Error clearing session after /goodbye: %v", err)
		return err
	}
	_ = g.sessions.SetSessionContextBatch(sessionKey, map[string]string{
		"last_prompt_tokens":        "",
		"last_completion_tokens":    "",
		"last_total_tokens":         "",
		"session_total_cost":        "",
		"session_request_count":     "",
		"session_unpriced_requests": "", // conduit-31jg.57
	})
	return nil
}

// goodbyeTurnSink renders the /goodbye reflection turn like a WS chat turn
// and, inside the turn lock, writes the session metrics and clears the
// session (conduit-31jg.66). As before, a failed (or timed-out) reflection
// still ends the session; a turn dropped before it ran (/stop while queued,
// shutdown) or stopped with /stop does not.
type goodbyeTurnSink struct {
	wsTurnSink
	ran      bool
	clearErr error
}

func (s *goodbyeTurnSink) Finish(ctx context.Context, res *TurnResult) {
	if res.Dropped {
		return
	}
	if res.Cancelled {
		s.streamEnd("", nil) // Begin sent StreamStart; close the client's stream
		return
	}
	s.ran = true
	content := ""
	if res.Delivered() {
		content = channels.SanitizeOutgoingText(res.Content)
	} else if res.Err != nil {
		log.Printf("SPAR reflection: session-end reflection failed for %s: %v", s.sessionKey, res.Err)
	}
	s.streamEnd(content, res)

	// Compute and write session metrics (after the reply was stored).
	metricsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	if updated, err := s.g.sessions.GetSession(s.sessionKey); err == nil {
		s.g.reflectHighConfidencePost(metricsCtx, updated)
	}
	cancel()
	log.Printf("SPAR reflection: session-end reflection completed for %s", s.sessionKey)

	s.clearErr = s.g.clearEndedSession(s.sessionKey)
}

// handleWebSocketSessionSwitch handles session management requests
func (g *Gateway) handleWebSocketSessionSwitch(client *Client, msg *protocol.SessionSwitch) {
	userID := msg.UserID
	if userID == "" {
		userID = client.UserID
	}
	if userID == "" {
		userID = client.Role
	}

	switch msg.Action {
	case "create":
		// Create a new session
		channelID := fmt.Sprintf("tui_%s_%d", userID, time.Now().UnixNano())
		session, err := g.sessions.GetOrCreateSession(userID, channelID)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Failed to create session: %v", err))
			return
		}
		client.SetSessionKey(session.Key) // conduit-31jg.25

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			Action:     "created",
			RequestID:  msg.RequestID, // Pass through the request ID for correlation
			CreatedAt:  session.CreatedAt,
		})

	case "switch":
		if msg.SessionKey == "" {
			g.sendErrorToClient(client, "", "invalid_request", "Session key required for switch")
			return
		}

		// Verify session exists
		session, err := g.sessions.GetSession(msg.SessionKey)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Session not found: %v", err))
			return
		}

		client.SetSessionKey(session.Key) // conduit-31jg.25

		// Get message history for the session
		messages, _ := g.sessions.GetMessages(session.Key, 100)
		var history []protocol.MessageInfo
		for _, m := range messages {
			history = append(history, protocol.MessageInfo{
				Role:      m.Role,
				Content:   m.Content,
				Timestamp: m.Timestamp,
			})
		}

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			Action:     "switched",
			Model:      session.Context["model"],
			CreatedAt:  session.CreatedAt,
			History:    history,
		})

	case "list":
		// Get all sessions for this user
		sessions, err := g.sessions.GetSessionsByUser(userID, 50)
		if err != nil {
			g.sendErrorToClient(client, "", "session_error", fmt.Sprintf("Failed to list sessions: %v", err))
			return
		}

		var sessionInfos []protocol.SessionInfo
		for _, s := range sessions {
			// Determine origin tag from channel ID
			origin := "TUI"
			if strings.HasPrefix(s.ChannelID, "telegram") {
				origin = "TG"
			} else if strings.HasPrefix(s.ChannelID, "ssh") {
				origin = "SSH"
			}

			sessionInfos = append(sessionInfos, protocol.SessionInfo{
				Key:          s.Key,
				UserID:       s.UserID,
				ChannelID:    s.ChannelID,
				CreatedAt:    s.CreatedAt,
				LastMessage:  s.UpdatedAt,
				MessageCount: s.MessageCount,
				Metadata:     map[string]string{"origin": origin},
			})
		}

		g.sendToClient(client, &protocol.SessionSwitch{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeSessionSwitch,
				ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			Action:   "list",
			Sessions: sessionInfos,
		})

	default:
		g.sendErrorToClient(client, "", "invalid_request", fmt.Sprintf("Unknown session action: %s", msg.Action))
	}
}
