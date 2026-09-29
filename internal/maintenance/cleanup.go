package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// SessionCleanupTask prunes automated sessions (and their messages) older
// than the retention window. See retention.go for the selection rules:
// only sessions whose key starts with a prunable prefix are ever deleted.
type SessionCleanupTask struct {
	db       *sql.DB
	dbPath   string
	searchDB *sql.DB
	config   SessionConfig
	logger   *log.Logger
	now      func() time.Time
}

// NewSessionCleanupTask creates a new session cleanup task. dbPath is the
// gateway database file, used for the pre-prune backup.
func NewSessionCleanupTask(db *sql.DB, dbPath string, config SessionConfig, logger *log.Logger) *SessionCleanupTask {
	if logger == nil {
		logger = log.Default()
	}

	return &SessionCleanupTask{
		db:     db,
		dbPath: dbPath,
		config: config,
		logger: logger,
		now:    time.Now,
	}
}

// SetSearchDB sets the search.db whose messages_fts mirror is pruned along
// with gateway.db (nil to skip).
func (t *SessionCleanupTask) SetSearchDB(db *sql.DB) { t.searchDB = db }

// Name returns the task name
func (t *SessionCleanupTask) Name() string {
	return "session_cleanup"
}

// Description returns the task description
func (t *SessionCleanupTask) Description() string {
	return fmt.Sprintf("Delete automated sessions (%s) and their messages inactive for more than %d days",
		strings.Join(t.config.PrunablePrefixes, ", "), t.config.RetentionDays)
}

// Plan returns what a run would delete, without changing anything.
func (t *SessionCleanupTask) Plan(ctx context.Context) (*PruneReport, error) {
	return PlanPrune(ctx, t.db, RetentionPolicy{
		RetentionDays:    t.config.RetentionDays,
		PrunablePrefixes: t.config.PrunablePrefixes,
	}, t.now())
}

// Execute runs the session cleanup task. In dry-run mode it only plans.
func (t *SessionCleanupTask) Execute(ctx context.Context) TaskResult {
	if !t.config.CleanupEnabled {
		return TaskResult{
			Success: true,
			Message: "Session cleanup disabled in configuration",
		}
	}

	rep, err := t.Plan(ctx)
	if err != nil {
		return TaskResult{Success: false, Message: "Failed to plan session cleanup", Error: err}
	}
	if t.config.DryRun {
		return TaskResult{
			Success: true,
			Details: rep,
			Message: fmt.Sprintf("Dry run: would delete %d sessions and %d messages; keeping %d protected sessions and %d protected messages older than the cutoff",
				rep.PruneSessions, rep.PruneMessages, rep.KeptProtectedSessions, rep.KeptProtectedMessages),
		}
	}
	if rep.PruneSessions == 0 {
		return TaskResult{Success: true, Details: rep, Message: "Nothing to prune"}
	}

	if t.config.BackupBeforePrune {
		path, err := BackupDatabase(ctx, t.db, t.dbPath, t.config.BackupDir, t.now())
		if err != nil {
			return TaskResult{Success: false, Details: rep, Message: "Backup failed; nothing deleted", Error: err}
		}
		rep.BackupPath = path
		t.logger.Printf("[SessionCleanup] Backup written to %s", path)
	}

	if err := ExecutePrune(ctx, t.db, rep, t.config.BatchSize); err != nil {
		return TaskResult{
			Success:          false,
			Details:          rep,
			RecordsProcessed: rep.SessionsDeleted + rep.MessagesDeleted,
			Message:          fmt.Sprintf("Session cleanup stopped after %d sessions", rep.SessionsDeleted),
			Error:            err,
		}
	}

	if t.searchDB != nil {
		n, err := PruneSearchIndex(ctx, t.searchDB, rep.PrunedKeysWithMessages(), t.config.BatchSize)
		rep.SearchIndexDeleted = n
		if err != nil {
			// Not fatal: the gateway rebuilds the mirror at startup when its
			// row count differs from gateway.db's.
			rep.SearchIndexWarnings = append(rep.SearchIndexWarnings, err.Error())
			t.logger.Printf("[SessionCleanup] Warning: %v", err)
		}
	}

	msg := fmt.Sprintf("Deleted %d sessions and %d messages in %d batches", rep.SessionsDeleted, rep.MessagesDeleted, rep.Batches)
	if rep.SkippedChanged > 0 {
		msg += fmt.Sprintf(" (%d skipped: active since planning)", rep.SkippedChanged)
	}
	return TaskResult{
		Success:          true,
		Details:          rep,
		RecordsProcessed: rep.SessionsDeleted + rep.MessagesDeleted,
		Message:          msg,
	}
}

// ShouldRun reports whether the task is enabled.
func (t *SessionCleanupTask) ShouldRun() bool {
	return t.config.CleanupEnabled
}

// NextRun is unused (there is no schedule; conduit-3kgo).
func (t *SessionCleanupTask) NextRun() time.Time {
	return time.Now().Add(24 * time.Hour)
}

// IsDestructive returns true since this task deletes data
func (t *SessionCleanupTask) IsDestructive() bool {
	return true
}
