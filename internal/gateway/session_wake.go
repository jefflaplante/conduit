package gateway

import (
	"context"
	"fmt"
	"time"

	"conduit/internal/ai"
	"conduit/internal/approval"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
	"conduit/internal/tools/types"
)

// bd-unz3: wall-clock budget for a single wake turn. Wake turns host the
// whole LLM chain (initial call + tool rounds + EmptyGuard retries) and can
// spawn sub-agent chains at core.DefaultSubAgentTimeoutSeconds, so this must
// always exceed the sub-agent chain budget (pinned by test).
const wakeTurnTimeout = 30 * time.Minute

// wakeTurnBudget derives the wake turn's context from parent with the wake
// chain deadline. Split out for testability.
func wakeTurnBudget(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, wakeTurnTimeout)
}

// wakeSession re-activates a session to process its most recent inter-session message.
// It is called from the session wakeup listener goroutine started in Start().
//
// Recursion guard (H4): every wake message carries the depth of the wake
// that delivered it (metaWakeDepth, the waking turn's depth + 1). A message
// deeper than maxWakeDepth is not processed now; it stays in the session for
// the next normal activation. The woken turn runs at that depth, so a wake it
// sends goes one level deeper. A human message starts again at depth 0.
func (g *Gateway) wakeSession(sessionKey string) {
	if g.ai == nil {
		return
	}

	session, err := g.sessions.GetSession(sessionKey)
	if err != nil {
		g.logger.Warn("session wakeup: session not found", "session_key", sessionKey)
		return
	}

	// Find the most recent user message (the inter-session message just delivered).
	messages, err := g.sessions.GetMessages(sessionKey, 20)
	if err != nil || len(messages) == 0 {
		g.logger.Warn("session wakeup: no messages found", "session_key", sessionKey)
		return
	}
	var wakeMessage, wakeMessageID string
	var wakeSource string
	depth := 0
	resumeInteractive := false
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			wakeMessage, wakeMessageID = messages[i].Content, messages[i].ID
			depth = messageWakeDepth(messages[i].Metadata)
			if src, ok := messages[i].Metadata["wake_source"]; ok && src != "" {
				wakeSource = src
			} else if src, ok := messages[i].Metadata["source"]; ok && src == "inter_session" {
				wakeSource = types.WakeSourceInterSession
			}
			// conduit-31jg.88: auto-resume of a turn cut off by a restart
			// keeps the interactive origin of the turn it continues.
			resumeInteractive = wakeSource == types.WakeSourceRestartResume &&
				messages[i].Metadata[metaResumeInteractive] == "true"
			break
		}
	}
	if wakeMessage == "" {
		g.logger.Warn("session wakeup: no user message to process", "session_key", sessionKey)
		return
	}
	if depth > maxWakeDepth {
		g.logger.Warn("session wakeup: max depth reached, message queued for next activation",
			"session_key", sessionKey, "wake_depth", depth)
		return
	}

	g.logger.Info("waking session for inter-session message",
		"session_key", sessionKey, "wake_depth", depth, "wake_source", wakeSource)

	// Derive a context from the gateway lifecycle context (not a request context).
	//
	// bd-unz3: this budget wraps the ENTIRE wake turn — initial LLM call,
	// every tool round, and every EmptyGuard retry — and the turn may
	// legitimately spawn a sub-agent chain running at
	// core.DefaultSubAgentTimeoutSeconds (600s). The previous 5-minute wall
	// was shorter than the chain it hosted, so deep heartbeat/wake turns
	// died at exactly 5m0.000s (bd-3ose census; three silent deaths the
	// night of Oct 14-15 alone). wake_depth (max 3) remains the runaway
	// guard; this deadline only needs to exceed any chain the turn hosts.
	wakeCtx, cancel := wakeTurnBudget(g.lifecycleCtx())
	defer cancel()

	// conduit-31jg.35: run through the shared TurnRunner. The wake message is
	// already in the transcript (mailbox delivery), so the runner does not
	// store it again but drops exactly that row from history by ID
	// (conduit-31jg.22). /stop registration happens only once the turn lock
	// is held, so a wake queued behind a live turn no longer overwrites the
	// live turn's cancel func (conduit-31jg.23).
	req := TurnRequest{
		Session:                session,
		ChannelID:              session.ChannelID,
		UserID:                 session.UserID,
		Text:                   wakeMessage,
		PersistedUserMessageID: wakeMessageID,
		// conduit-31jg.43: a wake is model/inter-session driven, not a live
		// human message — approval-gated actions fail closed.
		NonInteractiveSource: "wake:" + wakeSource,
		Decorate: func(ctx context.Context) context.Context {
			return withWakeDepth(types.WithWakeSource(ctx, wakeSource), depth) // H4
		},
	}
	if resumeInteractive {
		// conduit-31jg.88: the owner started the interrupted turn; its
		// resumption may prompt them on the same channel.
		req.NonInteractiveSource = ""
		req.Origin = &approval.Origin{
			Source: types.WakeSourceRestartResume, ChannelID: session.ChannelID, UserID: session.UserID,
			SessionKey: sessionKey, Notify: g.channelApprovalNotifier(session.ChannelID, session.UserID, sessionKey),
		}
	}
	g.turns().Run(wakeCtx, req, &wakeTurnSink{g: g, session: session, wakeSource: wakeSource, wakeMessageChars: len(wakeMessage)})
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
			Text:      s.g.topicTag(key, s.session.ChannelID, s.session.UserID, res.Content), // conduit-43vk
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
