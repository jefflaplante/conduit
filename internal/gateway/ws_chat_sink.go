package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
	"conduit/internal/channels"
	"conduit/internal/protocol"
	"conduit/internal/tools"
	"conduit/internal/tui"
)

// wsTurnSink renders a TurnRunner turn as the WebSocket streaming protocol
// (StreamStart / StreamDelta / ToolEvent / StreamEnd).
type wsTurnSink struct {
	g          *Gateway
	client     *Client
	sessionKey string
	requestID  string
}

// queuedNoticeText is the WS/TUI queued notice (conduit-31jg.66).
const queuedNoticeText = "Queued — I'll handle this right after the current request."

// Queued tells the client its message is waiting behind the session's
// running turn (conduit-31jg.66); its StreamStart arrives once that turn has
// finished. Sent as a command_response with command "queued", which older
// clients show as a plain system line.
func (s *wsTurnSink) Queued(context.Context) {
	s.g.sendToClient(s.client, &protocol.CommandResponse{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeCommandResponse,
			ID:        fmt.Sprintf("cr_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		Command:    tui.QueuedNoticeCommand,
		Response:   queuedNoticeText,
	})
}

func (s *wsTurnSink) Begin(context.Context) ai.StreamCallback {
	s.g.sendToClient(s.client, &protocol.StreamStart{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamStart,
			ID:        fmt.Sprintf("ss_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
	})
	return func(delta string, done bool) {
		if delta == "" {
			return
		}
		s.g.sendToClient(s.client, &protocol.StreamDelta{
			BaseMessage: protocol.BaseMessage{
				Type:      protocol.TypeStreamDelta,
				ID:        fmt.Sprintf("sd_%d", time.Now().UnixNano()),
				Timestamp: time.Now(),
			},
			SessionKey: s.sessionKey,
			RequestID:  s.requestID,
			Delta:      delta,
		})
	}
}

func (s *wsTurnSink) Progress(string) {}

func (s *wsTurnSink) ToolEvent(_ context.Context, event tools.ToolEventInfo) {
	s.g.sendToClient(s.client, &protocol.ToolEvent{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeToolEvent,
			ID:        fmt.Sprintf("te_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
		ToolName:   event.ToolName,
		EventType:  event.EventType,
		Args:       fmt.Sprintf("%v", event.Args),
		Result:     event.Result,
		Error:      event.Error,
		Duration:   event.Duration,
	})
}

func (s *wsTurnSink) streamEnd(content string, res *TurnResult) {
	end := &protocol.StreamEnd{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeStreamEnd,
			ID:        fmt.Sprintf("se_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: s.sessionKey,
		RequestID:  s.requestID,
		Content:    content,
	}
	if res != nil && res.Usage != nil {
		end.PromptTokens = res.Usage.PromptTokens
		end.CompletionTokens = res.Usage.CompletionTokens
		end.TotalTokens = res.Usage.TotalTokens
		end.Model = res.Model
		end.RequestCost = res.RequestCost
		end.SessionCost = res.SessionCost
	}
	s.g.sendToClient(s.client, end)
}

func (s *wsTurnSink) Finish(_ context.Context, res *TurnResult) {
	switch {
	case res.Dropped:
		return // stopped while queued; nothing was started
	case res.Cancelled:
		log.Printf("WS request cancelled for session: %s", s.sessionKey)
		return
	case res.Err != nil:
		log.Printf("Error generating AI response for WS client: %v", res.Err)
		s.g.sendErrorToClient(s.client, s.sessionKey, "ai_error", ai.UserFriendlyError(res.Err))
		// Send StreamEnd with empty content to signal completion
		s.streamEnd("", nil)
		return
	}
	if !res.Delivered() {
		// Silent/empty: StreamEnd with empty content so the client stops its
		// streaming state.
		log.Printf("Silent response detected in WS chat (%d chars), suppressing", len(res.Raw))
		s.streamEnd("", res)
		return
	}
	// Sanitize internal markers — TUI doesn't support reply threading
	s.streamEnd(channels.SanitizeOutgoingText(res.Content), res)
}
