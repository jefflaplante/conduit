package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/logging"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools"
)

// busyAckText is the reply sent when a message queues behind an in-flight turn.
const busyAckText = "Still working on your previous request — this message is queued and I'll handle it right after."

// channelTurnSink renders a TurnRunner turn onto a channel adapter
// (Telegram): busy-ack, typing indicator, streaming via placeholder edits
// when the adapter supports it, progress messages otherwise (conduit-31jg.35).
type channelTurnSink struct {
	g       *Gateway
	msg     *protocol.IncomingMessage
	session *sessions.Session

	typingOnce sync.Once
	typingDone chan struct{}

	// Streaming state (only when the adapter supports StreamingAdapter).
	streamer      channels.StreamingAdapter
	chatID        int64
	placeholderID int
	textBuilder   strings.Builder
	lastEditTime  time.Time
	lastEditLen   int
}

func newChannelTurnSink(g *Gateway, msg *protocol.IncomingMessage, session *sessions.Session) *channelTurnSink {
	return &channelTurnSink{g: g, msg: msg, session: session, typingDone: make(chan struct{})}
}

func (s *channelTurnSink) outgoing(idPrefix, text string) *protocol.OutgoingMessage {
	return &protocol.OutgoingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeOutgoingMessage,
			ID:        fmt.Sprintf("%s_%d", idPrefix, time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		ChannelID:  s.msg.ChannelID,
		SessionKey: s.msg.SessionKey,
		UserID:     s.msg.UserID,
		Text:       text,
	}
}

// Queued: conduit-1mnp busy-ack. If a turn is already in flight for this
// session, tell the user immediately instead of leaving them in silence. 30s
// cooldown per session so rapid nudges don't spam acks.
func (s *channelTurnSink) Queued(ctx context.Context) {
	now := time.Now().Unix()
	lastAck, _ := strconv.ParseInt(s.session.Context["last_busy_ack"], 10, 64)
	if now-lastAck <= 30 {
		return
	}
	s.g.channelManager.SendMessage(s.outgoing("busyack", busyAckText))
	_ = s.g.sessions.SetSessionContext(s.session.Key, "last_busy_ack", strconv.FormatInt(now, 10))
	logging.Info(ctx, "busy-ack sent, message queued behind in-flight turn", "session_key", s.session.Key)
}

// startTyping refreshes the typing indicator every 4 seconds until stopTyping.
func (s *channelTurnSink) startTyping() {
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		s.g.channelManager.SendTypingIndicator(s.msg.ChannelID, s.msg.UserID)
		for {
			select {
			case <-s.typingDone:
				return
			case <-ticker.C:
				s.g.channelManager.SendTypingIndicator(s.msg.ChannelID, s.msg.UserID)
			}
		}
	}()
}

func (s *channelTurnSink) stopTyping() { s.typingOnce.Do(func() { close(s.typingDone) }) }

func (s *channelTurnSink) Begin(ctx context.Context) ai.StreamCallback {
	s.startTyping()

	adapter, _ := s.g.channelManager.GetAdapter(s.msg.ChannelID)
	streamer, ok := adapter.(channels.StreamingAdapter)
	if !ok {
		return nil
	}
	chatID, _ := strconv.ParseInt(s.msg.UserID, 10, 64)
	placeholderID, err := streamer.SendMessageWithID(chatID, "...")
	if err != nil {
		logging.Warn(ctx, "streaming: failed to send placeholder", "error", err)
		return nil // non-streaming fallback
	}
	s.stopTyping() // we have a visible message now
	s.streamer, s.chatID, s.placeholderID = streamer, chatID, placeholderID

	const editInterval = 500 * time.Millisecond
	const minCharsForEdit = 50
	return func(delta string, done bool) {
		s.textBuilder.WriteString(delta)
		current := s.textBuilder.String()
		shouldEdit := done ||
			(time.Since(s.lastEditTime) >= editInterval && len(current) > minCharsForEdit) ||
			(len(current)-s.lastEditLen > 100) // Every 100 new chars since last edit
		if shouldEdit && len(current) > 0 {
			// Strip trailing silent tokens before showing to user
			if display := channels.StripTrailingSilentTokens(current); display != "" {
				if editErr := streamer.EditMessageText(chatID, placeholderID, display); editErr != nil {
					logging.Warn(ctx, "streaming: edit failed", "error", editErr)
				}
			}
			s.lastEditTime = time.Now()
			s.lastEditLen = len(current)
		}
	}
}

func (s *channelTurnSink) Progress(status string) {
	s.g.channelManager.SendMessage(s.outgoing("progress", status))
}

func (s *channelTurnSink) ToolEvent(ctx context.Context, ev tools.ToolEventInfo) {
	if ev.EventType == "thinking" {
		s.g.channelManager.SendTypingIndicator(s.msg.ChannelID, s.msg.UserID)
	}
	logging.Debug(ctx, "tool event", "channel", s.msg.ChannelID, "event", ev.EventType, "tool", ev.ToolName)
}

func (s *channelTurnSink) deletePlaceholder(ctx context.Context) {
	if s.streamer == nil {
		return
	}
	if err := s.streamer.DeleteMessage(s.chatID, s.placeholderID); err != nil {
		logging.Warn(ctx, "streaming: failed to delete placeholder", "error", err)
	}
}

// finishStreamed completes the placeholder message. It returns true when the
// reply was fully delivered by editing (no SendMessage needed).
func (s *channelTurnSink) finishStreamed(ctx context.Context, res *TurnResult) bool {
	if res.Silent {
		logging.Debug(ctx, "streaming: silent response pattern detected, deleting placeholder")
		s.deletePlaceholder(ctx)
		return true
	}
	text := res.Content
	if text == "" {
		// Router returned no final text; fall back to whatever streamed.
		text = s.textBuilder.String()
		if channels.IsSilentResponse(text) {
			s.deletePlaceholder(ctx)
			return true
		}
	}
	text = channels.SanitizeOutgoingText(text)
	if text == "" {
		logging.Warn(ctx, "streaming: final text empty after sanitization, deleting placeholder")
		s.deletePlaceholder(ctx)
		return true
	}
	if err := s.streamer.EditMessageText(s.chatID, s.placeholderID, text); err != nil {
		logging.Warn(ctx, "streaming: final edit failed, falling back to SendMessage", "error", err)
		s.deletePlaceholder(ctx)
		return false
	}
	return true
}

func (s *channelTurnSink) Finish(ctx context.Context, res *TurnResult) {
	s.stopTyping()

	switch {
	case res.Dropped:
		return // stopped while queued; /stop already replied
	case res.Err != nil:
		if s.streamer != nil {
			s.deletePlaceholder(ctx)
		}
		if res.Cancelled {
			return // Silent return, /stop already sent a message
		}
		s.g.channelManager.SendMessage(s.outgoing("error", ai.GetUserMessage(res.Err)))
		return
	}

	if s.streamer != nil && s.finishStreamed(ctx, res) {
		logging.Debug(ctx, "streaming: response delivered via message editing", "response_chars", len(res.Content))
		return
	}
	if !res.Delivered() {
		return // silent/empty: nothing to send
	}

	out := s.outgoing("response", res.Content)
	// Forward source message ID so reply tags can resolve [[reply_to_current]]
	if srcID, ok := s.msg.Metadata["message_id"]; ok && srcID != "" {
		out.Metadata = map[string]string{"source_message_id": srcID}
	}
	if err := s.g.channelManager.SendMessage(out); err != nil {
		logging.Error(ctx, "error sending response", "error", err)
	}
}
