package gateway

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/tools/debuglog"
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

	// Check for /ring command
	if text == "/ring" || strings.HasPrefix(text, "/ring ") {
		g.handleRingCommand(msg, text)
		return true
	}

	// Check for /smartroute command
	if text == "/smartroute" || strings.HasPrefix(text, "/smartroute ") {
		g.handleSmartRouteCommand(msg, text, session)
		return true
	}

	// Check for /compact command
	if text == "/compact" || strings.HasPrefix(text, "/compact ") {
		g.handleCompactCommand(ctx, msg, session)
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
	help := `*Available Commands*

/reset - Clear conversation history
/status - Show session info
/help - Show this message
/model - View/switch model (use /model reset to clear override)
/provider - View/switch provider
/context - Show context window usage
/compact - Compact context by summarizing older messages
/stop - Stop current operation
/ring - Show debug ring buffer activity
/smartroute [on|off|status|budget <amount>] - Smart routing controls

_Conduit Go Gateway_`

	g.sendCommandResponse(msg, help)
}

// handleRingCommand shows debug ring buffer contents
func (g *Gateway) handleRingCommand(msg *protocol.IncomingMessage, text string) {
	if g.ringBuffer == nil {
		g.sendCommandResponse(msg, "❌ Debug ring buffer not available")
		return
	}

	parts := strings.Fields(text)

	// Parse action and limit
	action := "dump"
	limit := 20
	if len(parts) > 1 {
		switch parts[1] {
		case "clear":
			action = "clear"
		case "status":
			action = "status"
		default:
			// Try to parse as a number (limit)
			if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
				limit = n
				if limit > 100 {
					limit = 100
				}
			}
		}
	}
	if len(parts) > 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
			limit = n
			if limit > 100 {
				limit = 100
			}
		}
	}

	switch action {
	case "clear":
		count := g.ringBuffer.Len()
		g.ringBuffer.Clear()
		g.sendCommandResponse(msg, fmt.Sprintf("🧹 Ring buffer cleared (%d entries removed)", count))

	case "status":
		g.sendCommandResponse(msg, fmt.Sprintf("📊 Ring buffer: %d entries (capacity: %d)",
			g.ringBuffer.Len(), debuglog.DefaultCapacity))

	default: // dump
		entries := g.ringBuffer.Last(limit)
		if len(entries) == 0 {
			g.sendCommandResponse(msg, "📭 Ring buffer is empty")
			return
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("🔍 *Debug Ring Buffer* (%d entries)\n\n", len(entries)))

		for _, e := range entries {
			ts := e.Timestamp.Format("15:04:05")
			switch e.Type {
			case debuglog.EntryToolStart:
				sb.WriteString(fmt.Sprintf("`%s` ▶ *%s*\n", ts, e.ToolName))
			case debuglog.EntryToolComplete:
				sb.WriteString(fmt.Sprintf("`%s` ✓ *%s* (%s)\n", ts, e.ToolName, e.Duration))
			case debuglog.EntryToolError:
				sb.WriteString(fmt.Sprintf("`%s` ✗ *%s* ERROR\n", ts, e.ToolName))
			case debuglog.EntryThinking:
				sb.WriteString(fmt.Sprintf("`%s` 💭 thinking...\n", ts))
			case debuglog.EntryLLMRequest:
				sb.WriteString(fmt.Sprintf("`%s` → LLM request\n", ts))
			case debuglog.EntryLLMResponse:
				sb.WriteString(fmt.Sprintf("`%s` ← LLM response (%s)\n", ts, e.Duration))
			}
		}

		sb.WriteString("\n_Use /ring clear to reset_")
		g.sendCommandResponse(msg, sb.String())
	}
}

// handleCompactCommand manually triggers context compaction for the session
func (g *Gateway) handleCompactCommand(ctx context.Context, msg *protocol.IncomingMessage, session *sessions.Session) {
	if g.compactionEngine == nil {
		g.sendCommandResponse(msg, "Context compaction is not configured. Enable it in the AI config.")
		return
	}

	result, err := g.compactionEngine.Compact(ctx, session)
	if err != nil {
		g.sendCommandResponse(msg, fmt.Sprintf("Compaction failed: %v", err))
		return
	}

	if result == nil {
		g.sendCommandResponse(msg, "No compaction needed (not enough messages to compact).")
		return
	}

	g.sendCommandResponse(msg, fmt.Sprintf("Compacted %d messages into summary + %d recent messages.", result.SummarizedCount, result.KeptCount))
}

// handleSmartRouteCommand handles smart routing configuration
func (g *Gateway) handleSmartRouteCommand(msg *protocol.IncomingMessage, text string, session *sessions.Session) {
	parts := strings.Fields(text)
	subcommand := ""
	if len(parts) > 1 {
		subcommand = strings.ToLower(parts[1])
	}

	switch subcommand {
	case "", "status":
		// Show current state
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
		sb.WriteString(fmt.Sprintf("🧠 *Smart Routing*: %s\n", enabled))
		if model != "" {
			sb.WriteString(fmt.Sprintf("*Last model:* %s\n", model))
		}
		if complexity != "" {
			sb.WriteString(fmt.Sprintf("*Complexity score:* %s\n", complexity))
		}
		if reason != "" {
			sb.WriteString(fmt.Sprintf("*Selection reason:* %s\n", reason))
		}
		if cost != "" {
			sb.WriteString(fmt.Sprintf("*Session cost:* $%s\n", cost))
		}
		if g.config.AI.SmartRouting != nil && g.config.AI.SmartRouting.CostBudgetDaily > 0 {
			sb.WriteString(fmt.Sprintf("*Daily budget:* $%.2f\n", g.config.AI.SmartRouting.CostBudgetDaily))
		}
		g.sendCommandResponse(msg, sb.String())

	case "on":
		_ = g.sessions.SetSessionContext(session.Key, "smart_routing_enabled", "true")
		g.sendCommandResponse(msg, "✅ Smart routing enabled for this session.")

	case "off":
		_ = g.sessions.SetSessionContext(session.Key, "smart_routing_enabled", "false")
		g.sendCommandResponse(msg, "⏸️ Smart routing disabled for this session. Using default model.")

	case "budget":
		if len(parts) < 3 {
			g.sendCommandResponse(msg, "Usage: /smartroute budget <amount>")
			return
		}
		amount := parts[2]
		// Validate it's a number
		if _, err := strconv.ParseFloat(amount, 64); err != nil {
			g.sendCommandResponse(msg, fmt.Sprintf("❌ Invalid budget amount: %s", amount))
			return
		}
		_ = g.sessions.SetSessionContext(session.Key, "smart_routing_budget", amount)
		g.sendCommandResponse(msg, fmt.Sprintf("💰 Session budget set to $%s.", amount))

	default:
		g.sendCommandResponse(msg, "Usage: /smartroute [on|off|status|budget <amount>]")
	}
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

	// Also switch the model to the new provider's default so we don't send
	// an incompatible model name (e.g. a Claude model to Ollama).
	if meta.DefaultModel != "" {
		_ = g.sessions.SetSessionContext(session.Key, "model", meta.DefaultModel)
		session.Context["model"] = meta.DefaultModel
	}

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

	// Handle reset/default to clear the override
	if requested == "reset" || requested == "default" {
		// Clear model override by setting to empty string
		if err := g.sessions.SetSessionContext(session.Key, "model", ""); err != nil {
			g.sendCommandResponse(msg, fmt.Sprintf("Failed to reset model: %v", err))
			return
		}
		if session.Context == nil {
			session.Context = make(map[string]string)
		}
		session.Context["model"] = ""

		// Also clear the provider override
		_ = g.sessions.SetSessionContext(session.Key, "provider", "")
		session.Context["provider"] = ""

		g.sendCommandResponse(msg, "Model reset to default (sonnet)")
		return
	}

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
