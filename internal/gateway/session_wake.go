package gateway

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/tools/types"
)

// wakeSession re-activates a session to process its most recent inter-session message.
// It is called from the session wakeup listener goroutine started in Start().
//
// Recursion guard: the session's wake_depth context key is checked before processing.
// If depth >= 3 (sessions messaging each other back and forth), the wake is skipped and
// the message remains in the session for processing on the next normal activation.
func (g *Gateway) wakeSession(sessionKey string) {
	if g.ai == nil {
		return
	}

	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		g.logger.Warn("session wakeup: session not found", "session_key", sessionKey)
		return
	}

	// Recursion guard: check current wake depth.
	depth := 0
	if d := session.Context["wake_depth"]; d != "" {
		if parsed, parseErr := strconv.Atoi(d); parseErr == nil {
			depth = parsed
		}
	}
	const maxWakeDepth = 3
	if depth >= maxWakeDepth {
		g.logger.Warn("session wakeup: max depth reached, message queued for next activation",
			"session_key", sessionKey, "wake_depth", depth)
		return
	}

	// Increment wake depth before processing (guards against recursive wakeups).
	if setErr := g.sessions.SetSessionContext(sessionKey, "wake_depth", strconv.Itoa(depth+1)); setErr != nil {
		g.logger.Warn("session wakeup: failed to set wake_depth", "session_key", sessionKey, "error", setErr)
	}

	// Find the most recent user message (the inter-session message just delivered).
	messages, err := g.sessions.GetMessages(sessionKey, 20)
	if err != nil || len(messages) == 0 {
		g.logger.Warn("session wakeup: no messages found", "session_key", sessionKey)
		_ = g.sessions.SetSessionContext(sessionKey, "wake_depth", "0")
		return
	}
	var wakeMessage string
	var wakeSource string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			wakeMessage = messages[i].Content
			if src, ok := messages[i].Metadata["wake_source"]; ok && src != "" {
				wakeSource = src
			} else if src, ok := messages[i].Metadata["source"]; ok && src == "inter_session" {
				wakeSource = types.WakeSourceInterSession
			}
			break
		}
	}
	if wakeMessage == "" {
		g.logger.Warn("session wakeup: no user message to process", "session_key", sessionKey)
		_ = g.sessions.SetSessionContext(sessionKey, "wake_depth", "0")
		return
	}

	g.logger.Info("waking session for inter-session message",
		"session_key", sessionKey, "wake_depth", depth+1, "wake_source", wakeSource)

	// Derive a context from the gateway lifecycle context (not a request context).
	wakeCtx, cancel := context.WithTimeout(g.ctx, 5*time.Minute)
	defer cancel()

	modelOverride := session.Context["model"]
	providerOverride := session.Context["provider"]
	wakeCtx = types.WithRequestContext(wakeCtx, session.ChannelID, session.UserID, sessionKey)
	wakeCtx = types.WithWakeSource(wakeCtx, wakeSource)

	// Track this request so /stop can cancel it.
	g.ws.ActiveRequestsMu.Lock()
	g.ws.ActiveRequests[sessionKey] = cancel
	g.ws.ActiveRequestsMu.Unlock()
	defer func() {
		g.ws.ActiveRequestsMu.Lock()
		delete(g.ws.ActiveRequests, sessionKey)
		g.ws.ActiveRequestsMu.Unlock()
	}()

	response, err := g.ai.GenerateResponseWithTools(wakeCtx, session, wakeMessage, providerOverride, modelOverride)

	// Always reset wake depth when done (success or failure).
	_ = g.sessions.SetSessionContext(sessionKey, "wake_depth", "0")

	if err != nil {
		if wakeCtx.Err() == context.Canceled {
			g.logger.Debug("session wakeup cancelled", "session_key", sessionKey)
			return
		}
		g.logger.Error("session wakeup: AI generation failed", "session_key", sessionKey, "error", err)
		return
	}

	responseContent := response.GetContent()

	// Persist AI response to session history.
	if _, addErr := g.sessions.AddMessage(sessionKey, "assistant", responseContent, nil); addErr != nil {
		g.logger.Warn("session wakeup: failed to save AI response", "session_key", sessionKey, "error", addErr)
	}

	// Route non-silent responses to the session's channel.
	if responseContent != "" && !channels.IsSilentResponse(responseContent) &&
		session.ChannelID != "" && session.UserID != "" {
		outgoingMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("wake_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID: session.ChannelID,
			UserID:    session.UserID,
			Text:      responseContent,
		}
		if sendErr := g.channelManager.SendMessage(outgoingMsg); sendErr != nil {
			g.logger.Warn("session wakeup: failed to send response to channel",
				"session_key", sessionKey, "error", sendErr)
		}
	} else if wakeSource == types.WakeSourceSubAgentSilent &&
		channels.IsSilentResponse(responseContent) {
		// conduit-3qb1 observability: the sub-agent ran, produced output that
		// was NOT posted to the channel (announce=false), and the parent LLM
		// then chose to stay silent. The human never sees anything. Log this
		// so we can observe the drop rate and tune the prompt guidance.
		g.logger.Warn("session wakeup: sub-agent silent callback fully suppressed",
			"session_key", sessionKey,
			"wake_source", wakeSource,
			"wake_message_chars", len(wakeMessage))
	}
}
