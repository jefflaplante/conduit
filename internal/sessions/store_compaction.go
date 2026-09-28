package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"conduit/internal/database"
	"github.com/google/uuid"
)

// ErrCompactionStale is returned by ApplyCompaction when one or more of the
// messages it was asked to replace no longer exist (a concurrent /reset, or a
// concurrent compaction already replaced them). Nothing is changed.
var ErrCompactionStale = errors.New("compaction snapshot is stale: compacted messages changed concurrently")

// CompactionSummary describes the summary message ApplyCompaction inserts.
type CompactionSummary struct {
	Role     string
	Content  string
	Metadata map[string]string
	// Before is the timestamp of the oldest message being retained. History is
	// ordered by timestamp (message IDs are random UUIDs), so the summary is
	// stamped 1ns before this to sort ahead of every retained message and of
	// anything appended while the summary was being generated. Zero means no
	// message is retained; the summary is then stamped time.Now().
	Before time.Time
}

// ApplyCompaction atomically replaces exactly the given message IDs with a
// single summary message (conduit-31jg.21).
//
// In ONE transaction it deletes only compactedIDs (scoped to sessionKey),
// inserts the summary and recomputes sessions.message_count. Messages not in
// compactedIDs — including any appended after the caller's snapshot — are
// never touched. If any compacted ID is already gone, the transaction is
// rolled back and ErrCompactionStale is returned. Any failure leaves history
// exactly as it was.
func (s *Store) ApplyCompaction(sessionKey string, compactedIDs []string, summary CompactionSummary) (*Message, error) {
	if len(compactedIDs) == 0 {
		return nil, fmt.Errorf("apply compaction: no message IDs to compact")
	}
	role := summary.Role
	if role == "" {
		role = "assistant"
	}
	ts := time.Now()
	if !summary.Before.IsZero() {
		ts = summary.Before.Add(-time.Nanosecond)
	}
	msg := &Message{
		ID:         uuid.New().String(),
		SessionKey: sessionKey,
		Role:       role,
		Content:    summary.Content,
		Timestamp:  ts,
		Metadata:   summary.Metadata,
	}
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]string)
	}
	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	err = database.RetryOnBusy(5, func() error {
		tx, err := s.db.Begin() // _txlock=immediate: takes the write lock up front
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit

		del, err := tx.Prepare(`DELETE FROM messages WHERE id = ? AND session_key = ?`)
		if err != nil {
			return err
		}
		defer del.Close()

		var deleted int64
		for _, id := range compactedIDs {
			res, err := del.Exec(id, sessionKey)
			if err != nil {
				return fmt.Errorf("delete compacted message: %w", err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			deleted += n
		}
		if deleted != int64(len(compactedIDs)) {
			return ErrCompactionStale
		}

		if _, err := tx.Exec(`
			INSERT INTO messages (id, session_key, role, content, timestamp, metadata)
			VALUES (?, ?, ?, ?, ?, ?)
		`, msg.ID, msg.SessionKey, msg.Role, msg.Content, msg.Timestamp, string(metadataJSON)); err != nil {
			return fmt.Errorf("insert summary: %w", err)
		}

		if _, err := tx.Exec(`
			UPDATE sessions
			SET message_count = (SELECT COUNT(*) FROM messages WHERE session_key = ?),
			    updated_at = ?
			WHERE key = ?
		`, sessionKey, nowUpdatedAt(), sessionKey); err != nil {
			return fmt.Errorf("update message count: %w", err)
		}

		return tx.Commit()
	})
	if err != nil {
		if errors.Is(err, ErrCompactionStale) {
			return nil, err
		}
		return nil, fmt.Errorf("apply compaction: %w", err)
	}

	// Best-effort search.db sync, after commit (same contract as AddMessage).
	if s.onMessagesDeleted != nil {
		s.onMessagesDeleted(sessionKey, compactedIDs)
	}
	if s.onMessageAdded != nil {
		s.onMessageAdded(msg.ID, msg.SessionKey, msg.Role, msg.Content)
	}

	return msg, nil
}
