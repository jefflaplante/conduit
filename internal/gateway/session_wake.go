package gateway

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"conduit/internal/ai"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
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
	var wakeMessage, wakeMessageID string
	var wakeSource string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			wakeMessage, wakeMessageID = messages[i].Content, messages[i].ID
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

	// conduit-31jg.35: run through the shared TurnRunner. The wake message is
	// already in the transcript (mailbox delivery), so the runner does not
	// store it again but drops exactly that row from history by ID
	// (conduit-31jg.22). /stop registration happens only once the turn lock
	// is held, so a wake queued behind a live turn no longer overwrites the
	// live turn's cancel func (conduit-31jg.23).
	g.turns().Run(wakeCtx, TurnRequest{
		Session:                session,
		ChannelID:              session.ChannelID,
		UserID:                 session.UserID,
		Text:                   wakeMessage,
		PersistedUserMessageID: wakeMessageID,
		// conduit-31jg.43: a wake is model/inter-session driven, not a live
		// human message — approval-gated actions fail closed.
		NonInteractiveSource: "wake:" + wakeSource,
		Decorate: func(ctx context.Context) context.Context {
			return types.WithWakeSource(ctx, wakeSource)
		},
	}, &wakeTurnSink{g: g, session: session, wakeSource: wakeSource, wakeMessageChars: len(wakeMessage)})

	// Always reset wake depth when done (success or failure).
	_ = g.sessions.SetSessionContext(sessionKey, "wake_depth", "0")
}

// wakeTurnSink routes a woken session's reply to the session's channel.
type wakeTurnSink struct {
	g                *Gateway
	session          *sessions.Session
	wakeSource       string
	wakeMessageChars int
}

func (s *wakeTurnSink) Queued(context.Context)                         {}
func (s *wakeTurnSink) Begin(context.Context) ai.StreamCallback        { return nil }
func (s *wakeTurnSink) Progress(string)                                {}
func (s *wakeTurnSink) ToolEvent(context.Context, tools.ToolEventInfo) {}

func (s *wakeTurnSink) Finish(_ context.Context, res *TurnResult) {
	key := s.session.Key
	switch {
	case res.Dropped, res.Cancelled:
		s.g.logger.Debug("session wakeup cancelled", "session_key", key)
		return
	case res.Err != nil:
		s.g.logger.Error("session wakeup: AI generation failed", "session_key", key, "error", res.Err)
		return
	}

	// Route non-silent responses to the session's channel.
	if res.Delivered() && s.session.ChannelID != "" && s.session.UserID != "" {
		outgoingMsg := &protocol.OutgoingMessage{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeOutgoingMessage,
				ID:        fmt.Sprintf("wake_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			ChannelID: s.session.ChannelID,
			UserID:    s.session.UserID,
			Text:      res.Content,
		}
		if sendErr := s.g.channelManager.SendMessage(outgoingMsg); sendErr != nil {
			s.g.logger.Warn("session wakeup: failed to send response to channel",
				"session_key", key, "error", sendErr)
		}
	} else if s.wakeSource == types.WakeSourceSubAgentSilent && res.Silent {
		// conduit-3qb1 observability: the sub-agent ran, produced output that
		// was NOT posted to the channel (announce=false), and the parent LLM
		// then chose to stay silent. The human never sees anything. Log this
		// so we can observe the drop rate and tune the prompt guidance.
		s.g.logger.Warn("session wakeup: sub-agent silent callback fully suppressed",
			"session_key", key,
			"wake_source", s.wakeSource,
			"wake_message_chars", s.wakeMessageChars)
	}
}
