package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/protocol"
)

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
