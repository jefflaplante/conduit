package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/brain"
	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/tools/types"
)

// SpawnSubAgent spawns a new sub-agent session (quiet mode, no announcements)
func (g *Gateway) SpawnSubAgent(ctx context.Context, task, agentId, model, label string, timeoutSeconds int) (string, error) {
	return g.SpawnSubAgentWithCallback(ctx, task, agentId, model, label, timeoutSeconds, "", "", false, nil)
}

// SpawnSubAgentWithSkills spawns a sub-agent with a filtered skill set
func (g *Gateway) SpawnSubAgentWithSkills(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, skills []string) (string, error) {
	return g.SpawnSubAgentWithCallback(ctx, task, agentId, model, label, timeoutSeconds, "", "", false, skills)
}

// deriveSubAgentContext creates a sub-agent context from the gateway lifecycle context
// with the specified timeout. Sub-agents outlive parent requests but respect gateway shutdown.
func deriveSubAgentContext(gatewayCtx context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(gatewayCtx, time.Duration(timeoutSeconds)*time.Second)
}

// SpawnSubAgentWithCallback spawns a sub-agent with optional result announcement and skill filtering
func (g *Gateway) SpawnSubAgentWithCallback(ctx context.Context, task, agentId, model, label string, timeoutSeconds int, parentChannelID, parentUserID string, announce bool, skills []string) (string, error) {
	// Check if caller's context is already done (don't spawn if request was canceled)
	if ctx.Err() != nil {
		return "", fmt.Errorf("cannot spawn sub-agent: parent context already canceled")
	}

	// Capture the parent session key now (before the goroutine), so we can wake it when done.
	parentSessionKey := types.RequestSessionKey(ctx)

	// Create a unique session key for the sub-agent
	sessionKey := fmt.Sprintf("subagent_%d", time.Now().UnixNano())

	// Create the sub-agent session
	session, err := g.sessions.GetOrCreateSession("subagent", sessionKey)
	if err != nil {
		return "", fmt.Errorf("failed to create sub-agent session: %w", err)
	}

	// Capture parent's effective brain user ID for WM sharing
	parentBrainUID := types.RequestUserID(ctx)

	// Run the sub-agent in a goroutine
	go func() {
		// Use gateway lifecycle context, not request context.
		// Sub-agents are fire-and-forget - they should outlive the parent request.
		subCtx, cancel := deriveSubAgentContext(g.ctx, timeoutSeconds)
		defer cancel()

		// Share parent's brain working memory (read-only fallback)
		if parentBrainUID != "" {
			subCtx = brain.WithParentUserID(subCtx, parentBrainUID)
		}

		// Resolve model (explicit model wins; empty uses configured sub-agent
		// default, falling back to the gateway default)
		modelToUse := g.getSubagentModel(model)

		// Set model context for prompt builder's context window calculations
		if session.Context == nil {
			session.Context = make(map[string]string)
		}
		session.Context["model"] = modelToUse

		// Set skill filter if skills are provided
		if len(skills) > 0 {
			session.Context["skill_filter"] = strings.Join(skills, ",")
		}

		log.Printf("[SubAgent] Starting task: %s (session: %s, model: %s, announce: %v)", task, session.Key, modelToUse, announce)

		response, err := g.ai.GenerateResponseWithTools(subCtx, session, task, "", modelToUse)
		if err != nil {
			log.Printf("[SubAgent] Error on %s: %v", session.Key, err)
			// Store error in session for manager to query
			errorMsg := fmt.Sprintf("Error: %v", err)
			_, _ = g.sessions.AddMessage(session.Key, "assistant", errorMsg, nil)
			// Announce failure if requested
			if announce && parentChannelID != "" && parentUserID != "" {
				g.announceToParent(parentChannelID, parentUserID, fmt.Sprintf("❌ Sub-agent failed: %v", err))
			}
			// Wake the parent session so it knows the sub-agent failed (even in silent mode)
			if parentSessionKey != "" {
				wakeErr := g.sendToSessionWakeWithSource(context.Background(), parentSessionKey, "", errorMsg, types.WakeSourceSubAgentFailed)
				if wakeErr != nil {
					log.Printf("[SubAgent] Failed to wake parent session %s on error: %v", parentSessionKey, wakeErr)
				}
			}
			return
		}

		log.Printf("[SubAgent] Completed: %s", session.Key)

		// Store the result
		_, _ = g.sessions.AddMessage(session.Key, "assistant", response.GetContent(), nil)

		result := response.GetContent()

		// Did we (the spawn path) post the raw result directly to the channel?
		// This controls the wake_source tag we pass to the parent: if the human
		// already saw the text, the parent LLM can safely stay silent; otherwise
		// it must decide whether to surface the result.
		var announced bool

		// Announce result to channel if requested
		if announce && parentUserID != "" {
			if result != "" && !channels.IsSilentResponse(result) {
				announceText := result
				if len(announceText) > 3500 {
					announceText = announceText[:3500] + "\n\n_(truncated)_"
				}
				// Resolve the parent's current channel from its session rather
				// than relying on the captured parentChannelID snapshot — if the
				// parent reconnected on a new channel since spawn time, this picks
				// the live one.
				channelID := g.resolveAnnounceChannelID(parentSessionKey, parentChannelID)
				if channelID != "" {
					g.announceToParent(channelID, parentUserID, announceText)
					announced = true
				} else {
					log.Printf("[SubAgent] Cannot announce result for session %s: no live channel (captured=%q)",
						parentSessionKey, parentChannelID)
				}
			}
		}

		// Wake the parent session so it can process sub-agent output autonomously.
		// This works regardless of announce mode — the parent session always gets woken.
		if parentSessionKey != "" && result != "" && !channels.IsSilentResponse(result) {
			wakeResult := result
			if len(wakeResult) > 3500 {
				wakeResult = wakeResult[:3500] + "\n\n_(truncated)_"
			}
			wakeSource := types.WakeSourceSubAgentSilent
			if announced {
				wakeSource = types.WakeSourceSubAgentAnnounced
			}
			if wakeErr := g.sendToSessionWakeWithSource(context.Background(), parentSessionKey, "", wakeResult, wakeSource); wakeErr != nil {
				log.Printf("[SubAgent] Failed to wake parent session %s: %v", parentSessionKey, wakeErr)
			}
		}
	}()

	return session.Key, nil
}

// resolveAnnounceChannelID returns the parent session's current ChannelID if it
// can be read from the session store, falling back to the channel captured at
// spawn time. Empty string means we have no plausible destination and should
// skip the announce.
func (g *Gateway) resolveAnnounceChannelID(parentSessionKey, capturedChannelID string) string {
	if parentSessionKey != "" {
		if s, err := g.sessions.GetSession(parentSessionKey); err == nil && s != nil && s.ChannelID != "" {
			if capturedChannelID != "" && s.ChannelID != capturedChannelID {
				log.Printf("[SubAgent] Parent session %s channel shifted %q → %q since spawn; using live channel",
					parentSessionKey, capturedChannelID, s.ChannelID)
			}
			return s.ChannelID
		}
	}
	return capturedChannelID
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

// getSubagentModel resolves the model for a spawned sub-agent.
// Explicit model wins (alias-resolved). Otherwise, if AIConfig.SubagentDefaultModel
// is set it is used (alias-resolved), letting operators pin a cheaper default for
// sub-agents independently of the main-session gateway default. Empty/nil config
// falls back to the gateway default model.
func (g *Gateway) getSubagentModel(model string) string {
	if model != "" {
		if fullModel, exists := g.getModelAliases()[strings.ToLower(model)]; exists && fullModel != "" {
			return fullModel
		}
		return model
	}

	if g.config != nil && g.config.AI.SubagentDefaultModel != "" {
		if fullModel, exists := g.getModelAliases()[strings.ToLower(g.config.AI.SubagentDefaultModel)]; exists && fullModel != "" {
			return fullModel
		}
		return g.config.AI.SubagentDefaultModel
	}

	return g.getDefaultModel()
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
