package gateway

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/sessions"
	"conduit/internal/tools/types"
	"conduit/pkg/protocol"
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
