package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/pkg/protocol"
)

// SpawnSubAgent spawns a new sub-agent session (quiet mode, no announcements)
func (g *Gateway) SpawnSubAgent(ctx context.Context, task, agentId, model, label string, timeoutSeconds int) (string, error) {
	return g.SpawnSubAgentWithCallback(ctx, task, agentId, model, label, timeoutSeconds, "", "", false)
}

// SpawnSubAgentWithCallback spawns a sub-agent with optional result announcement
func (g *Gateway) SpawnSubAgentWithCallback(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, parentChannelID, parentUserID string, announce bool) (string, error) {
	// Create a unique session key for the sub-agent
	sessionKey := fmt.Sprintf("subagent_%d", time.Now().UnixNano())

	// Create the sub-agent session
	session, err := g.sessions.GetOrCreateSession("subagent", sessionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create sub-agent session: %w", err)
	}

	// Run the sub-agent in a goroutine
	go func() {
		subCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
		defer cancel()

		log.Printf("[SubAgent] Starting task: %s (session: %s, announce: %v)", task, session.Key, announce)

		// Resolve model alias (haiku -> claude-haiku-4-5-20251001, etc.)
		modelToUse := model
		if modelToUse == "" {
			// Use gateway's configured default model
			modelToUse = g.getDefaultModel()
		} else if fullModel, exists := g.getModelAliases()[strings.ToLower(modelToUse)]; exists && fullModel != "" {
			modelToUse = fullModel
		}

		response, err := g.ai.GenerateResponseWithTools(subCtx, session, task, "", modelToUse)
		if err != nil {
			log.Printf("[SubAgent] Error on %s: %v", session.Key, err)
			// Store error in session for manager to query
			_, _ = g.sessions.AddMessage(session.Key, "assistant", fmt.Sprintf("Error: %v", err), nil)
			// Announce failure if requested
			if announce && parentChannelID != "" && parentUserID != "" {
				g.announceToParent(parentChannelID, parentUserID, fmt.Sprintf("❌ Sub-agent failed: %v", err))
			}
			return
		}

		log.Printf("[SubAgent] Completed: %s", session.Key)

		// Store the result
		_, _ = g.sessions.AddMessage(session.Key, "assistant", response.GetContent(), nil)

		// Announce result if requested
		if announce && parentChannelID != "" && parentUserID != "" {
			result := response.GetContent()

			// Check for silent response patterns - don't announce these
			if result == "" || isSilentResponse(result) {
				log.Printf("[SubAgent] Silent response, not announcing")
				return
			}

			// Truncate if too long
			if len(result) > 3500 {
				result = result[:3500] + "\n\n_(truncated)_"
			}
			g.announceToParent(parentChannelID, parentUserID, result)
		}
	}()

	return session.Key, nil
}

// announceToParent sends a message back to the parent session
func (g *Gateway) announceToParent(channelID, userID, message string) {
	outgoingMsg := &protocol.OutgoingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeOutgoingMessage,
			ID:        fmt.Sprintf("subagent_announce_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		ChannelID: channelID,
		UserID:    userID,
		Text:      message,
	}

	if err := g.channelManager.SendMessage(outgoingMsg); err != nil {
		log.Printf("[SubAgent] Failed to announce result: %v", err)
	}
}

// getDefaultModel returns the gateway's configured default model
func (g *Gateway) getDefaultModel() string {
	if g.config == nil || len(g.config.AI.Providers) == 0 {
		return "claude-sonnet-4-20250514" // Fallback
	}

	// Find the default provider
	defaultName := g.config.AI.DefaultProvider
	for _, provider := range g.config.AI.Providers {
		if provider.Name == defaultName {
			return provider.Model
		}
	}

	// Fall back to first provider's model
	return g.config.AI.Providers[0].Model
}
