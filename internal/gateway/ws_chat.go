package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/approval"
	"conduit/internal/protocol"
	"conduit/internal/sessions"
)

// sendToClient sends a protocol message to a WebSocket client (non-blocking)
func (g *Gateway) sendToClient(client *Client, msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message for client %s: %v", client.ID, err)
		return
	}

	select {
	case client.Send <- data:
	default:
		log.Printf("Client %s send buffer full, dropping message", client.ID)
	}
}

// sendErrorToClient sends an error response to a WebSocket client
func (g *Gateway) sendErrorToClient(client *Client, sessionKey, code, message string) {
	g.sendToClient(client, &protocol.ErrorResponse{
		BaseMessage: protocol.BaseMessage{
			Type:      protocol.TypeErrorResponse,
			ID:        fmt.Sprintf("err_%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
		},
		SessionKey: sessionKey,
		Code:       code,
		Message:    message,
	})
}

// handleWebSocketChat processes a chat message from a WebSocket client
func (g *Gateway) handleWebSocketChat(ctx context.Context, client *Client, msg *protocol.ChatMessage) {
	log.Printf("WebSocket chat from %s: %d chars (session: %s)", client.ID, len(msg.Text), msg.SessionKey)

	// Track activity
	if g.monitoring != nil && g.monitoring.MetricsCollector != nil {
		g.monitoring.MetricsCollector.MarkActivity()
	}

	// Determine user ID: prefer message field, fall back to client field
	userID := msg.UserID
	if userID == "" {
		userID = client.UserID
	}
	if userID == "" {
		userID = client.Role // fall back to client name
	}

	// Determine session key
	sessionKey := msg.SessionKey
	if sessionKey == "" {
		sessionKey = client.SessionKey()
	}

	// Retrieve existing session by key, or create a new one.
	var session *sessions.Session
	var err error
	if sessionKey != "" {
		session, err = g.sessions.GetSession(sessionKey)
	}
	if session == nil {
		channelID := fmt.Sprintf("tui_%s", userID)
		session, err = g.sessions.GetOrCreateSession(userID, channelID)
	}
	if err != nil {
		log.Printf("Error getting session for WS client %s: %v", client.ID, err)
		g.sendErrorToClient(client, sessionKey, "session_error", "Failed to get or create session")
		return
	}

	// Update client's active session
	client.SetSessionKey(session.Key) // conduit-31jg.25

	// conduit-31jg.43: consume approval replies before the transcript and the
	// per-session turn lock (see approval_wiring.go).
	notify := g.wsApprovalNotifier(client, session.Key)
	if g.approvals.HandleReply(ctx, approval.Inbound{
		ChannelID: session.ChannelID, UserID: userID, SessionKey: session.Key,
		Text: msg.Text, Notify: notify,
	}) {
		return
	}

	// Check for commands
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		g.handleWebSocketCommandFromChat(ctx, client, session.Key, text)
		return
	}

	if g.ai == nil {
		g.sendErrorToClient(client, session.Key, "ai_error", "AI is not available")
		return
	}

	requestID := msg.RequestID
	if requestID == "" {
		requestID = fmt.Sprintf("req_%d", time.Now().UnixNano())
	}

	// conduit-31jg.35: the shared TurnRunner owns the turn lock, transcript
	// persistence (inside the lock, conduit-31jg.22), /stop registration
	// (conduit-31jg.23), SPAR reflection, cost and compaction.
	g.turns().Run(ctx, TurnRequest{
		Session:   session,
		ChannelID: session.ChannelID,
		UserID:    userID,
		Text:      msg.Text,
		Origin: &approval.Origin{ // conduit-31jg.43
			Source: "websocket", ChannelID: session.ChannelID, UserID: userID,
			SessionKey: session.Key, Notify: notify,
		},
		SanitizeStored: true,
	}, &wsTurnSink{g: g, client: client, sessionKey: session.Key, requestID: requestID})
}
