package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/tools"
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

	// Capture parent's effective brain user ID for WM sharing (the bucket the
	// brain adapter scopes the parent's turn to). conduit-31jg.30
	parentBrainUID := effectiveBrainUserID(ctx)

	// Resolve model (explicit model wins; empty uses configured sub-agent
	// default, falling back to the gateway default) and persist it with the
	// skill filter: the TurnRunner re-reads the session inside the turn lock
	// and takes the model override from its context (conduit-31jg.66).
	modelToUse := g.getSubagentModel(model)
	subContext := map[string]string{"model": modelToUse}
	if len(skills) > 0 {
		subContext["skill_filter"] = strings.Join(skills, ",")
	}
	if err := g.sessions.SetSessionContextBatch(session.Key, subContext); err != nil {
		return "", fmt.Errorf("failed to configure sub-agent session: %w", err)
	}
	if session.Context == nil {
		session.Context = make(map[string]string)
	}
	for k, v := range subContext {
		session.Context[k] = v
	}

	spawn := subAgentSpawn{
		task:             task,
		parentSessionKey: parentSessionKey,
		parentChannelID:  parentChannelID,
		parentUserID:     parentUserID,
		announce:         announce,
	}

	// Run the sub-agent in a goroutine
	go func() {
		// Use gateway lifecycle context, not request context.
		// Sub-agents are fire-and-forget - they should outlive the parent request.
		subCtx, cancel := deriveSubAgentContext(g.lifecycleCtx(), timeoutSeconds)
		defer cancel()

		// Own WM bucket + read-only fallback to the parent's WM. conduit-31jg.30
		subCtx = withSubAgentBrainScope(subCtx, parentBrainUID, session.Key)

		log.Printf("[SubAgent] Starting task: %s (session: %s, model: %s, announce: %v)", task, session.Key, modelToUse, announce)

		// conduit-31jg.66: run on the shared TurnRunner — task and result
		// persisted inside the turn lock, the turn registered in
		// ActiveRequests (/stop and the shutdown drain see it), usage/cost
		// (incl. SideCallLedger) and compaction like every other turn.
		// No channel/user: the sub-agent has no live human (conduit-31jg.43).
		sink := &subAgentTurnSink{g: g, sessionKey: session.Key}
		g.turns().Run(subCtx, TurnRequest{
			Session:              session,
			Text:                 task,
			NonInteractiveSource: "subagent",
		}, sink)
		g.finishSubAgent(session.Key, spawn, sink.outcome)
	}()

	return session.Key, nil
}

// subAgentSpawn is what the spawn call captured for routing the result.
type subAgentSpawn struct {
	task             string
	parentSessionKey string
	parentChannelID  string
	parentUserID     string
	announce         bool
}

// subAgentOutcome is how a sub-agent turn ended: failed (err set) or
// completed with result (possibly silent/empty).
type subAgentOutcome struct {
	err    error
	result string
}

// subAgentTurnSink keeps the sub-agent's custom failure / degenerate-reply
// routing on the TurnRunner (conduit-31jg.66). Finish runs inside the turn
// lock, so the "Error: …" row the sessions tools read is written in
// transcript order; the parent announcement and wake happen after the lock
// is released (finishSubAgent).
type subAgentTurnSink struct {
	g          *Gateway
	sessionKey string
	outcome    subAgentOutcome
}

func (s *subAgentTurnSink) Queued(context.Context)                         {}
func (s *subAgentTurnSink) Begin(context.Context) ai.StreamCallback        { return nil }
func (s *subAgentTurnSink) Progress(string)                                {}
func (s *subAgentTurnSink) ToolEvent(context.Context, tools.ToolEventInfo) {}

func (s *subAgentTurnSink) Finish(_ context.Context, res *TurnResult) {
	switch {
	case res.Dropped:
		s.outcome.err = fmt.Errorf("sub-agent did not start: %v", res.Err)
	case res.Cancelled && s.g.isDraining():
		// conduit-31jg.88: tell the parent (via finishSubAgent's failure
		// wake/transcript row) why; sub-agents get no restart notice.
		s.outcome.err = fmt.Errorf("sub-agent interrupted by gateway restart: %v", res.Err)
	case res.Cancelled:
		s.outcome.err = fmt.Errorf("sub-agent stopped: %v", res.Err)
	case res.Err != nil:
		s.outcome.err = res.Err
	case ai.IsEmptyResponseFallback(res.Raw):
		// bd-1k3o: a "successful" chain whose final answer is the
		// empty-guard fallback is a silent death, not a completion — the
		// model returned raw-empty twice and the guard substituted local
		// text (2026-09-04 RCA: sub-agent 6eb4bfe1 logged "Completed" while
		// delivering the fallback). Route it through the error path so the
		// parent gets WakeSourceSubAgentFailed instead of a fake result.
		s.outcome.err = fmt.Errorf("model returned empty responses (empty-guard fallback delivered); task not completed")
		log.Printf("[SubAgent] Degenerate final (empty-guard fallback) on %s — routing to failure: %v", s.sessionKey, s.outcome.err)
	default:
		// The runner already stored the reply (inside the lock).
		s.outcome.result = res.Raw
		return
	}
	// Store the error in the session for the manager to query.
	if _, err := s.g.sessions.AddMessage(s.sessionKey, "assistant", fmt.Sprintf("Error: %v", s.outcome.err), nil); err != nil {
		log.Printf("[SubAgent] Failed to store error for %s: %v", s.sessionKey, err)
	}
}

// finishSubAgent announces the sub-agent's outcome to the parent channel
// (when requested) and wakes the parent session.
func (g *Gateway) finishSubAgent(sessionKey string, sp subAgentSpawn, out subAgentOutcome) {
	if out.err != nil {
		log.Printf("[SubAgent] Error on %s: %v", sessionKey, out.err)
		errorMsg := fmt.Sprintf("Error: %v", out.err)
		// Announce failure if requested
		if sp.announce && sp.parentChannelID != "" && sp.parentUserID != "" {
			g.announceToParent(sp.parentChannelID, sp.parentUserID, fmt.Sprintf("❌ Sub-agent failed: %v", out.err))
		}
		// Wake the parent session so it knows the sub-agent failed (even in silent mode)
		if sp.parentSessionKey != "" {
			if wakeErr := g.sendToSessionWakeWithSource(context.Background(), sp.parentSessionKey, "", errorMsg, types.WakeSourceSubAgentFailed); wakeErr != nil {
				log.Printf("[SubAgent] Failed to wake parent session %s on error: %v", sp.parentSessionKey, wakeErr)
			}
		}
		return
	}

	log.Printf("[SubAgent] Completed: %s", sessionKey)
	result := out.result

	// Did we (the spawn path) post the raw result directly to the channel?
	// This controls the wake_source tag we pass to the parent: if the human
	// already saw the text, the parent LLM can safely stay silent; otherwise
	// it must decide whether to surface the result.
	var announced bool

	// Announce result to channel if requested
	if sp.announce && sp.parentUserID != "" {
		if result != "" && !channels.IsSilentResponse(result) {
			announceText := result
			if len(announceText) > 3500 {
				announceText = announceText[:3500] + "\n\n_(truncated)_"
			}
			// Resolve the parent's current channel from its session rather
			// than relying on the captured parentChannelID snapshot — if the
			// parent reconnected on a new channel since spawn time, this picks
			// the live one.
			channelID := g.resolveAnnounceChannelID(sp.parentSessionKey, sp.parentChannelID)
			if channelID != "" {
				g.announceToParent(channelID, sp.parentUserID, announceText)
				announced = true
			} else {
				log.Printf("[SubAgent] Cannot announce result for session %s: no live channel (captured=%q)",
					sp.parentSessionKey, sp.parentChannelID)
			}
		}
	}

	// Wake the parent session so it can process sub-agent output autonomously.
	// This works regardless of announce mode — the parent session always gets woken.
	if sp.parentSessionKey != "" && result != "" && !channels.IsSilentResponse(result) {
		wakeResult := result
		if len(wakeResult) > 3500 {
			wakeResult = wakeResult[:3500] + "\n\n_(truncated)_"
		}
		wakeSource := types.WakeSourceSubAgentSilent
		if announced {
			wakeSource = types.WakeSourceSubAgentAnnounced
		}
		if wakeErr := g.sendToSessionWakeWithSource(context.Background(), sp.parentSessionKey, "", wakeResult, wakeSource); wakeErr != nil {
			log.Printf("[SubAgent] Failed to wake parent session %s: %v", sp.parentSessionKey, wakeErr)
		}
	}
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
		// conduit-31jg.17: nothing configured → "" (provider default;
		// ai.ContextWindowForModel("") = DefaultContextWindow). No
		// hardcoded model literal.
		return ""
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
