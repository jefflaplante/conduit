package gateway

import (
	"context"
	"database/sql"
	"time"

	"conduit/internal/protocol"
)

// writeIngestDLQ records a dropped ingress message to the ingest_dlq table.
// Used by the backpressure branch in processMessages and by the WebSocket chat
// ingress when msgSemaphore is full. A short timeout bounds the write so a
// slow DB never blocks the ingest goroutine (the caller is already on the hot
// path). Failures are intentionally non-fatal — the DLQ is best-effort audit,
// not an acknowledgement path.
func writeIngestDLQ(db *sql.DB, msg *protocol.IncomingMessage, reason string) error {
	if db == nil || msg == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := db.ExecContext(ctx,
		`INSERT INTO ingest_dlq (channel_id, user_id, session_key, text, reason)
		 VALUES (?, ?, ?, ?, ?)`,
		msg.ChannelID, msg.UserID, msg.SessionKey, msg.Text, reason,
	)
	return err
}

// writeClientChatDLQ records a dropped WebSocket chat message. WebSocket chat
// carries its own shape (protocol.ChatMessage) so user_id/session_key come from
// the Client struct rather than the message payload.
func writeClientChatDLQ(db *sql.DB, clientID, userID, sessionKey, text, reason string) error {
	if db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// channel_id column stores "websocket:<clientID>" for WS drops so operators
	// can distinguish them from channel-adapter drops.
	_, err := db.ExecContext(ctx,
		`INSERT INTO ingest_dlq (channel_id, user_id, session_key, text, reason)
		 VALUES (?, ?, ?, ?, ?)`,
		"websocket:"+clientID, userID, sessionKey, text, reason,
	)
	return err
}

// recordIngestDrop emits the gateway.ingest.drops{channel} metric and writes a
// DLQ row for an ingress message that could not be handed off because
// msgSemaphore was full. Silent drops were the bug in conduit-101n.
func (g *Gateway) recordIngestDrop(msg *protocol.IncomingMessage, reason string) {
	g.logger.Warn("request backpressure: dropping channel message",
		"channel_id", msg.ChannelID, "reason", reason)

	if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
		g.monitoring.GatewayMetrics.IncrementIngestDrop(msg.ChannelID)
	}
	if g.sessions != nil {
		if err := writeIngestDLQ(g.sessions.DB(), msg, reason); err != nil {
			g.logger.Error("failed to write ingest DLQ row",
				"channel_id", msg.ChannelID, "error", err)
		}
	}
}

// recordWebSocketDrop is the WS-chat analogue of recordIngestDrop. WS clients
// don't have a channel adapter, so the drop is counted under the synthetic
// "websocket" channel label and the DLQ row records the originating client.
func (g *Gateway) recordWebSocketDrop(client *Client, msg *protocol.ChatMessage, reason string) {
	g.logger.Warn("request backpressure: dropping chat message",
		"client_id", client.ID, "reason", reason)

	if g.monitoring != nil && g.monitoring.GatewayMetrics != nil {
		g.monitoring.GatewayMetrics.IncrementIngestDrop("websocket")
	}
	if g.sessions != nil {
		if err := writeClientChatDLQ(g.sessions.DB(), client.ID, client.UserID, msg.SessionKey, msg.Text, reason); err != nil {
			g.logger.Error("failed to write ingest DLQ row",
				"client_id", client.ID, "error", err)
		}
	}
}
