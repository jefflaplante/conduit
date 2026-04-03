package searchdb

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/database"
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

	err := database.RetryOnBusy(5, func() error {
		_, err := s.searchDB.Exec(
			`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`,
			id, sessionKey, role, content,
		)
		return err
	})
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
	// First, check if we need a full sync (without holding the lock during the check)
	needsFullSync, missingCount := s.checkSyncNeeded(ctx)
	if needsFullSync {
		log.Printf("MessageSyncer: incremental sync detected %d missing messages, triggering full sync",
			missingCount)
		return s.FullSync(ctx)
	}

	// No sync needed
	return nil
}

// checkSyncNeeded compares counts between gateway and FTS to determine if sync is needed.
// Returns (needsSync, missingCount).
func (s *MessageSyncer) checkSyncNeeded(ctx context.Context) (bool, int) {
	var ftsCount, gatewayCount int

	if err := s.searchDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages_fts").Scan(&ftsCount); err != nil {
		return false, 0
	}
	if err := s.gatewayDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages").Scan(&gatewayCount); err != nil {
		return false, 0
	}

	if ftsCount < gatewayCount {
		return true, gatewayCount - ftsCount
	}
	return false, 0
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

// syncMessage is a message queued for async sync.
type syncMessage struct {
	id, sessionKey, role, content string
}

// AsyncMessageSyncer wraps MessageSyncer with a buffered channel so the
// onMessageAdded callback never blocks the chat response path.
type AsyncMessageSyncer struct {
	syncer *MessageSyncer
	queue  chan syncMessage
	done   chan struct{}
}

// NewAsyncMessageSyncer creates an AsyncMessageSyncer with the given buffer size.
func NewAsyncMessageSyncer(syncer *MessageSyncer, bufferSize int) *AsyncMessageSyncer {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	a := &AsyncMessageSyncer{
		syncer: syncer,
		queue:  make(chan syncMessage, bufferSize),
		done:   make(chan struct{}),
	}
	go a.drain()
	return a
}

// drain processes queued messages until the channel is closed.
func (a *AsyncMessageSyncer) drain() {
	defer close(a.done)
	for msg := range a.queue {
		if err := a.syncer.SyncSingleMessage(msg.id, msg.sessionKey, msg.role, msg.content); err != nil {
			log.Printf("Warning: async message sync failed for %s: %v", msg.id, err)
		}
	}
}

// MessageAddedCallback returns a non-blocking callback for session store.
// If the queue is full the message is dropped (IncrementalSync catches it later).
func (a *AsyncMessageSyncer) MessageAddedCallback() func(id, sessionKey, role, content string) {
	return func(id, sessionKey, role, content string) {
		select {
		case a.queue <- syncMessage{id, sessionKey, role, content}:
		default:
			log.Printf("Warning: async message sync queue full, dropping message %s (incremental sync will catch it)", id)
		}
	}
}

// Close closes the queue and waits for all pending messages to be processed.
func (a *AsyncMessageSyncer) Close() {
	close(a.queue)
	<-a.done
}
