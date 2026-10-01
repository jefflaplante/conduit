package sessions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"conduit/internal/database"
	"github.com/google/uuid"
)

// AddMessage adds a message to a session
func (s *Store) AddMessage(sessionKey, role, content string, metadata map[string]string) (*Message, error) {
	message := &Message{
		ID:         uuid.New().String(),
		SessionKey: sessionKey,
		Role:       role,
		Content:    content,
		Timestamp:  time.Now(),
		Metadata:   metadata,
	}

	if message.Metadata == nil {
		message.Metadata = make(map[string]string)
	}

	metadataJSON, err := json.Marshal(message.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	// conduit-31jg.24: the insert and the message_count/updated_at bump are
	// one transaction, so a failed count update can no longer leave an
	// orphan message behind (or a count that disagrees with the rows).
	err = database.RetryOnBusy(5, func() error {
		tx, err := s.db.Begin() // _txlock=immediate: takes the write lock up front
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit

		if _, err := tx.Exec(`
			INSERT INTO messages (id, session_key, role, content, timestamp, metadata)
			VALUES (?, ?, ?, ?, ?, ?)
		`,
			message.ID,
			message.SessionKey,
			message.Role,
			message.Content,
			formatStoredTime(message.Timestamp), // conduit-a636: canonical UTC text
			string(metadataJSON),
		); err != nil {
			return fmt.Errorf("failed to save message: %w", err)
		}

		if err := incrementMessageCountTx(tx, sessionKey); err != nil {
			return err
		}
		return tx.Commit()
	})

	if err != nil {
		return nil, err
	}

	// Sync to search.db FTS5 index via callback, after commit (best-effort —
	// never fails the message insert).
	if s.onMessageAdded != nil {
		s.onMessageAdded(message.ID, message.SessionKey, message.Role, message.Content)
	}

	// Mark session activity
	s.stateTracker.MarkActivity(sessionKey)

	return message, nil
}

// DefaultMessageLimit is the maximum number of messages returned when no explicit
// limit is provided (limit <= 0). This prevents unbounded memory growth from
// sessions with very long histories.
const DefaultMessageLimit = 10000

// GetMessages retrieves messages for a session.
// If limit <= 0, DefaultMessageLimit is used to prevent unbounded reads.
func (s *Store) GetMessages(sessionKey string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = DefaultMessageLimit
	}

	// Use subquery to get most recent N messages, then order chronologically.
	// Without this, LIMIT + ASC gives oldest messages, not newest.
	// conduit-31jg.50: rowid breaks timestamp ties in insertion order (ids
	// are random UUIDs). VACUUM may renumber rowids but keeps their order.
	query := fmt.Sprintf(`
		SELECT id, session_key, role, content, timestamp, metadata
		FROM (
			SELECT rowid AS rid, id, session_key, role, content, timestamp, metadata
			FROM messages
			WHERE session_key = ?
			ORDER BY timestamp DESC, rowid DESC
			LIMIT %d
		) sub
		ORDER BY timestamp ASC, rid ASC
	`, limit)

	rows, err := s.db.Query(query, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	messages := make([]Message, 0, min(limit, 256))

	for rows.Next() {
		var message Message
		var metadataJSON string

		err := rows.Scan(
			&message.ID,
			&message.SessionKey,
			&message.Role,
			&message.Content,
			&message.Timestamp,
			&metadataJSON,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Parse metadata JSON
		if err := json.Unmarshal([]byte(metadataJSON), &message.Metadata); err != nil {
			message.Metadata = make(map[string]string)
		}

		messages = append(messages, message)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return messages, nil
}

// incrementMessageCountTx increments the message count for a session inside
// tx. Uses atomic increment instead of COUNT(*) subquery to avoid a table
// scan.
func incrementMessageCountTx(tx *sql.Tx, sessionKey string) error {
	if _, err := tx.Exec(`
		UPDATE sessions
		SET message_count = message_count + 1,
		    updated_at = ?
		WHERE key = ?
	`, nowUpdatedAt(), sessionKey); err != nil {
		return fmt.Errorf("failed to update session message count: %w", err)
	}
	return nil
}

// ClearSessionMessages deletes all messages for a session (keeps the session record).
// Intended for explicit user resets (/reset, /new, /goodbye). Do NOT use it to
// implement compaction — use ApplyCompaction, which is atomic and never drops
// messages that arrived concurrently (conduit-31jg.21).
func (s *Store) ClearSessionMessages(sessionKey string) error {
	// Clear search.db FTS5 index via callback (best-effort)
	if s.onSessionCleared != nil {
		s.onSessionCleared(sessionKey)
	}

	// Delete all messages for the session
	err := database.RetryOnBusy(5, func() error {
		_, execErr := s.db.Exec(`DELETE FROM messages WHERE session_key = ?`, sessionKey)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}

	// Update the session's message count to 0
	err = database.RetryOnBusy(5, func() error {
		_, execErr := s.db.Exec(`
			UPDATE sessions
			SET message_count = 0, updated_at = ?
			WHERE key = ?
		`, nowUpdatedAt(), sessionKey)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}
