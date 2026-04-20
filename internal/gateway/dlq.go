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
