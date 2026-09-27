package sessions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"conduit/internal/database"
	"conduit/internal/ftsquery"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// MessageAddedCallback is called when a message is added to a session.
// Parameters: id, sessionKey, role, content
type MessageAddedCallback func(id, sessionKey, role, content string)

// SessionClearedCallback is called when a session's messages are cleared.
// Parameters: sessionKey
type SessionClearedCallback func(sessionKey string)

// MessagesDeletedCallback is called after specific messages are deleted from a
// session (e.g. by ApplyCompaction). Parameters: sessionKey, message IDs.
type MessagesDeletedCallback func(sessionKey string, ids []string)

// Store manages conversation sessions
type Store struct {
	db           *sql.DB
	stateTracker *SessionStateTracker

	// Callbacks for search index synchronization
	onMessageAdded    MessageAddedCallback
	onSessionCleared  SessionClearedCallback
	onMessagesDeleted MessagesDeletedCallback
}

// Session represents a conversation session
type Session struct {
	Key          string            `json:"key"`
	UserID       string            `json:"user_id"`
	ChannelID    string            `json:"channel_id"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	MessageCount int               `json:"message_count"`
	Context      map[string]string `json:"context"`
	Messages     []Message         `json:"messages,omitempty"`

	// State tracking fields
	State        SessionState `json:"state,omitempty"`
	LastActivity time.Time    `json:"last_activity,omitempty"`
	StateChanged time.Time    `json:"state_changed,omitempty"`
}

// Message represents a single message in a session
type Message struct {
	ID         string            `json:"id"`
	SessionKey string            `json:"session_key"`
	Role       string            `json:"role"` // "user", "assistant", "system"
	Content    string            `json:"content"`
	Timestamp  time.Time         `json:"timestamp"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// updatedAtLayout is the canonical on-disk format for sessions.updated_at:
// UTC, fixed-width nanoseconds, so lexicographic ORDER BY / < comparisons
// match chronological order and modernc parses it back into a time.Time.
//
// conduit-31jg.24: previously updated_at was written both by Go (the
// driver's time.Time.String(), in the process's local zone, with a
// monotonic "m=+..." suffix) and by SQL CURRENT_TIMESTAMP (UTC, second
// precision), so ORDER BY updated_at could pick the wrong "latest" session.
const updatedAtLayout = "2006-01-02 15:04:05.000000000"

// canonicalUpdatedAtGlob matches an updated_at already in updatedAtLayout.
const canonicalUpdatedAtGlob = `updated_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]'`

// formatUpdatedAt renders t in the canonical updated_at format.
func formatUpdatedAt(t time.Time) string {
	return t.UTC().Format(updatedAtLayout)
}

// FormatUpdatedAt renders t in the canonical sessions.updated_at format, for
// other packages that compare against the column (maintenance cleanup).
// Binding a time.Time instead compares against the driver's text rendering,
// which does not sort with the canonical layout.
func FormatUpdatedAt(t time.Time) string { return formatUpdatedAt(t) }

// nowUpdatedAt returns the current time in the canonical updated_at format.
func nowUpdatedAt() string { return formatUpdatedAt(time.Now()) }

// legacyTimeLayouts are the formats legacy rows may hold in updated_at.
var legacyTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST", // Go time.Time.String() (m=+ stripped)
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999", // CURRENT_TIMESTAMP (UTC) and friends
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseLegacyTime parses a legacy updated_at string. Zone-less forms are
// UTC (that is what SQLite's CURRENT_TIMESTAMP produces).
func parseLegacyTime(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, " m="); i > 0 {
		v = v[:i]
	}
	for _, layout := range legacyTimeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// normalizeUpdatedAt rewrites every sessions.updated_at value that is not
// already canonical into updatedAtLayout (UTC). It is idempotent: once all
// rows are canonical it only performs a read. Unparseable values are left
// untouched. Returns the number of rows rewritten. conduit-31jg.24.
func (s *Store) normalizeUpdatedAt() (int, error) {
	rows, err := s.db.Query(`SELECT rowid, CAST(updated_at AS TEXT) FROM sessions
		WHERE updated_at IS NOT NULL AND typeof(updated_at) = 'text' AND NOT (` + canonicalUpdatedAtGlob + `)`)
	if err != nil {
		return 0, fmt.Errorf("scan legacy updated_at: %w", err)
	}
	type fix struct {
		rowid int64
		val   string
	}
	var fixes []fix
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan legacy updated_at: %w", err)
		}
		if t, ok := parseLegacyTime(v); ok {
			fixes = append(fixes, fix{id, formatUpdatedAt(t)})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(fixes) == 0 {
		return 0, nil
	}

	// Batched transactions keep each write-lock hold short on a large live DB.
	const batch = 5000
	for start := 0; start < len(fixes); start += batch {
		end := min(start+batch, len(fixes))
		err := database.RetryOnBusy(5, func() error {
			tx, err := s.db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback() //nolint:errcheck // no-op after Commit
			stmt, err := tx.Prepare(`UPDATE sessions SET updated_at = ? WHERE rowid = ?`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, f := range fixes[start:end] {
				if _, err := stmt.Exec(f.val, f.rowid); err != nil {
					return err
				}
			}
			return tx.Commit()
		})
		if err != nil {
			return start, fmt.Errorf("normalize updated_at: %w", err)
		}
	}
	return len(fixes), nil
}

// sessionNotFoundError keeps the historical error text while letting
// callers test errors.Is(err, sql.ErrNoRows). conduit-31jg.24.
type sessionNotFoundError struct{ msg string }

func (e *sessionNotFoundError) Error() string { return e.msg }
func (e *sessionNotFoundError) Unwrap() error { return sql.ErrNoRows }

// NewStore creates a new session store
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	store := &Store{
		db:           db,
		stateTracker: NewSessionStateTracker(),
	}

	// Configure database and run migrations
	if err := database.ConfigureDatabase(db); err != nil {
		return nil, fmt.Errorf("failed to configure database: %w", err)
	}

	// conduit-31jg.24: bring legacy mixed-format updated_at values into the
	// canonical format so ORDER BY updated_at is chronological. Best-effort:
	// a failure here must not stop the gateway from starting.
	if n, err := store.normalizeUpdatedAt(); err != nil {
		log.Printf("[sessions] WARNING: updated_at normalization incomplete (%d rows fixed): %v", n, err)
	} else if n > 0 {
		log.Printf("[sessions] normalized %d legacy updated_at values", n)
	}

	return store, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection for shared use (e.g., auth tokens)
func (s *Store) DB() *sql.DB {
	return s.db
}

// SetMessageCallbacks sets callbacks for message synchronization with search.db.
// The added callback is invoked after each message is added to the store.
// The cleared callback is invoked when a session's messages are cleared.
func (s *Store) SetMessageCallbacks(added MessageAddedCallback, cleared SessionClearedCallback) {
	s.onMessageAdded = added
	s.onSessionCleared = cleared
}

// SetMessagesDeletedCallback sets the callback invoked after ApplyCompaction
// removes specific messages, so search.db can drop them from its index
// (conduit-31jg.21).
func (s *Store) SetMessagesDeletedCallback(cb MessagesDeletedCallback) {
	s.onMessagesDeleted = cb
}

// Legacy createTables method - replaced by database migrations
// This method is kept for reference but is no longer used
func (s *Store) createTablesLegacy() error {
	// Create sessions table
	sessionsSQL := `
	CREATE TABLE IF NOT EXISTS sessions (
		key TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		message_count INTEGER DEFAULT 0,
		context TEXT DEFAULT '{}'
	);`

	if _, err := s.db.Exec(sessionsSQL); err != nil {
		return fmt.Errorf("failed to create sessions table: %w", err)
	}

	// Create messages table
	messagesSQL := `
	CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		session_key TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		metadata TEXT DEFAULT '{}',
		FOREIGN KEY (session_key) REFERENCES sessions (key)
	);`

	if _, err := s.db.Exec(messagesSQL); err != nil {
		return fmt.Errorf("failed to create messages table: %w", err)
	}

	// Create indexes
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_sessions_user_channel ON sessions (user_id, channel_id);",
		"CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions (updated_at);",
		"CREATE INDEX IF NOT EXISTS idx_messages_session_key ON messages (session_key);",
		"CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages (timestamp);",
	}

	for _, indexSQL := range indexes {
		if _, err := s.db.Exec(indexSQL); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

// GetOrCreateSession retrieves an existing session or creates a new one
func (s *Store) GetOrCreateSession(userID, channelID string) (*Session, error) {
	// Try to find existing session
	session, err := s.GetLatestSession(userID, channelID)
	if err == nil {
		return session, nil
	}
	// conduit-31jg.24: only a genuine "no rows" means create. Any other
	// error (SQLITE_BUSY, scan failure, ...) must surface; creating a fresh
	// session would silently drop the user's conversation continuity.
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Create new session if not found
	sessionKey := fmt.Sprintf("%s_%s_%s", channelID, userID, uuid.New().String()[:8])

	session = &Session{
		Key:          sessionKey,
		UserID:       userID,
		ChannelID:    channelID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		MessageCount: 0,
		Context:      make(map[string]string),
	}

	if err := s.SaveSession(session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// Initialize session state tracking
	s.stateTracker.UpdateState(session.Key, SessionStateIdle, map[string]interface{}{
		"action":     "session_created",
		"user_id":    session.UserID,
		"channel_id": session.ChannelID,
	})

	return session, nil
}

// GetSession retrieves a session by key
func (s *Store) GetSession(key string) (*Session, error) {
	var session Session
	var contextJSON string

	row := s.db.QueryRow(`
		SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
		FROM sessions WHERE key = ?
	`, key)

	err := row.Scan(
		&session.Key,
		&session.UserID,
		&session.ChannelID,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.MessageCount,
		&contextJSON,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("session not found: %s", key)
		}
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// Parse context JSON
	if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
		session.Context = make(map[string]string)
	}

	// Add current state information from tracker
	if stateInfo, exists := s.stateTracker.GetStateInfo(session.Key); exists {
		session.State = stateInfo.State
		session.LastActivity = stateInfo.LastActivity
		session.StateChanged = stateInfo.StateChanged
	} else {
		// Initialize tracking for existing session
		session.State = SessionStateIdle
		session.LastActivity = session.UpdatedAt
		session.StateChanged = session.UpdatedAt
		s.stateTracker.UpdateState(session.Key, SessionStateIdle, map[string]interface{}{
			"action": "session_loaded",
		})
	}

	return &session, nil
}

// GetLatestSession retrieves the most recent session for a user/channel
func (s *Store) GetLatestSession(userID, channelID string) (*Session, error) {
	var session Session
	var contextJSON string

	err := database.RetryOnBusy(5, func() error {
		return s.db.QueryRow(`
			SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
			FROM sessions
			WHERE user_id = ? AND channel_id = ?
			ORDER BY updated_at DESC
			LIMIT 1
		`, userID, channelID).Scan(
			&session.Key,
			&session.UserID,
			&session.ChannelID,
			&session.CreatedAt,
			&session.UpdatedAt,
			&session.MessageCount,
			&contextJSON,
		)
	})

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// conduit-31jg.24: wraps sql.ErrNoRows so GetOrCreateSession can
			// distinguish not-found from real DB errors.
			return nil, &sessionNotFoundError{fmt.Sprintf("no session found for user %s in channel %s", userID, channelID)}
		}
		return nil, fmt.Errorf("failed to get latest session: %w", err)
	}

	// Parse context JSON
	if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
		session.Context = make(map[string]string)
	}

	// Add current state information from tracker
	if stateInfo, exists := s.stateTracker.GetStateInfo(session.Key); exists {
		session.State = stateInfo.State
		session.LastActivity = stateInfo.LastActivity
		session.StateChanged = stateInfo.StateChanged
	} else {
		// Initialize tracking for existing session
		session.State = SessionStateIdle
		session.LastActivity = session.UpdatedAt
		session.StateChanged = session.UpdatedAt
		s.stateTracker.UpdateState(session.Key, SessionStateIdle, map[string]interface{}{
			"action": "session_loaded",
		})
	}

	return &session, nil
}

// SaveSession saves a session to the database
func (s *Store) SaveSession(session *Session) error {
	contextJSON, err := json.Marshal(session.Context)
	if err != nil {
		return fmt.Errorf("failed to marshal context: %w", err)
	}

	err = database.RetryOnBusy(5, func() error {
		_, execErr := s.db.Exec(`
			INSERT OR REPLACE INTO sessions
			(key, user_id, channel_id, created_at, updated_at, message_count, context)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
			session.Key,
			session.UserID,
			session.ChannelID,
			session.CreatedAt,
			nowUpdatedAt(), // conduit-31jg.24: canonical format
			session.MessageCount,
			string(contextJSON),
		)
		return execErr
	})

	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	return nil
}

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
			message.Timestamp,
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

// SetSessionContext updates a key in the session's context.
// Uses json_set to perform an atomic read-modify-write in a single SQL statement,
// preventing concurrent calls from losing each other's updates.
func (s *Store) SetSessionContext(sessionKey, key, value string) error {
	var result sql.Result
	err := database.RetryOnBusy(5, func() error {
		var execErr error
		result, execErr = s.db.Exec(`
			UPDATE sessions
			SET context = json_set(context, '$.' || ?, ?),
			    updated_at = ?
			WHERE key = ?
		`, key, value, nowUpdatedAt(), sessionKey)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("failed to update session context: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionKey)
	}

	return nil
}

// SetSessionContextBatch updates multiple keys in the session's context in a single statement.
// This reduces write lock acquisitions from N to 1 compared to N individual SetSessionContext calls.
func (s *Store) SetSessionContextBatch(sessionKey string, kvPairs map[string]string) error {
	if len(kvPairs) == 0 {
		return nil
	}

	// Build nested json_set: json_set(json_set(context, '$.k1', ?), '$.k2', ?)
	expr := "context"
	args := make([]interface{}, 0, len(kvPairs)*2+1)
	for key, value := range kvPairs {
		expr = fmt.Sprintf("json_set(%s, '$.'||?, ?)", expr)
		args = append(args, key, value)
	}
	args = append(args, nowUpdatedAt(), sessionKey)

	query := fmt.Sprintf(`
		UPDATE sessions
		SET context = %s,
		    updated_at = ?
		WHERE key = ?
	`, expr)

	var result sql.Result
	err := database.RetryOnBusy(5, func() error {
		var execErr error
		result, execErr = s.db.Exec(query, args...)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("failed to batch update session context: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("session not found: %s", sessionKey)
	}

	return nil
}

// GetSessionContext gets a value from the session's context
func (s *Store) GetSessionContext(sessionKey, key string) (string, error) {
	session, err := s.GetSession(sessionKey)
	if err != nil {
		return "", err
	}
	return session.Context[key], nil
}

// Context keys written by RecordTokenUsage. These power SessionStatus's
// context-budget gauge (last_* snapshot keys) and cumulative session totals.
// Declared here so the sessions package is the single source of truth for the
// key names; the gateway package mirrors them via exported constants.
const (
	CtxKeyLastPromptTokens         = "last_prompt_tokens"
	CtxKeyLastCompletionTokens     = "last_completion_tokens"
	CtxKeyLastTotalTokens          = "last_total_tokens"
	CtxKeySessionPromptTokensTotal = "session_prompt_tokens_total"
	CtxKeySessionCompletionTokens  = "session_completion_tokens_total"
	CtxKeyContextBudgetUpdatedAt   = "context_budget_updated_at"
)

// RecordTokenUsage persists a token-usage snapshot to the session's context:
// the last_* keys reflect the most recent generation, the session_*_tokens_total
// keys accumulate across generations, and context_budget_updated_at is an
// RFC3339 timestamp. It reads the current cumulative totals from the store
// (single round trip) and merges everything in one batched context write.
//
// This is the router-level accounting path (bd-27hs): every generation entry
// point records through here, so usage is uniform across channel, direct,
// cron, wake, sub-agent, WS and HTTP callers. Zero-token usage is a no-op.
// Best-effort: errors are returned for the caller to log/ignore.
func (s *Store) RecordTokenUsage(sessionKey string, promptTokens, completionTokens, totalTokens int) error {
	if totalTokens == 0 {
		totalTokens = promptTokens + completionTokens
	}
	return s.RecordTurnUsage(sessionKey, TurnUsage{
		ContextTokens:    promptTokens,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	})
}

// Cache token context keys written by RecordTurnUsage (conduit-31jg.15).
const (
	CtxKeyLastCacheCreationTokens     = "last_cache_creation_tokens"
	CtxKeyLastCacheReadTokens         = "last_cache_read_tokens"
	CtxKeySessionCacheCreationTotal   = "session_cache_creation_tokens_total"
	CtxKeySessionCacheReadTokensTotal = "session_cache_read_tokens_total"
)

// TurnUsage is one turn's token accounting as recorded by RecordTurnUsage.
// ContextTokens is the context-window occupancy (the prompt size of the
// turn's last round trip, cached input included — ai.Usage.Context()); the
// other fields are whole-turn sums across every billed round trip.
type TurnUsage struct {
	ContextTokens            int
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	CacheCreationInputTokens int
	CacheReadInputTokens     int
}

// RecordTurnUsage persists a turn's usage (conduit-31jg.15). The
// last_prompt_tokens / last_total_tokens keys drive the context-budget gauge
// and /context, so they hold context occupancy (ContextTokens), not the
// turn-wide prompt sum, which overstates occupancy on multi-round tool
// turns. last_total_tokens is ContextTokens + the turn's completion tokens.
// The session_*_total keys accumulate the billed sums, and the cache token
// fields are recorded alongside (last_* = this turn's sums).
func (s *Store) RecordTurnUsage(sessionKey string, u TurnUsage) error {
	if sessionKey == "" {
		return nil
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.ContextTokens == 0 &&
		u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		return nil
	}
	if u.ContextTokens == 0 {
		u.ContextTokens = u.PromptTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	}

	// Read current context to pick up the running cumulative totals.
	session, err := s.GetSession(sessionKey)
	if err != nil {
		return err
	}
	prevPrompt := atoiContext(session.Context, CtxKeySessionPromptTokensTotal)
	prevCompletion := atoiContext(session.Context, CtxKeySessionCompletionTokens)
	prevCacheCreate := atoiContext(session.Context, CtxKeySessionCacheCreationTotal)
	prevCacheRead := atoiContext(session.Context, CtxKeySessionCacheReadTokensTotal)

	return s.SetSessionContextBatch(sessionKey, map[string]string{
		CtxKeyLastPromptTokens:            strconv.Itoa(u.ContextTokens),
		CtxKeyLastCompletionTokens:        strconv.Itoa(u.CompletionTokens),
		CtxKeyLastTotalTokens:             strconv.Itoa(u.ContextTokens + u.CompletionTokens),
		CtxKeyLastCacheCreationTokens:     strconv.Itoa(u.CacheCreationInputTokens),
		CtxKeyLastCacheReadTokens:         strconv.Itoa(u.CacheReadInputTokens),
		CtxKeySessionPromptTokensTotal:    strconv.Itoa(prevPrompt + u.PromptTokens),
		CtxKeySessionCompletionTokens:     strconv.Itoa(prevCompletion + u.CompletionTokens),
		CtxKeySessionCacheCreationTotal:   strconv.Itoa(prevCacheCreate + u.CacheCreationInputTokens),
		CtxKeySessionCacheReadTokensTotal: strconv.Itoa(prevCacheRead + u.CacheReadInputTokens),
		CtxKeyContextBudgetUpdatedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// atoiContext parses a context value as an int, returning 0 on absence/error.
func atoiContext(ctx map[string]string, key string) int {
	v, err := strconv.Atoi(ctx[key])
	if err != nil {
		return 0
	}
	return v
}

// Session State Tracking Methods

// GetStateTracker returns the session state tracker
func (s *Store) GetStateTracker() *SessionStateTracker {
	return s.stateTracker
}

// StartStateCleanup starts the background cleanup loop for idle session states.
// Returns a function to stop the cleanup loop. Should be called on shutdown.
func (s *Store) StartStateCleanup(idleTimeout, interval time.Duration) func() {
	return s.stateTracker.StartCleanupLoop(idleTimeout, interval)
}

// UpdateSessionState updates the state of a session with optional metadata
func (s *Store) UpdateSessionState(sessionKey string, state SessionState, metadata map[string]interface{}) error {
	if err := s.stateTracker.UpdateState(sessionKey, state, metadata); err != nil {
		return fmt.Errorf("failed to update session state: %w", err)
	}

	// Mark activity in the database record as well
	s.markSessionActivity(sessionKey)

	return nil
}

// MarkSessionActivity marks that activity occurred for a session
func (s *Store) MarkSessionActivity(sessionKey string) {
	s.stateTracker.MarkActivity(sessionKey)
	s.markSessionActivity(sessionKey)
}

// GetSessionState returns the current state of a session
func (s *Store) GetSessionState(sessionKey string) (SessionState, bool) {
	return s.stateTracker.GetState(sessionKey)
}

// GetSessionStateInfo returns detailed state information for a session
func (s *Store) GetSessionStateInfo(sessionKey string) (*SessionStateInfo, bool) {
	return s.stateTracker.GetStateInfo(sessionKey)
}

// GetSessionStateMetrics returns current session state metrics
func (s *Store) GetSessionStateMetrics() SessionStateMetrics {
	return s.stateTracker.GetMetrics()
}

// UpdateQueueDepth updates the queue depth metric
func (s *Store) UpdateQueueDepth(depth int) {
	s.stateTracker.UpdateQueueDepth(depth)
}

// DetectStuckSessions finds sessions that have been stuck for too long
func (s *Store) DetectStuckSessions(config StuckSessionConfig) []StuckSessionInfo {
	return s.stateTracker.DetectStuckSessions(config)
}

// AddStateChangeHook adds a hook that will be called on state changes
func (s *Store) AddStateChangeHook(hook StateChangeHook) {
	s.stateTracker.AddStateHook(hook)
}

// markSessionActivity is an internal helper to update the database last activity
func (s *Store) markSessionActivity(sessionKey string) {
	// Update the database record's updated_at timestamp
	// Best-effort — retry on BUSY so heartbeat-paced writers don't silently drop updates.
	_ = database.RetryOnBusy(5, func() error {
		_, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE key = ?`, nowUpdatedAt(), sessionKey)
		return err
	})
}

// SearchMessagesResult represents a search result from session messages
type SearchMessagesResult struct {
	Message    Message `json:"message"`
	SessionKey string  `json:"session_key"`
	MatchScore float64 `json:"match_score"`
}

// SearchMessages searches for messages across all sessions containing the query keywords.
// Uses FTS5 full-text search for efficient querying with BM25 ranking.
func (s *Store) SearchMessages(query string, limit int) ([]SearchMessagesResult, error) {
	if limit <= 0 {
		limit = 50
	}

	// Build FTS5 query: escape special characters and join with OR for flexible matching
	ftsQuery := s.buildFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	// Try FTS5 search first (uses messages_fts virtual table with BM25 ranking)
	rows, err := s.db.Query(`
		SELECT m.id, m.session_key, m.role, m.content, m.timestamp, m.metadata, s.key, fts.rank
		FROM messages_fts fts
		JOIN messages m ON fts.message_id = m.id
		JOIN sessions s ON m.session_key = s.key
		WHERE messages_fts MATCH ?
		ORDER BY fts.rank
		LIMIT ?
	`, ftsquery.Column("content", ftsQuery), limit) // conduit-31jg.31: scope every OR'd phrase

	if err != nil {
		// Fall back to LIKE search if FTS fails (e.g., table doesn't exist)
		return s.searchMessagesLIKE(query, limit)
	}
	defer rows.Close()

	var results []SearchMessagesResult

	for rows.Next() {
		var message Message
		var metadataJSON string
		var sessionKey string
		var rank float64

		err := rows.Scan(
			&message.ID,
			&message.SessionKey,
			&message.Role,
			&message.Content,
			&message.Timestamp,
			&metadataJSON,
			&sessionKey,
			&rank,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Parse metadata JSON
		if err := json.Unmarshal([]byte(metadataJSON), &message.Metadata); err != nil {
			message.Metadata = make(map[string]string)
		}

		// Convert BM25 rank to 0-1 score (rank is negative, more negative = better match)
		score := -rank / 20.0
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}

		results = append(results, SearchMessagesResult{
			Message:    message,
			SessionKey: sessionKey,
			MatchScore: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// buildFTSQuery converts a user query into FTS5 query syntax.
// Escapes special characters and handles multi-word queries.
func (s *Store) buildFTSQuery(query string) string {
	// conduit-31jg.31: shared quoted-phrase builder (the old partial escaper
	// left . / @ % # : unquoted -> "fts5: syntax error" -> silent LIKE fallback).
	return ftsquery.Build(query)
}

// likeEscaper makes %, _ and the escape char literal in a LIKE ... ESCAPE '\'
// pattern. conduit-31jg.73.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// searchMessagesLIKE is a fallback search using LIKE when FTS5 is unavailable.
func (s *Store) searchMessagesLIKE(query string, limit int) ([]SearchMessagesResult, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.session_key, m.role, m.content, m.timestamp, m.metadata, s.key
		FROM messages m
		JOIN sessions s ON m.session_key = s.key
		WHERE LOWER(m.content) LIKE LOWER(?) ESCAPE '\'
		ORDER BY m.timestamp DESC, m.rowid DESC
		LIMIT ?
	`, "%"+likeEscaper.Replace(query)+"%", limit)

	if err != nil {
		return nil, fmt.Errorf("failed to search messages: %w", err)
	}
	defer rows.Close()

	var results []SearchMessagesResult

	for rows.Next() {
		var message Message
		var metadataJSON string
		var sessionKey string

		err := rows.Scan(
			&message.ID,
			&message.SessionKey,
			&message.Role,
			&message.Content,
			&message.Timestamp,
			&metadataJSON,
			&sessionKey,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Parse metadata JSON
		if err := json.Unmarshal([]byte(metadataJSON), &message.Metadata); err != nil {
			message.Metadata = make(map[string]string)
		}

		// Calculate basic match score based on keyword frequency
		score := s.calculateMessageMatchScore(message.Content, query)

		results = append(results, SearchMessagesResult{
			Message:    message,
			SessionKey: sessionKey,
			MatchScore: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return results, nil
}

// calculateMessageMatchScore calculates a simple match score for message content
func (s *Store) calculateMessageMatchScore(content, query string) float64 {
	if query == "" {
		return 0.0
	}

	contentLower := strings.ToLower(content)
	queryLower := strings.ToLower(query)

	// Split query into keywords
	keywords := strings.Fields(queryLower)
	if len(keywords) == 0 {
		return 0.0
	}

	matches := 0
	for _, keyword := range keywords {
		if strings.Contains(contentLower, keyword) {
			matches++
		}
	}

	return float64(matches) / float64(len(keywords))
}

// GetSessionsByUser returns all sessions for a given user across all channels
func (s *Store) GetSessionsByUser(userID string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
		FROM sessions
		WHERE user_id = ?
		ORDER BY updated_at DESC
		LIMIT ?
	`, userID, limit)

	if err != nil {
		return nil, fmt.Errorf("failed to query sessions by user: %w", err)
	}
	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		var session Session
		var contextJSON string

		err := rows.Scan(
			&session.Key,
			&session.UserID,
			&session.ChannelID,
			&session.CreatedAt,
			&session.UpdatedAt,
			&session.MessageCount,
			&contextJSON,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
			session.Context = make(map[string]string)
		}

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return sessions, nil
}

// GetIdleSessions returns keys of sessions updated before olderThan with more than minMessages.
func (s *Store) GetIdleSessions(olderThan time.Time, minMessages int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT key FROM sessions
		WHERE updated_at < ? AND message_count > ?
	`, formatUpdatedAt(olderThan), minMessages)
	if err != nil {
		return nil, fmt.Errorf("query idle sessions: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan idle session key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// GetSessionByLabel retrieves a session by its label from the context JSON.
// Labels are stored as context["label"] on sessions.
func (s *Store) GetSessionByLabel(label string) (*Session, error) {
	if label == "" {
		return nil, fmt.Errorf("label cannot be empty")
	}

	rows, err := s.db.Query(`
		SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
		FROM sessions
		WHERE json_extract(context, '$.label') = ?
		ORDER BY updated_at DESC
		LIMIT 1
	`, label)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions by label: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, fmt.Errorf("no session found with label: %s", label)
	}

	var session Session
	var contextJSON string
	err = rows.Scan(
		&session.Key,
		&session.UserID,
		&session.ChannelID,
		&session.CreatedAt,
		&session.UpdatedAt,
		&session.MessageCount,
		&contextJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan session: %w", err)
	}

	if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
		session.Context = make(map[string]string)
	}

	return &session, nil
}

// ListActiveSessions returns a list of recently active sessions
func (s *Store) ListActiveSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
		FROM sessions
		ORDER BY updated_at DESC
		LIMIT ?
	`, limit)

	if err != nil {
		return nil, fmt.Errorf("failed to query active sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		var session Session
		var contextJSON string

		err := rows.Scan(
			&session.Key,
			&session.UserID,
			&session.ChannelID,
			&session.CreatedAt,
			&session.UpdatedAt,
			&session.MessageCount,
			&contextJSON,
		)

		if err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		// Parse context JSON
		if err := json.Unmarshal([]byte(contextJSON), &session.Context); err != nil {
			session.Context = make(map[string]string)
		}

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return sessions, nil
}
