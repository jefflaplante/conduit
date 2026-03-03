package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/version"
	"conduit/pkg/protocol"
)

// handleCommand handles slash commands and returns true if handled
func (g *Gateway) handleCommand(ctx context.Context, msg *protocol.IncomingMessage, session *sessions.Session) bool {
	text := strings.TrimSpace(msg.Text)

	// Check for /reset command
	if text == "/reset" || text == "/new" || strings.HasPrefix(text, "/reset ") || strings.HasPrefix(text, "/new ") {
		log.Printf("Processing /reset command for session: %s", session.Key)

		// Clear session messages
		if err := g.sessions.ClearSessionMessages(session.Key); err != nil {
			log.Printf("Error clearing session messages: %v", err)
			g.sendCommandResponse(msg, "❌ Failed to reset session. Please try again.")
			return true
		}

		// Clear persisted context usage so /context reflects the reset
		_ = g.sessions.SetSessionContext(session.Key, "last_prompt_tokens", "")
		_ = g.sessions.SetSessionContext(session.Key, "last_completion_tokens", "")
		_ = g.sessions.SetSessionContext(session.Key, "last_total_tokens", "")

		g.sendCommandResponse(msg, "✨ Session reset. Fresh start!")
		log.Printf("Session reset successfully: %s", session.Key)
		return true
	}

	// Check for /status command
	if text == "/status" || strings.HasPrefix(text, "/status ") {
		g.handleStatusCommand(msg, session)
		return true
	}

	// Check for /help or /commands
	if text == "/help" || text == "/commands" || strings.HasPrefix(text, "/help ") {
		g.handleHelpCommand(msg)
		return true
	}

	// Check for /model command
	if text == "/model" || strings.HasPrefix(text, "/model ") {
		g.handleModelCommand(msg, text, session)
		return true
	}

	// Check for /provider command
	if text == "/provider" || strings.HasPrefix(text, "/provider ") {
		g.handleProviderCommand(msg, text, session)
		return true
	}

	// Check for /context command
	if text == "/context" || strings.HasPrefix(text, "/context ") {
		g.sendCommandResponse(msg, formatContextUsage(session))
		return true
	}

	// Check for /stop command
	if text == "/stop" {
		g.activeRequestsMu.RLock()
		cancel, exists := g.activeRequests[session.Key]
		g.activeRequestsMu.RUnlock()

		if exists && cancel != nil {
			cancel()
			g.sendCommandResponse(msg, "🛑 Stopping current operation...")
			log.Printf("Cancelled active request for session: %s", session.Key)
		} else {
			g.sendCommandResponse(msg, "ℹ️ No active operation to stop.")
		}
		return true
	}

	return false
}

// handleStatusCommand shows session status
func (g *Gateway) handleStatusCommand(msg *protocol.IncomingMessage, session *sessions.Session) {
	// Get message count for this session
	messages, _ := g.sessions.GetMessages(session.Key, 1000)
	msgCount := len(messages)

	currentModel := session.Context["model"]
	if currentModel == "" {
		currentModel = "sonnet (default)"
	}

	currentProvider := session.Context["provider"]
	if currentProvider == "" {
		currentProvider = "default"
	}

	// Build status message
	status := fmt.Sprintf("*Session Status*\n\n"+
		"*Session:* %s\n"+
		"*Messages:* %d\n"+
		"*Channel:* %s\n"+
		"*Model:* %s\n"+
		"*Provider:* %s\n\n"+
		"_Go Gateway %s_",
		session.Key,
		msgCount,
		msg.ChannelID,
		currentModel,
		currentProvider,
		version.Info(),
	)

	g.sendCommandResponse(msg, status)
}

// handleHelpCommand shows available commands
func (g *Gateway) handleHelpCommand(msg *protocol.IncomingMessage) {
	help := `📖 *Available Commands*

/reset - Clear conversation history
/status - Show session info
/help - Show this message
/model - View/switch model
/provider - View/switch provider
/context - Show context window usage
/stop - Stop current operation

_Conduit Go Gateway_`

	g.sendCommandResponse(msg, help)
}

// getModelAliases returns the configured model aliases, falling back to defaults
// if the config has none.
func (g *Gateway) getModelAliases() map[string]string {
	if len(g.config.AI.ModelAliases) > 0 {
		return g.config.AI.ModelAliases
	}
	return config.DefaultModelAliases()
}

// formatAliasKeys returns a comma-separated list of alias names from the map.
func formatAliasKeys(aliases map[string]string) string {
	keys := make([]string, 0, len(aliases))
	for k := range aliases {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// formatAliasDisplay returns a multi-line display string of aliases and their targets.
func formatAliasDisplay(aliases map[string]string, prefix, arrow string) string {
	var lines []string
	for alias, model := range aliases {
		display := model
		if display == "" {
			display = "reset to default"
		}
		lines = append(lines, fmt.Sprintf("%s%s %s %s", prefix, alias, arrow, display))
	}
	return strings.Join(lines, "\n")
}

// formatAliasDisplayWithProvider returns a multi-line display string of aliases,
// their targets, and the resolved provider for each.
func (g *Gateway) formatAliasDisplayWithProvider(aliases map[string]string, prefix, arrow string) string {
	var lines []string
	for alias, model := range aliases {
		display := model
		if display == "" {
			display = "reset to default"
		}
		providerName := g.ai.ResolveProviderForModel(model)
		if providerName == "" {
			providerName = g.ai.DefaultProviderName()
		}
		lines = append(lines, fmt.Sprintf("%s%s %s %s (%s)", prefix, alias, arrow, display, providerName))
	}
	return strings.Join(lines, "\n")
}

// handleProviderCommand handles provider switching
func (g *Gateway) handleProviderCommand(msg *protocol.IncomingMessage, text string, session *sessions.Session) {
	parts := strings.Fields(text)

	currentProvider := session.Context["provider"]
	if currentProvider == "" {
		currentProvider = g.ai.DefaultProviderName() + " (default)"
	}

	if len(parts) == 1 {
		// List providers
		providers := g.ai.ListProviders()
		var lines []string
		for _, p := range providers {
			lines = append(lines, fmt.Sprintf("• *%s* — %s (model: %s)", p.Name, p.Type, p.DefaultModel))
		}
		response := fmt.Sprintf("🔌 *Current Provider*\n\n*Active:* %s\n\n*Available providers:*\n%s\n\nUse /provider <name> to switch.", currentProvider, strings.Join(lines, "\n"))
		g.sendCommandResponse(msg, response)
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
		g.sendCommandResponse(msg, fmt.Sprintf("❌ Unknown provider: %s\n\nAvailable: %s", requested, strings.Join(names, ", ")))
		return
	}

	if err := g.sessions.SetSessionContext(session.Key, "provider", requested); err != nil {
		g.sendCommandResponse(msg, fmt.Sprintf("❌ Failed to switch provider: %v", err))
		return
	}
	if session.Context == nil {
		session.Context = make(map[string]string)
	}
	session.Context["provider"] = requested

	g.sendCommandResponse(msg, fmt.Sprintf("✅ Switched to provider *%s* (%s, model: %s)", meta.Name, meta.Type, meta.DefaultModel))
}

// handleModelCommand handles model switching
func (g *Gateway) handleModelCommand(msg *protocol.IncomingMessage, text string, session *sessions.Session) {
	parts := strings.Fields(text)

	// Get current model from session
	currentModel := session.Context["model"]
	if currentModel == "" {
		currentModel = "sonnet (default)"
	}

	aliases := g.getModelAliases()

	if len(parts) == 1 {
		// Just /model - show current and list available with provider info
		currentProvider := session.Context["provider"]
		if currentProvider == "" {
			currentProvider = g.ai.DefaultProviderName()
		}
		aliasDisplay := g.formatAliasDisplayWithProvider(aliases, "• ", " → ")
		response := fmt.Sprintf("🤖 *Current Model*\n\n*Active:* %s\n*Provider:* %s\n\n*Available aliases:*\n%s\n\nUse /model <alias> to switch.", currentModel, currentProvider, aliasDisplay)
		g.sendCommandResponse(msg, response)
		return
	}

	// Model switch requested
	requested := strings.ToLower(parts[1])

	// setModelAndResolveProvider stores the model in session context,
	// auto-resolves the provider, and returns the resolved provider name.
	setModelAndResolveProvider := func(model string) (string, error) {
		if err := g.sessions.SetSessionContext(session.Key, "model", model); err != nil {
			return "", err
		}
		if session.Context == nil {
			session.Context = make(map[string]string)
		}
		session.Context["model"] = model

		resolvedProvider := g.ai.ResolveProviderForModel(model)
		if resolvedProvider != "" {
			_ = g.sessions.SetSessionContext(session.Key, "provider", resolvedProvider)
			session.Context["provider"] = resolvedProvider
		}
		return resolvedProvider, nil
	}

	// Check if it's a known alias
	if fullModel, exists := aliases[requested]; exists {
		resolvedProvider, err := setModelAndResolveProvider(fullModel)
		if err != nil {
			g.sendCommandResponse(msg, fmt.Sprintf("❌ Failed to switch model: %v", err))
			return
		}

		if fullModel == "" {
			g.sendCommandResponse(msg, "✅ Switched to *default* model (sonnet)")
		} else {
			providerSuffix := ""
			if resolvedProvider != "" {
				providerSuffix = " on " + resolvedProvider
			}
			g.sendCommandResponse(msg, fmt.Sprintf("✅ Switched to *%s* (%s)%s", requested, fullModel, providerSuffix))
		}
		return
	}

	// Accept any raw model name (full model name, contains /, or any string)
	if strings.Contains(requested, "/") || len(requested) > 3 {
		resolvedProvider, err := setModelAndResolveProvider(requested)
		if err != nil {
			g.sendCommandResponse(msg, fmt.Sprintf("❌ Failed to switch model: %v", err))
			return
		}

		providerSuffix := ""
		if resolvedProvider != "" {
			providerSuffix = " on " + resolvedProvider
		}
		g.sendCommandResponse(msg, fmt.Sprintf("✅ Switched to *%s*%s", requested, providerSuffix))
		return
	}

	// Unknown alias
	g.sendCommandResponse(msg, fmt.Sprintf("❌ Unknown model alias: %s\n\nAvailable: %s", requested, formatAliasKeys(aliases)))
}

// sendCommandResponse sends a simple response for command handling
func (g *Gateway) sendCommandResponse(msg *protocol.IncomingMessage, text string) {
	outgoingMsg := &protocol.OutgoingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeOutgoingMessage,
			ID:        fmt.Sprintf("cmd_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		ChannelID:  msg.ChannelID,
		SessionKey: msg.SessionKey,
		UserID:     msg.UserID,
		Text:       text,
	}

	if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
		log.Printf("Error sending command response: %v", err)
	}
}
