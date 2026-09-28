package sessions

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"conduit/internal/database"
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
//
//lint:ignore U1000 kept pending salvage review (conduit-31jg.72); see staticcheck.conf
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
