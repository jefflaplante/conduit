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
	"conduit/internal/channels"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
	"conduit/internal/protocol"
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
		sessionKey = client.SessionKey
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
	client.SessionKey = session.Key

	// Check for commands
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		g.handleWebSocketCommandFromChat(ctx, client, session.Key, text)
		return
	}

	// SPAR reflection: check for farewell or context budget trigger before sending to AI.
	isFarewell, _ := g.shouldTriggerReflection(text)
	isContextBudgetReflect := false

	// Save user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", msg.Text, nil)
	if err != nil {
		log.Printf("Error saving user message: %v", err)
		g.sendErrorToClient(client, session.Key, "save_error", "Failed to save message")
		return
	}

	// If farewell detected, append the reflection prompt to the message
	// so the model sees it in the same turn and can reflect before signing off.
	messageForAI := msg.Text
	if isFarewell {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			messageForAI = msg.Text + "\n\n[System: " + reflPrompt + "]"
			log.Printf("SPAR reflection: farewell detected, injecting reflection prompt for session %s", session.Key)
		}
	} else if session.Context["reflection_context_budget_triggered"] == "true" {
		if reflPrompt := g.reflectHighConfidencePre(); reflPrompt != "" {
			messageForAI = msg.Text + "\n\n[System: " + reflPrompt + "]"
			isContextBudgetReflect = true
			_ = g.sessions.SetSessionContextBatch(session.Key, map[string]string{
				"reflection_context_budget_triggered": "",
			})
			log.Printf("SPAR reflection: context budget triggered, injecting reflection prompt for session %s", session.Key)
		}
	}

	// Create cancellable context for this request
	reqCtx, cancel := context.WithCancel(ctx)
	reqCtx = types.WithRequestContext(reqCtx, session.ChannelID, userID, session.Key)

	// Track active request for /stop support
	g.ws.ActiveRequestsMu.Lock()
	g.ws.ActiveRequests[session.Key] = cancel
	requestCount := len(g.ws.ActiveRequests)
	g.ws.ActiveRequestsMu.Unlock()

	if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.UpdateActiveRequests(requestCount)
	}

	defer func() {
		g.ws.ActiveRequestsMu.Lock()
		delete(g.ws.ActiveRequests, session.Key)
		finalCount := len(g.ws.ActiveRequests)
		g.ws.ActiveRequestsMu.Unlock()
		if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
			g.monitoring.MetricsCollector.UpdateActiveRequests(finalCount)
		}
	}()

	requestID := msg.RequestID
	if requestID == "" {
		requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}

	// Send StreamStart
	g.sendToClient(client, &protocol.StreamStart{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamStart,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: session.Key,
		RequestID:  requestID,
	})

	// Set up tool event callback in context
	reqCtx = tools.WithToolEventCallback(reqCtx, func(event tools.ToolEventInfo) {
		g.sendToClient(client, &protocol.ToolEvent{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeToolEvent,
				ID:        fmt.Sprintf("te_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			RequestID:  requestID,
			ToolName:   event.ToolName,
			EventType:  event.EventType,
			Args:       fmt.Sprintf("%v", event.Args),
			Result:     event.Result,
			Error:      event.Error,
			Duration:   event.Duration,
		})
	})

	// Get model and provider overrides from session context
	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]

	// Try streaming first
	var responseContent string
	onDelta := func(delta string, done bool) {
		if delta != "" {
			g.sendToClient(client, &protocol.StreamDelta{
				BaseMessage: protocol.BaseMessage{
					Type:      protocol.TypeStreamDelta,
					ID:        fmt.Sprintf("sd_%d", time.Now().UnixNano()),
					Timestamp: time.Now(),
				},
				SessionKey: session.Key,
				RequestID:  requestID,
				Delta:      delta,
			})
		}
	}

	// Check if smart routing should be used
	smartRoutingEnabled := g.config.AI.SmartRouting != nil && g.config.AI.SmartRouting.Enabled
	if sessionSmartOverride := session.Context["smart_routing_enabled"]; sessionSmartOverride == "false" {
		smartRoutingEnabled = false
	} else if sessionSmartOverride == "true" {
		smartRoutingEnabled = true
	}

	var convResponse ai.ConversationResponse

	if smartRoutingEnabled && modelOverride == "" {
		// Use smart routing: let the router select the optimal model
		var routingResult *ai.SmartRoutingResult
		convResponse, routingResult, err = g.ai.GenerateResponseSmartStreaming(reqCtx, session, messageForAI, providerOverride, onDelta)
		if routingResult != nil {
			// Store smart routing metadata in session context for debugging/visibility
			_ = g.sessions.SetSessionContext(session.Key, "smart_routing_model", routingResult.SelectedModel)
			_ = g.sessions.SetSessionContext(session.Key, "smart_routing_reason", routingResult.SelectionReason)
			_ = g.sessions.SetSessionContext(session.Key, "smart_routing_complexity", fmt.Sprintf("%d", routingResult.Complexity.Score))
			// Use the selected model for cost calculation
			modelOverride = routingResult.SelectedModel
		}
	} else {
		// Use direct streaming with explicit model (or default)
		convResponse, err = g.ai.GenerateResponseStreaming(reqCtx, session, messageForAI, providerOverride, modelOverride, onDelta)
	}
	if err != nil {
		// Check for cancellation from /stop
		if reqCtx.Err() == context.Canceled {
			log.Printf("WS request cancelled for session: %s", session.Key)
			return
		}

		log.Printf("Error generating AI response for WS client: %v", err)
		g.sendErrorToClient(client, session.Key, "ai_error", ai.UserFriendlyError(err))

		// Send StreamEnd with empty content to signal completion
		g.sendToClient(client, &protocol.StreamEnd{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeStreamEnd,
				ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: session.Key,
			RequestID:  requestID,
			Content:    "",
		})
		return
	}

	if convResponse != nil {
		responseContent = convResponse.GetContent()
	}

	// Sanitize internal markers — TUI doesn't support reply threading
	responseContent = channels.SanitizeOutgoingText(responseContent)

	// Extract usage and persist to session context
	var promptTokens, completionTokens, totalTokens int
	var requestCost, sessionCost float64
	if convResponse != nil {
		if usage := convResponse.GetUsage(); usage != nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			totalTokens = usage.TotalTokens

			// Proactive context window warning
			warning := contextWarningIfNeeded(session, promptTokens, modelOverride)
			if warning.Text != "" {
				responseContent += warning.Text
			}

			// Accumulate session cost
			requestCost = ai.CalculateCost(modelOverride, promptTokens, completionTokens)
			prevCost, _ := strconv.ParseFloat(session.Context["session_total_cost"], 64)
			sessionCost = prevCost + requestCost
			prevCount, _ := strconv.Atoi(session.Context["session_request_count"])

			// Token usage recording is router-level since bd-27hs —
			// GenerateResponseStreaming records last_* and cumulative totals
			// inside the turn lock. This block now only handles path-local
			// concerns: context warning, session cost, request count.

			batch := map[string]string{
				"session_total_cost":    fmt.Sprintf("%.6f", sessionCost),
				"session_request_count": strconv.Itoa(prevCount + 1),
			}
			if warning.Text != "" {
				batch[warning.Key] = "true"
				// SPAR: trigger reflection on next message when context budget >= 80%
				if warning.Key == "context_warned_80" && g.sessionReflector != nil {
					batch["reflection_context_budget_triggered"] = "true"
				}
			}
			_ = g.sessions.SetSessionContextBatch(session.Key, batch)

			// Auto-compaction check (async, non-blocking)
			// Determine actual model used for context window calculation
			modelUsed := modelOverride
			if modelUsed == "" {
				modelUsed = "claude-sonnet-4-20250514" // default model
			}
			if g.compactionEngine != nil && g.compactionEngine.ShouldCompact(promptTokens, modelUsed) {
				go func() {
					compactCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					if _, err := g.compactionEngine.Compact(compactCtx, session); err != nil {
						log.Printf("[Compaction] Auto-compact failed for session %s: %v", session.Key, err)
					}
				}()
			}
		}
	}

	// Check for silent response tokens (NO_REPLY, HEARTBEAT_OK)
	if channels.IsSilentResponse(responseContent) {
		log.Printf("Silent response detected in WS chat (%d chars), suppressing", len(responseContent))
		// Send StreamEnd with empty content so TUI stops its streaming state
		g.sendToClient(client, &protocol.StreamEnd{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeStreamEnd,
				ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
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

	// Send StreamEnd with final content and usage
	g.sendToClient(client, &protocol.StreamEnd{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamEnd,
			ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
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

	// Save assistant message to session
	if responseContent != "" {
		_, err = g.sessions.AddMessage(session.Key, "assistant", responseContent, nil)
		if err != nil {
			log.Printf("Error saving AI message: %v", err)
		}
	}

	// SPAR reflection: after model responds to a farewell or context budget trigger,
	// compute and write session metrics (high-confidence path: model was available to reflect).
	if isFarewell || isContextBudgetReflect {
		// Re-fetch session to get updated message count after saving the response
		if updatedSession, sErr := g.sessions.GetSession(session.Key); sErr == nil {
			g.reflectHighConfidencePost(ctx, updatedSession)
		}
	}
}

// handleWebSocketCommand processes a slash command from a WebSocket client
func (g *Gateway) handleWebSocketCommand(ctx context.Context, client *Client, msg *protocol.CommandMessage) {
	sessionKey := msg.SessionKey
	if sessionKey == "" {
		sessionKey = client.SessionKey
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
		sendResponse(formatStatusResponse(session, len(messages), g.ai.GetUsageTracker()))

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
		sendResponse(formatContextUsage(session))

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
		g.ws.ActiveRequestsMu.RLock()
		cancel, exists := g.ws.ActiveRequests[sessionKey]
		g.ws.ActiveRequestsMu.RUnlock()

		if exists && cancel != nil {
			cancel()
			sendResponse("Stopping current operation...")
		} else {
			sendResponse("No active operation to stop.")
		}

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
		client.SessionKey = session.Key

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

		client.SessionKey = session.Key

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
