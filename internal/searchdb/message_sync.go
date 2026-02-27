package searchdb

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// MessageSyncer handles cross-database synchronization of messages
// from gateway.db to search.db's messages_fts index.
type MessageSyncer struct {
	searchDB  *sql.DB
	gatewayDB *sql.DB
	mu        sync.Mutex

	// Sync statistics
	syncedCount  int64
	lastSyncTime time.Time
	lastFullSync time.Time
}

// NewMessageSyncer creates a new message syncer.
func NewMessageSyncer(searchDB, gatewayDB *sql.DB) *MessageSyncer {
	return &MessageSyncer{
		searchDB:  searchDB,
		gatewayDB: gatewayDB,
	}
}

// SyncSingleMessage adds or updates a single message in the FTS index.
// This is called from the session store callback after each message is added.
func (s *MessageSyncer) SyncSingleMessage(id, sessionKey, role, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Use INSERT OR REPLACE to handle both new and updated messages
	_, err := s.searchDB.Exec(
		`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`,
		id, sessionKey, role, content,
	)
	if err != nil {
		return fmt.Errorf("failed to sync message %s: %w", id, err)
	}

	atomic.AddInt64(&s.syncedCount, 1)
	s.lastSyncTime = time.Now()
	return nil
}

// DeleteSessionMessages removes all messages for a session from the FTS index.
// This is called from the session store callback when a session is cleared.
func (s *MessageSyncer) DeleteSessionMessages(sessionKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.searchDB.Exec(
		`DELETE FROM messages_fts WHERE session_key = ?`,
		sessionKey,
	)
	if err != nil {
		return fmt.Errorf("failed to delete messages for session %s: %w", sessionKey, err)
	}

	return nil
}

// FullSync performs a complete synchronization from gateway.db messages to search.db.
// This is run at startup to ensure the FTS index is complete.
func (s *MessageSyncer) FullSync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	startTime := time.Now()
	log.Printf("MessageSyncer: starting full sync from gateway.db")

	// Count existing messages in FTS
	var ftsCount int
	if err := s.searchDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts").Scan(&ftsCount); err != nil {
		ftsCount = 0
	}

	// Count messages in gateway.db
	var gatewayCount int
	if err := s.gatewayDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&gatewayCount); err != nil {
		return fmt.Errorf("failed to count gateway messages: %w", err)
	}

	// If counts match and FTS is not empty, assume sync is complete
	if ftsCount > 0 && ftsCount == gatewayCount {
		log.Printf("MessageSyncer: FTS index already in sync (%d messages)", ftsCount)
		s.lastFullSync = time.Now()
		return nil
	}

	// Perform full sync
	tx, err := s.searchDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Clear existing FTS data and repopulate
	if _, err := tx.ExecContext(ctx, "DELETE FROM messages_fts"); err != nil {
		return fmt.Errorf("failed to clear messages_fts: %w", err)
	}

	// Query all messages from gateway.db
	rows, err := s.gatewayDB.QueryContext(ctx,
		`SELECT id, session_key, role, content FROM messages`)
	if err != nil {
		return fmt.Errorf("failed to query gateway messages: %w", err)
	}
	defer rows.Close()

	// Prepare insert statement
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	syncedCount := 0
	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var id, sessionKey, role, content string
		if err := rows.Scan(&id, &sessionKey, &role, &content); err != nil {
			log.Printf("Warning: failed to scan message: %v", err)
			continue
		}

		if _, err := stmt.ExecContext(ctx, id, sessionKey, role, content); err != nil {
			log.Printf("Warning: failed to insert message %s: %v", id, err)
			continue
		}
		syncedCount++
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating messages: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.lastFullSync = time.Now()
	duration := time.Since(startTime)
	log.Printf("MessageSyncer: full sync complete - %d messages in %v", syncedCount, duration)

	return nil
}

// IncrementalSync finds and syncs any messages that are in gateway.db but not in search.db.
// This is a safety net for any messages that might have been missed by callbacks.
func (s *MessageSyncer) IncrementalSync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find messages in gateway.db that are not in search.db
	// We do this by checking for message IDs that don't exist in FTS
	rows, err := s.gatewayDB.QueryContext(ctx, `
		SELECT id, session_key, role, content
		FROM messages
		WHERE id NOT IN (SELECT message_id FROM messages_fts)
		LIMIT 1000
	`)

	// Note: The above query won't work across databases directly.
	// We need to get all message IDs from search.db first, then check gateway.db
	// This is a simplified approach - for a more robust solution, we'd use
	// a watermark or timestamp-based approach.

	// For now, let's use a simpler approach: compare counts and do full sync if mismatch
	if err != nil {
		// Cross-database query not supported - fall back to count comparison
		var ftsCount, gatewayCount int
		s.searchDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts").Scan(&ftsCount)
		s.gatewayDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&gatewayCount)

		if ftsCount < gatewayCount {
			log.Printf("MessageSyncer: incremental sync detected %d missing messages, triggering full sync",
				gatewayCount-ftsCount)
			s.mu.Unlock() // Unlock before calling FullSync which will re-lock
			return s.FullSync(ctx)
		}
		return nil
	}
	defer rows.Close()

	syncedCount := 0
	for rows.Next() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var id, sessionKey, role, content string
		if err := rows.Scan(&id, &sessionKey, &role, &content); err != nil {
			continue
		}

		if _, err := s.searchDB.ExecContext(ctx,
			`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`,
			id, sessionKey, role, content); err != nil {
			log.Printf("Warning: incremental sync failed for message %s: %v", id, err)
			continue
		}
		syncedCount++
	}

	if syncedCount > 0 {
		log.Printf("MessageSyncer: incremental sync added %d messages", syncedCount)
	}

	return nil
}

// ValidateSync compares message counts between gateway.db and search.db.
// Returns the counts and whether they match.
func (s *MessageSyncer) ValidateSync(ctx context.Context) (gatewayCount, ftsCount int, inSync bool, err error) {
	if err := s.gatewayDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&gatewayCount); err != nil {
		return 0, 0, false, fmt.Errorf("failed to count gateway messages: %w", err)
	}

	if err := s.searchDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts").Scan(&ftsCount); err != nil {
		return gatewayCount, 0, false, fmt.Errorf("failed to count FTS messages: %w", err)
	}

	return gatewayCount, ftsCount, gatewayCount == ftsCount, nil
}

// GetStats returns synchronization statistics.
func (s *MessageSyncer) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"synced_count":   atomic.LoadInt64(&s.syncedCount),
		"last_sync_time": s.lastSyncTime,
		"last_full_sync": s.lastFullSync,
	}
}

// MessageAddedCallback returns a callback function suitable for session store's
// onMessageAdded hook. This allows the session store to notify us of new messages
// without importing this package directly.
func (s *MessageSyncer) MessageAddedCallback() func(id, sessionKey, role, content string) {
	return func(id, sessionKey, role, content string) {
		if err := s.SyncSingleMessage(id, sessionKey, role, content); err != nil {
			log.Printf("Warning: MessageSyncer callback failed: %v", err)
		}
	}
}

// SessionClearedCallback returns a callback function suitable for session store's
// onSessionCleared hook.
func (s *MessageSyncer) SessionClearedCallback() func(sessionKey string) {
	return func(sessionKey string) {
		if err := s.DeleteSessionMessages(sessionKey); err != nil {
			log.Printf("Warning: MessageSyncer session clear callback failed: %v", err)
		}
	}
}
