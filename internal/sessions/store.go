package sessions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"conduit/internal/database"
)

// MessageAddedCallback is called when a message is added to a session.
// Parameters: id, sessionKey, role, content
type MessageAddedCallback func(id, sessionKey, role, content string)

// SessionClearedCallback is called when a session's messages are cleared.
// Parameters: sessionKey
type SessionClearedCallback func(sessionKey string)

// Store manages conversation sessions
type Store struct {
	db           *sql.DB
	stateTracker *SessionStateTracker

	// Callbacks for search index synchronization
	onMessageAdded   MessageAddedCallback
	onSessionCleared SessionClearedCallback
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

// NewStore creates a new session store
func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
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

	row := s.db.QueryRow(`
		SELECT key, user_id, channel_id, created_at, updated_at, message_count, context
		FROM sessions 
		WHERE user_id = ? AND channel_id = ?
		ORDER BY updated_at DESC
		LIMIT 1
	`, userID, channelID)

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
			return nil, fmt.Errorf("no session found for user %s in channel %s", userID, channelID)
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

	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO sessions 
		(key, user_id, channel_id, created_at, updated_at, message_count, context)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		session.Key,
		session.UserID,
		session.ChannelID,
		session.CreatedAt,
		time.Now(),
		session.MessageCount,
		string(contextJSON),
	)

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

	_, err = s.db.Exec(`
		INSERT INTO messages (id, session_key, role, content, timestamp, metadata)
		VALUES (?, ?, ?, ?, ?, ?)
	`,
		message.ID,
		message.SessionKey,
		message.Role,
		message.Content,
		message.Timestamp,
		string(metadataJSON),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to save message: %w", err)
	}

	// Sync to search.db FTS5 index via callback (best-effort — don't fail the message insert)
	if s.onMessageAdded != nil {
		s.onMessageAdded(message.ID, message.SessionKey, message.Role, message.Content)
	}

	// Update session message count
	if err := s.updateSessionMessageCount(sessionKey); err != nil {
		return nil, fmt.Errorf("failed to update session message count: %w", err)
	}

	// Mark session activity
	s.stateTracker.MarkActivity(sessionKey)

	return message, nil
}

// GetMessages retrieves messages for a session
func (s *Store) GetMessages(sessionKey string, limit int) ([]Message, error) {
	// Use subquery to get most recent N messages, then order chronologically
	// Without this, LIMIT + ASC gives oldest messages, not newest
	var query string
	if limit > 0 {
		query = fmt.Sprintf(`
			SELECT id, session_key, role, content, timestamp, metadata
			FROM (
				SELECT id, session_key, role, content, timestamp, metadata
				FROM messages
				WHERE session_key = ?
				ORDER BY timestamp DESC
				LIMIT %d
			) sub
			ORDER BY timestamp ASC
		`, limit)
	} else {
		query = `
			SELECT id, session_key, role, content, timestamp, metadata
			FROM messages
			WHERE session_key = ?
			ORDER BY timestamp ASC
		`
	}

	rows, err := s.db.Query(query, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []Message

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

// updateSessionMessageCount updates the message count for a session
func (s *Store) updateSessionMessageCount(sessionKey string) error {
	_, err := s.db.Exec(`
		UPDATE sessions 
		SET message_count = (
			SELECT COUNT(*) FROM messages WHERE session_key = ?
		),
		updated_at = CURRENT_TIMESTAMP
		WHERE key = ?
	`, sessionKey, sessionKey)

	if err != nil {
		return fmt.Errorf("failed to update session message count: %w", err)
	}

	return nil
}

// ClearSessionMessages deletes all messages for a session (keeps the session record)
func (s *Store) ClearSessionMessages(sessionKey string) error {
	// Clear search.db FTS5 index via callback (best-effort)
	if s.onSessionCleared != nil {
		s.onSessionCleared(sessionKey)
	}

	// Delete all messages for the session
	_, err := s.db.Exec(`DELETE FROM messages WHERE session_key = ?`, sessionKey)
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}

	// Update the session's message count to 0
	_, err = s.db.Exec(`
		UPDATE sessions 
		SET message_count = 0, updated_at = CURRENT_TIMESTAMP 
		WHERE key = ?
	`, sessionKey)
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}

// SetSessionContext updates a key in the session's context
func (s *Store) SetSessionContext(sessionKey, key, value string) error {
	// Get current session
	session, err := s.GetSession(sessionKey)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// Update context
	if session.Context == nil {
		session.Context = make(map[string]string)
	}
	session.Context[key] = value

	// Marshal and save
	contextJSON, err := json.Marshal(session.Context)
	if err != nil {
		return fmt.Errorf("failed to marshal context: %w", err)
	}

	_, err = s.db.Exec(`
		UPDATE sessions 
		SET context = ?, updated_at = CURRENT_TIMESTAMP 
		WHERE key = ?
	`, string(contextJSON), sessionKey)
	if err != nil {
		return fmt.Errorf("failed to update session context: %w", err)
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

// Session State Tracking Methods

// GetStateTracker returns the session state tracker
func (s *Store) GetStateTracker() *SessionStateTracker {
	return s.stateTracker
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
	// This is best-effort and doesn't return an error to avoid disrupting normal flow
	s.db.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE key = ?`, sessionKey)
}

// SearchMessagesResult represents a search result from session messages
type SearchMessagesResult struct {
	Message    Message `json:"message"`
	SessionKey string  `json:"session_key"`
	MatchScore float64 `json:"match_score"`
}

// SearchMessages searches for messages across all sessions containing the query keywords
func (s *Store) SearchMessages(query string, limit int) ([]SearchMessagesResult, error) {
	if limit <= 0 {
		limit = 50
	}

	// Use FTS if available, otherwise fall back to LIKE search
	// For simplicity, using LIKE search which works without FTS setup
	rows, err := s.db.Query(`
		SELECT m.id, m.session_key, m.role, m.content, m.timestamp, m.metadata, s.key
		FROM messages m
		JOIN sessions s ON m.session_key = s.key
		WHERE LOWER(m.content) LIKE LOWER(?)
		ORDER BY m.timestamp DESC
		LIMIT ?
	`, "%"+query+"%", limit)

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
