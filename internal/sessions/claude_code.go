package sessions

import (
	"database/sql"
	"fmt"
	"time"
)

// ClaudeCodeSessionMapper maps Conduit sessions to Claude Code session IDs
// so that `claude -p --resume <cc_session_id>` can maintain conversation continuity.
type ClaudeCodeSessionMapper struct {
	db *sql.DB
}

// NewClaudeCodeSessionMapper creates a new mapper using the given database.
func NewClaudeCodeSessionMapper(db *sql.DB) *ClaudeCodeSessionMapper {
	return &ClaudeCodeSessionMapper{db: db}
}

// EnsureTable creates the claude_code_sessions table if it doesn't exist.
// Call on startup before using other methods.
func (m *ClaudeCodeSessionMapper) EnsureTable() error {
	_, err := m.db.Exec(`
		CREATE TABLE IF NOT EXISTS claude_code_sessions (
			conduit_session_id TEXT PRIMARY KEY,
			cc_session_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_used_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create claude_code_sessions table: %w", err)
	}
	return nil
}

// GetClaudeCodeSession returns the Claude Code session ID for a Conduit session.
// Returns ("", nil) if no mapping exists.
func (m *ClaudeCodeSessionMapper) GetClaudeCodeSession(conduitSessionID string) (string, error) {
	var ccSessionID string
	err := m.db.QueryRow(`
		SELECT cc_session_id FROM claude_code_sessions
		WHERE conduit_session_id = ?
	`, conduitSessionID).Scan(&ccSessionID)

	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("failed to get claude code session: %w", err)
	}
	return ccSessionID, nil
}

// SaveMapping stores a new mapping or updates an existing one (upsert semantics).
func (m *ClaudeCodeSessionMapper) SaveMapping(conduitSessionID, ccSessionID string) error {
	_, err := m.db.Exec(`
		INSERT OR REPLACE INTO claude_code_sessions
		(conduit_session_id, cc_session_id, created_at, last_used_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, conduitSessionID, ccSessionID)
	if err != nil {
		return fmt.Errorf("failed to save claude code session mapping: %w", err)
	}
	return nil
}

// UpdateLastUsed updates the last_used_at timestamp for the given Conduit session.
func (m *ClaudeCodeSessionMapper) UpdateLastUsed(conduitSessionID string) error {
	_, err := m.db.Exec(`
		UPDATE claude_code_sessions
		SET last_used_at = CURRENT_TIMESTAMP
		WHERE conduit_session_id = ?
	`, conduitSessionID)
	if err != nil {
		return fmt.Errorf("failed to update last used timestamp: %w", err)
	}
	return nil
}

// DeleteMapping removes a mapping, e.g. when a Claude Code session can't be resumed.
func (m *ClaudeCodeSessionMapper) DeleteMapping(conduitSessionID string) error {
	_, err := m.db.Exec(`
		DELETE FROM claude_code_sessions
		WHERE conduit_session_id = ?
	`, conduitSessionID)
	if err != nil {
		return fmt.Errorf("failed to delete claude code session mapping: %w", err)
	}
	return nil
}

// CleanupOld removes mappings whose last_used_at is older than the given duration.
// Returns the number of rows deleted.
func (m *ClaudeCodeSessionMapper) CleanupOld(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := m.db.Exec(`
		DELETE FROM claude_code_sessions
		WHERE last_used_at < ?
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup old claude code session mappings: %w", err)
	}
	return result.RowsAffected()
}
