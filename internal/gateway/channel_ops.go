package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/protocol"
	"conduit/internal/sessions"
	"conduit/internal/tools/types"
)

// SendMessage implements the ChannelSender interface for tools
func (g *Gateway) SendMessage(ctx context.Context, channelID, userID, content string, metadata map[string]string) error {
	outgoingMsg := &protocol.OutgoingMessage{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeOutgoingMessage,
			ID:        fmt.Sprintf("tool_msg_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		ChannelID: channelID,
		UserID:    userID,
		Text:      content,
		Metadata:  metadata,
	}

	return g.channelManager.SendMessage(outgoingMsg)
}

// GetChannelStatusMap implements the ChannelSender interface for rich error messages
func (g *Gateway) GetChannelStatusMap() map[string]string {
	if g.channelManager == nil {
		return map[string]string{}
	}
	return g.channelManager.GetStatusMap()
}

// GetAvailableTargets implements the ChannelSender interface for rich error messages
func (g *Gateway) GetAvailableTargets() []string {
	if g.channelManager == nil {
		return []string{"No channels configured"}
	}
	return g.channelManager.GetAvailableTargets()
}

// SendToSession sends a message to another session by key or label.
// The message is added to the target session's history as a "user" message
// with source:inter_session metadata. No session wakeup — the message is
// queued and processed on the target session's next activation.
// Use SendToSessionWake to also trigger immediate processing.
func (g *Gateway) SendToSession(ctx context.Context, sessionKey, label, message string) error {
	// Resolve target session
	var targetSession *sessions.Session
	var err error

	if sessionKey != "" {
		targetSession, err = g.sessions.GetSession(sessionKey)
		if err != nil {
			return fmt.Errorf("session not found by key %q: %w", sessionKey, err)
		}
	} else if label != "" {
		targetSession, err = g.sessions.GetSessionByLabel(label)
		if err != nil {
			return fmt.Errorf("session not found by label %q: %w", label, err)
		}
	} else {
		return fmt.Errorf("either sessionKey or label must be provided")
	}

	// Build metadata for inter-session message
	metadata := map[string]string{
		"source": "inter_session",
	}
	// Attach sender info from context if available
	if senderSession := types.RequestSessionKey(ctx); senderSession != "" {
		metadata["sender_session"] = senderSession
	}
	if senderUser := types.RequestUserID(ctx); senderUser != "" {
		metadata["sender_user"] = senderUser
	}

	// Add message to target session as "user" role
	_, err = g.sessions.AddMessage(targetSession.Key, "user", message, metadata)
	if err != nil {
		return fmt.Errorf("failed to add message to session %q: %w", targetSession.Key, err)
	}

	log.Printf("[SendToSession] Message delivered to session %s (via %s)",
		targetSession.Key, map[bool]string{true: "key", false: "label"}[sessionKey != ""])

	return nil
}

// SendToSessionWake sends a message to another session and then signals the session
// to wake up and process it immediately. This is equivalent to SendToSession followed
// by an immediate re-activation of the target session's AI processing loop.
//
// A recursion guard (wake_depth in session context) limits nesting to 3 levels deep
// to prevent infinite wakeup loops when sessions message each other.
func (g *Gateway) SendToSessionWake(ctx context.Context, sessionKey, label, message string) error {
	return g.sendToSessionWakeWithSource(ctx, sessionKey, label, message, "")
}

// sendToSessionWakeWithSource is the internal variant that lets the caller tag the
// wake with a specific source (e.g. "sub_agent_callback") so the target session's
// prompt/processing path can tell it apart from a plain inter-session send.
// When wakeSource is empty the tag defaults to "inter_session".
func (g *Gateway) sendToSessionWakeWithSource(ctx context.Context, sessionKey, label, message, wakeSource string) error {
	// Resolve target session
	var targetSession *sessions.Session
	var err error

	if sessionKey != "" {
		targetSession, err = g.sessions.GetSession(sessionKey)
		if err != nil {
			return fmt.Errorf("session not found by key %q: %w", sessionKey, err)
		}
	} else if label != "" {
		targetSession, err = g.sessions.GetSessionByLabel(label)
		if err != nil {
			return fmt.Errorf("session not found by label %q: %w", label, err)
		}
	} else {
		return fmt.Errorf("either sessionKey or label must be provided")
	}

	// Build metadata for inter-session message
	metadata := map[string]string{
		"source": "inter_session",
	}
	if wakeSource != "" {
		metadata["wake_source"] = wakeSource
	}
	if senderSession := types.RequestSessionKey(ctx); senderSession != "" {
		metadata["sender_session"] = senderSession
	}
	if senderUser := types.RequestUserID(ctx); senderUser != "" {
		metadata["sender_user"] = senderUser
	}

	// Add message to target session as "user" role
	_, err = g.sessions.AddMessage(targetSession.Key, "user", message, metadata)
	if err != nil {
		return fmt.Errorf("failed to add message to session %q: %w", targetSession.Key, err)
	}

	log.Printf("[SendToSessionWake] Message delivered to session %s, signalling wake (source=%q)", targetSession.Key, wakeSource)

	g.enqueueSessionWake(targetSession.Key)

	return nil
}

// enqueueSessionWake signals the wake listener for sessionKey.
//
// If a wake for the same session is already buffered (pendingWakeKeys slot
// held), the new signal is coalesced — we just bump the coalesce counter and
// return. The existing buffered wake will trigger exactly one AI processing
// loop that picks up whatever messages are in the session by then, which
// matches the intent of multiple back-to-back wakes.
//
// Only when there is no existing pending wake AND the channel is full do we
// count a real drop: the target session will not be woken until its next
// natural activation. See conduit-t38m.
func (g *Gateway) enqueueSessionWake(sessionKey string) {
	g.pendingWakeMu.Lock()
	if g.pendingWakeKeys == nil {
		g.pendingWakeKeys = make(map[string]struct{})
	}
	if _, already := g.pendingWakeKeys[sessionKey]; already {
		g.pendingWakeMu.Unlock()
		if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
			g.monitoring.GatewayMetrics.IncrementSessionWakeCoalesced()
		}
		log.Printf("[SendToSessionWake] Wake for session %s already pending, coalescing", sessionKey)
		return
	}
	// Reserve the slot before we attempt the send. If the send fails we back
	// out. Holding the mutex across the channel send keeps the reservation and
	// the buffer state consistent with each other — the drainer only clears
	// the slot under this same mutex.
	g.pendingWakeKeys[sessionKey] = struct{}{}
	select {
	case g.sessionWake <- sessionKey:
		g.pendingWakeMu.Unlock()
	default:
		delete(g.pendingWakeKeys, sessionKey)
		g.pendingWakeMu.Unlock()
		if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
			g.monitoring.GatewayMetrics.IncrementSessionWakeDrop()
		}
		log.Printf("[SendToSessionWake] Wake channel full, session %s will process on next activation", sessionKey)
	}
}

// clearPendingWake removes the pending-wake reservation for sessionKey. Called
// by the wake-listener goroutine in Start() as soon as a key is dequeued from
// sessionWake, so subsequent wakes can enqueue again.
func (g *Gateway) clearPendingWake(sessionKey string) {
	g.pendingWakeMu.Lock()
	delete(g.pendingWakeKeys, sessionKey)
	g.pendingWakeMu.Unlock()
}
