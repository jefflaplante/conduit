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
	"conduit/pkg/protocol"
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
	if g.metricsCollector != nil {
		g.metricsCollector.MarkActivity()
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

	// Save user message to session
	_, err = g.sessions.AddMessage(session.Key, "user", msg.Text, nil)
	if err != nil {
		log.Printf("Error saving user message: %v", err)
		g.sendErrorToClient(client, session.Key, "save_error", "Failed to save message")
		return
	}

	// Create cancellable context for this request
	reqCtx, cancel := context.WithCancel(ctx)
	reqCtx = types.WithRequestContext(reqCtx, session.ChannelID, userID, session.Key)

	// Track active request for /stop support
	g.activeRequestsMu.Lock()
	g.activeRequests[session.Key] = cancel
	requestCount := len(g.activeRequests)
	g.activeRequestsMu.Unlock()

	if g.metricsCollector != nil {
		g.metricsCollector.UpdateActiveRequests(requestCount)
	}

	defer func() {
		g.activeRequestsMu.Lock()
		delete(g.activeRequests, session.Key)
		finalCount := len(g.activeRequests)
		g.activeRequestsMu.Unlock()
		if g.metricsCollector != nil {
			g.metricsCollector.UpdateActiveRequests(finalCount)
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

	// Get model override from session context
	modelOverride := session.Context["model"]

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

	convResponse, err := g.ai.GenerateResponseStreaming(reqCtx, session, msg.Text, modelOverride, onDelta)
	if err != nil {
		// Check for cancellation from /stop
		if reqCtx.Err() == context.Canceled {
			log.Printf("WS request cancelled for session: %s", session.Key)
			return
		}

		log.Printf("Error generating AI response for WS client: %v", err)
		g.sendErrorToClient(client, session.Key, "ai_error", "Failed to generate response")

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

	// Strip reply tags — TUI doesn't support reply threading
	responseContent = channels.StripReplyTags(responseContent)

	// Extract usage and persist to session context
	var promptTokens, completionTokens, totalTokens int
	var requestCost, sessionCost float64
	if convResponse != nil {
		if usage := convResponse.GetUsage(); usage != nil {
			promptTokens = usage.PromptTokens
			completionTokens = usage.CompletionTokens
			totalTokens = usage.TotalTokens
			_ = g.sessions.SetSessionContext(session.Key, "last_prompt_tokens", strconv.Itoa(promptTokens))
			_ = g.sessions.SetSessionContext(session.Key, "last_completion_tokens", strconv.Itoa(completionTokens))
			_ = g.sessions.SetSessionContext(session.Key, "last_total_tokens", strconv.Itoa(totalTokens))

			// Accumulate session cost
			requestCost = ai.CalculateCost(modelOverride, promptTokens, completionTokens)
			prevCost, _ := strconv.ParseFloat(session.Context["session_total_cost"], 64)
			sessionCost = prevCost + requestCost
			prevCount, _ := strconv.Atoi(session.Context["session_request_count"])
			_ = g.sessions.SetSessionContext(session.Key, "session_total_cost", fmt.Sprintf("%.6f", sessionCost))
			_ = g.sessions.SetSessionContext(session.Key, "session_request_count", strconv.Itoa(prevCount+1))
		}
	}

	// Check for silent response tokens (NO_REPLY, HEARTBEAT_OK)
	if isSilentResponse(responseContent) {
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
	case text == "/reset" || text == "/new" || strings.HasPrefix(text, "/reset ") || strings.HasPrefix(text, "/new "):
		if sessionKey == "" {
			sendResponse("No active session to reset.")
			return
		}
		if err := g.sessions.ClearSessionMessages(sessionKey); err != nil {
			log.Printf("Error clearing session: %v", err)
			sendResponse("Failed to reset session.")
			return
		}
		// Clear persisted context usage so /context reflects the reset
		_ = g.sessions.SetSessionContext(sessionKey, "last_prompt_tokens", "")
		_ = g.sessions.SetSessionContext(sessionKey, "last_completion_tokens", "")
		_ = g.sessions.SetSessionContext(sessionKey, "last_total_tokens", "")
		_ = g.sessions.SetSessionContext(sessionKey, "session_total_cost", "")
		_ = g.sessions.SetSessionContext(sessionKey, "session_request_count", "")
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
			"/status - Show session info\n" +
			"/help - Show this message\n" +
			"/model [alias] - View/switch model\n" +
			"/context - Show context window usage\n" +
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
		sendResponse(formatContextUsage(session))

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
			aliasDisplay := formatAliasDisplay(aliases, "  ", " -> ")
			response := fmt.Sprintf("Current Model: %s\n\nAvailable aliases:\n%s\n\nUse /model <alias> to switch.", currentModel, aliasDisplay)
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
		if fullModel, exists := aliases[requested]; exists {
			if err := g.sessions.SetSessionContext(sessionKey, "model", fullModel); err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			if fullModel == "" {
				sendModelResponse("Switched to default model (sonnet)", "")
			} else {
				sendModelResponse(fmt.Sprintf("Switched to %s (%s)", requested, fullModel), fullModel)
			}
		} else if strings.Contains(requested, "/") || strings.HasPrefix(requested, "claude-") {
			if err := g.sessions.SetSessionContext(sessionKey, "model", requested); err != nil {
				sendResponse(fmt.Sprintf("Failed to switch model: %v", err))
				return
			}
			sendModelResponse(fmt.Sprintf("Switched to %s", requested), requested)
		} else {
			sendResponse(fmt.Sprintf("Unknown model alias: %s\n\nAvailable: %s", requested, formatAliasKeys(aliases)))
		}

	case text == "/stop":
		g.activeRequestsMu.RLock()
		cancel, exists := g.activeRequests[sessionKey]
		g.activeRequestsMu.RUnlock()

		if exists && cancel != nil {
			cancel()
			sendResponse("Stopping current operation...")
		} else {
			sendResponse("No active operation to stop.")
		}

	default:
		sendResponse(fmt.Sprintf("Unknown command: %s\nType /help for available commands.", command))
	}
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
