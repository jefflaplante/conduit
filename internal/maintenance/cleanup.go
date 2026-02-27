package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// SessionCleanupTask handles cleanup of old session and message records
type SessionCleanupTask struct {
	db     *sql.DB
	config SessionConfig
	logger *log.Logger
}

// NewSessionCleanupTask creates a new session cleanup task
func NewSessionCleanupTask(db *sql.DB, config SessionConfig, logger *log.Logger) *SessionCleanupTask {
	if logger == nil {
		logger = log.Default()
	}

	return &SessionCleanupTask{
		db:     db,
		config: config,
		logger: logger,
	}
}

// Name returns the task name
func (t *SessionCleanupTask) Name() string {
	return "session_cleanup"
}

// Description returns the task description
func (t *SessionCleanupTask) Description() string {
	return fmt.Sprintf("Clean up sessions and messages older than %d days", t.config.RetentionDays)
}

// Execute runs the session cleanup task
func (t *SessionCleanupTask) Execute(ctx context.Context) TaskResult {
	if !t.config.CleanupEnabled {
		return TaskResult{
			Success: true,
			Message: "Session cleanup disabled in configuration",
		}
	}

	start := time.Now()
	result := TaskResult{Success: true}

	// Calculate cutoff date
	cutoff := time.Now().AddDate(0, 0, -t.config.RetentionDays)

	// Start transaction
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to start transaction",
			Error:   err,
		}
	}
	defer tx.Rollback()

	// First, handle summarization if enabled
	if t.config.SummarizeOld {
		summaryResult := t.summarizeSessions(ctx, tx, cutoff)
		if !summaryResult.Success {
			return summaryResult
		}
		result.RecordsProcessed += summaryResult.RecordsProcessed
	}

	// Clean up old messages first (due to foreign key constraints)
	messagesResult := t.cleanupMessages(ctx, tx, cutoff)
	if !messagesResult.Success {
		return messagesResult
	}
	result.RecordsProcessed += messagesResult.RecordsProcessed

	// Clean up old sessions
	sessionsResult := t.cleanupSessions(ctx, tx, cutoff)
	if !sessionsResult.Success {
		return sessionsResult
	}
	result.RecordsProcessed += sessionsResult.RecordsProcessed

	// Clean up old session summaries if configured
	if t.config.SummaryRetentionDays > 0 {
		summaryCleanupResult := t.cleanupSessionSummaries(ctx, tx)
		if !summaryCleanupResult.Success {
			return summaryCleanupResult
		}
		result.RecordsProcessed += summaryCleanupResult.RecordsProcessed
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to commit cleanup transaction",
			Error:   err,
		}
	}

	result.Duration = time.Since(start)
	result.Message = fmt.Sprintf("Cleaned up %d records", result.RecordsProcessed)

	return result
}

// ShouldRun determines if the task should run (always true for scheduled tasks)
func (t *SessionCleanupTask) ShouldRun() bool {
	return t.config.CleanupEnabled
}

// NextRun returns when the task should run next (handled by scheduler)
func (t *SessionCleanupTask) NextRun() time.Time {
	return time.Now().Add(24 * time.Hour)
}

// IsDestructive returns true since this task deletes data
func (t *SessionCleanupTask) IsDestructive() bool {
	return true
}

// cleanupMessages removes messages older than the cutoff date
func (t *SessionCleanupTask) cleanupMessages(ctx context.Context, tx *sql.Tx, cutoff time.Time) TaskResult {
	query := `DELETE FROM messages WHERE timestamp < ?`

	result, err := tx.ExecContext(ctx, query, cutoff)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to cleanup old messages",
			Error:   err,
		}
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		t.logger.Printf("[SessionCleanup] Warning: Could not get rows affected for message cleanup: %v", err)
		rowsAffected = 0
	}

	return TaskResult{
		Success:          true,
		RecordsProcessed: int(rowsAffected),
		Message:          fmt.Sprintf("Cleaned up %d old messages", rowsAffected),
	}
}

// cleanupSessions removes sessions older than the cutoff date
func (t *SessionCleanupTask) cleanupSessions(ctx context.Context, tx *sql.Tx, cutoff time.Time) TaskResult {
	query := `DELETE FROM sessions WHERE updated_at < ?`

	result, err := tx.ExecContext(ctx, query, cutoff)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to cleanup old sessions",
			Error:   err,
		}
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		t.logger.Printf("[SessionCleanup] Warning: Could not get rows affected for session cleanup: %v", err)
		rowsAffected = 0
	}

	return TaskResult{
		Success:          true,
		RecordsProcessed: int(rowsAffected),
		Message:          fmt.Sprintf("Cleaned up %d old sessions", rowsAffected),
	}
}

// summarizeSessions creates compressed summaries of sessions before deletion
func (t *SessionCleanupTask) summarizeSessions(ctx context.Context, tx *sql.Tx, cutoff time.Time) TaskResult {
	// First, ensure the session_summaries table exists
	if err := t.ensureSessionSummariesTable(ctx, tx); err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to create session summaries table",
			Error:   err,
		}
	}

	// Find sessions that need summarization
	query := `
		SELECT s.key, s.user_id, s.channel_id, s.created_at, s.updated_at,
		       COUNT(m.id) as message_count,
		       MIN(m.timestamp) as first_message,
		       MAX(m.timestamp) as last_message,
		       json_extract(s.context, '$.session_total_cost') as total_cost
		FROM sessions s
		LEFT JOIN messages m ON s.key = m.session_key
		WHERE s.updated_at < ? AND s.key NOT IN (
			SELECT session_key FROM session_summaries WHERE session_key = s.key
		)
		GROUP BY s.key, s.user_id, s.channel_id, s.created_at, s.updated_at
	`

	rows, err := tx.QueryContext(ctx, query, cutoff)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to query sessions for summarization",
			Error:   err,
		}
	}
	defer rows.Close()

	summaryCount := 0

	// Create summary for each session
	for rows.Next() {
		var sessionKey, userID, channelID string
		var createdAt, updatedAt time.Time
		var messageCount int
		var firstMessageStr, lastMessageStr sql.NullString
		var totalCostStr sql.NullString

		err := rows.Scan(&sessionKey, &userID, &channelID, &createdAt, &updatedAt,
			&messageCount, &firstMessageStr, &lastMessageStr, &totalCostStr)
		if err != nil {
			t.logger.Printf("[SessionCleanup] Warning: Failed to scan session for summary: %v", err)
			continue
		}

		// Parse cost
		var totalCost float64
		if totalCostStr.Valid {
			fmt.Sscanf(totalCostStr.String, "%f", &totalCost)
		}

		// Create session summary
		summary := fmt.Sprintf("Session had %d messages", messageCount)
		if firstMessageStr.Valid && lastMessageStr.Valid {
			// Parse the datetime strings
			firstMessage, err1 := time.Parse(time.RFC3339, firstMessageStr.String)
			lastMessage, err2 := time.Parse(time.RFC3339, lastMessageStr.String)
			if err1 == nil && err2 == nil {
				duration := lastMessage.Sub(firstMessage)
				summary += fmt.Sprintf(" over %v", duration.Round(time.Minute))
			}
		}
		if totalCost > 0 {
			summary += fmt.Sprintf(", estimated cost $%.4f", totalCost)
		}

		// Insert summary
		insertQuery := `
			INSERT INTO session_summaries (session_key, user_id, channel_id,
				created_at, updated_at, message_count, summary, total_cost, summary_created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		`

		_, err = tx.ExecContext(ctx, insertQuery, sessionKey, userID, channelID,
			createdAt, updatedAt, messageCount, summary, totalCost)
		if err != nil {
			t.logger.Printf("[SessionCleanup] Warning: Failed to create summary for session %s: %v",
				sessionKey, err)
			continue
		}

		summaryCount++
	}

	return TaskResult{
		Success:          true,
		RecordsProcessed: summaryCount,
		Message:          fmt.Sprintf("Created %d session summaries", summaryCount),
	}
}

// cleanupSessionSummaries removes old session summaries
func (t *SessionCleanupTask) cleanupSessionSummaries(ctx context.Context, tx *sql.Tx) TaskResult {
	cutoff := time.Now().AddDate(0, 0, -t.config.SummaryRetentionDays)

	query := `DELETE FROM session_summaries WHERE summary_created_at < ?`

	result, err := tx.ExecContext(ctx, query, cutoff)
	if err != nil {
		return TaskResult{
			Success: false,
			Message: "Failed to cleanup old session summaries",
			Error:   err,
		}
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		t.logger.Printf("[SessionCleanup] Warning: Could not get rows affected for summary cleanup: %v", err)
		rowsAffected = 0
	}

	return TaskResult{
		Success:          true,
		RecordsProcessed: int(rowsAffected),
		Message:          fmt.Sprintf("Cleaned up %d old session summaries", rowsAffected),
	}
}

// ensureSessionSummariesTable creates the session_summaries table if it doesn't exist
func (t *SessionCleanupTask) ensureSessionSummariesTable(ctx context.Context, tx *sql.Tx) error {
	query := `
		CREATE TABLE IF NOT EXISTS session_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			user_id TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			message_count INTEGER NOT NULL,
			summary TEXT NOT NULL,
			total_cost REAL DEFAULT 0,
			summary_created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE INDEX IF NOT EXISTS idx_session_summaries_session_key ON session_summaries (session_key);
		CREATE INDEX IF NOT EXISTS idx_session_summaries_user_channel ON session_summaries (user_id, channel_id);
		CREATE INDEX IF NOT EXISTS idx_session_summaries_created_at ON session_summaries (summary_created_at);
	`

	_, err := tx.ExecContext(ctx, query)
	return err
}
