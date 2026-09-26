package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
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

	// Check if smart routing should be used
	smartRoutingEnabled := g.config != nil && g.config.AI.SmartRouting != nil && g.config.AI.SmartRouting.Enabled
	if sessionSmartOverride := session.Context["smart_routing_enabled"]; sessionSmartOverride == "false" {
		smartRoutingEnabled = false
	} else if sessionSmartOverride == "true" {
		smartRoutingEnabled = true
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
		SmartRouting:   smartRoutingEnabled,
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

// Queued: WebSocket chat never had a busy-ack; the StreamStart for the queued
// turn simply arrives once the previous turn has finished.
func (s *wsTurnSink) Queued(context.Context) {}

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
			"last_prompt_tokens":     "",
			"last_completion_tokens": "",
			"last_total_tokens":      "",
			"session_total_cost":     "",
			"session_request_count":  "",
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
			"/smartroute [on|off|status|budget <amount>] - Smart routing controls\n" +
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
		resp, _ := stopResponse(g.turns().Stop(sessionKey))
		sendResponse(resp)

	case text == "/smartroute" || strings.HasPrefix(text, "/smartroute "):
		if sessionKey == "" {
			sendResponse("No active session.")
			return
		}

		session, err := g.sessions.GetSession(sessionKey)
		if err != nil {
			sendResponse("Could not retrieve session.")
			return
		}

		parts := strings.Fields(text)
		subcommand := ""
		if len(parts) > 1 {
			subcommand = strings.ToLower(parts[1])
		}

		switch subcommand {
		case "", "status":
			enabled := "off (global)"
			if g.config.AI.SmartRouting != nil && g.config.AI.SmartRouting.Enabled {
				enabled = "on (global)"
			}
			if override := session.Context["smart_routing_enabled"]; override != "" {
				if override == "true" {
					enabled = "on (session)"
				} else {
					enabled = "off (session)"
				}
			}

			model := session.Context["smart_routing_model"]
			reason := session.Context["smart_routing_reason"]
			complexity := session.Context["smart_routing_complexity"]
			cost := session.Context["session_total_cost"]

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Smart Routing: %s\n", enabled))
			if model != "" {
				sb.WriteString(fmt.Sprintf("Last model: %s\n", model))
			}
			if complexity != "" {
				sb.WriteString(fmt.Sprintf("Complexity score: %s\n", complexity))
			}
			if reason != "" {
				sb.WriteString(fmt.Sprintf("Selection reason: %s\n", reason))
			}
			if cost != "" {
				sb.WriteString(fmt.Sprintf("Session cost: $%s\n", cost))
			}
			if g.config.AI.SmartRouting != nil && g.config.AI.SmartRouting.CostBudgetDaily > 0 {
				sb.WriteString(fmt.Sprintf("Daily budget: $%.2f\n", g.config.AI.SmartRouting.CostBudgetDaily))
			}
			sendResponse(sb.String())

		case "on":
			_ = g.sessions.SetSessionContext(sessionKey, "smart_routing_enabled", "true")
			sendResponse("Smart routing enabled for this session.")

		case "off":
			_ = g.sessions.SetSessionContext(sessionKey, "smart_routing_enabled", "false")
			sendResponse("Smart routing disabled for this session. Using default model.")

		case "budget":
			if len(parts) < 3 {
				sendResponse("Usage: /smartroute budget <amount>")
				return
			}
			amount := parts[2]
			if _, err := strconv.ParseFloat(amount, 64); err != nil {
				sendResponse(fmt.Sprintf("Invalid budget amount: %s", amount))
				return
			}
			_ = g.sessions.SetSessionContext(sessionKey, "smart_routing_budget", amount)
			sendResponse(fmt.Sprintf("Session budget set to $%s.", amount))

		default:
			sendResponse("Usage: /smartroute [on|off|status|budget <amount>]")
		}

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
func (g *Gateway) handleReflectiveSessionEnd(ctx context.Context, client *Client, sessionKey string, sendResponse func(string)) {
	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		sendResponse("Could not retrieve session.")
		return
	}

	// If reflection is available and the session has enough history, let
	// the model reflect before we tear down the context.
	if g.sessionReflector != nil && session.MessageCount > 2 {
		reflPrompt := g.reflectHighConfidencePre()
		if reflPrompt != "" {
			// Send the reflection prompt as a user message to the model so it
			// can introspect on the conversation. We use a short timeout to
			// avoid blocking the client if the model is slow.
			reflCtx, reflCancel := context.WithTimeout(ctx, 30*time.Second)
			defer reflCancel()

			modelOverride := session.Context["model"]
			providerOverride := session.Context["provider"]

			// Send a StreamStart so the TUI knows a response is coming
			requestID := fmt.Sprintf("refl_%d", time.Now().UnixNano())
			g.sendToClient(client, &protocol.StreamStart{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeStreamStart,
					ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				SessionKey: sessionKey,
				RequestID:  requestID,
			})

			onDelta := func(delta string, done bool) {
				if delta != "" {
					g.sendToClient(client, &protocol.StreamDelta{
						BaseMessage: protocol.BaseMessage{
							Type:      protocol.TypeStreamDelta,
							ID:        fmt.Sprintf("sd_%d", time.Now().UnixNano()),
							Timestamp: time.Now(),
						},
						SessionKey: sessionKey,
						RequestID:  requestID,
						Delta:      delta,
					})
				}
			}

			convResponse, aiErr := g.ai.GenerateResponseStreaming(reflCtx, session, reflPrompt, providerOverride, modelOverride, onDelta)

			var reflContent string
			if aiErr == nil && convResponse != nil {
				reflContent = convResponse.GetContent()
			}

			// Send StreamEnd with the reflection response
			g.sendToClient(client, &protocol.StreamEnd{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeStreamEnd,
					ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				SessionKey: sessionKey,
				RequestID:  requestID,
				Content:    reflContent,
			})

			// Save the reflection response
			if reflContent != "" {
				_, _ = g.sessions.AddMessage(sessionKey, "assistant", reflContent, nil)
			}

			// Compute and write session metrics
			if updatedSession, sErr := g.sessions.GetSession(sessionKey); sErr == nil {
				g.reflectHighConfidencePost(reflCtx, updatedSession)
			}

			log.Printf("SPAR reflection: session-end reflection completed for %s", sessionKey)
		}
	} else if g.sessionReflector != nil {
		// Session too short for model reflection — write Go-only metrics
		reflCtx, reflCancel := context.WithTimeout(ctx, 5*time.Second)
		g.reflectOnSessionEnd(reflCtx, sessionKey)
		reflCancel()
	}

	// Clear the session (same as /reset)
	if err := g.sessions.ClearSessionMessages(sessionKey); err != nil {
		log.Printf("Error clearing session after /goodbye: %v", err)
		sendResponse("Session reflection complete, but failed to clear session.")
		return
	}
	_ = g.sessions.SetSessionContextBatch(sessionKey, map[string]string{
		"last_prompt_tokens":     "",
		"last_completion_tokens": "",
		"last_total_tokens":      "",
		"session_total_cost":     "",
		"session_request_count":  "",
	})
	sendResponse("Session reflection complete. Goodbye!")
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
