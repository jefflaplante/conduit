package sessions

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"conduit/internal/database"
	"github.com/google/uuid"
)

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
			formatStoredTime(session.CreatedAt), // conduit-a636: canonical format
			nowUpdatedAt(),                      // conduit-31jg.24: canonical format
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
