package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"conduit/internal/database"
)

// FTSRebuildTask repairs drift between gateway.db's messages table and its
// messages_fts index (conduit-3dad). messages_fts is a standalone FTS5
// table (it keeps its own copy of the text, keyed by a message_id column,
// because messages has a TEXT primary key), so FTS5's 'rebuild' command
// cannot help: it re-indexes messages_fts from its own content, not from
// messages. Instead the task deletes stale index rows and inserts the
// missing ones in one transaction. Plan and execute share one selection
// (PlanFTSRepair); in dry-run mode only the counts are reported.
type FTSRebuildTask struct {
	db     *sql.DB
	config DatabaseConfig
	logger *log.Logger
}

// NewFTSRebuildTask creates the messages_fts repair task. It uses the
// database config for DryRun only.
func NewFTSRebuildTask(db *sql.DB, config DatabaseConfig, logger *log.Logger) *FTSRebuildTask {
	if logger == nil {
		logger = log.Default()
	}
	return &FTSRebuildTask{db: db, config: config, logger: logger}
}

// Name returns the task name.
func (t *FTSRebuildTask) Name() string { return "fts_rebuild" }

// Description returns the task description.
func (t *FTSRebuildTask) Description() string {
	return "Repair the messages_fts search index: delete stale rows, index missing messages"
}

// FTSRepairReport describes the drift between messages and messages_fts,
// and what a repair did about it.
type FTSRepairReport struct {
	DryRun    bool `json:"dry_run"`
	Messages  int  `json:"messages"`   // rows in messages
	IndexRows int  `json:"index_rows"` // rows in messages_fts when planned

	// Stale index rows (deleted by a repair), by cause.
	Orphaned   int `json:"orphaned"`   // message no longer exists (or NULL message_id)
	Outdated   int `json:"outdated"`   // session_key, role or content differ from the message
	Duplicates int `json:"duplicates"` // extra rows for an already indexed message
	Stale      int `json:"stale"`      // Orphaned + Outdated + Duplicates

	// Messages with no up-to-date index row (indexed by a repair),
	// including those whose only rows are outdated.
	Missing int `json:"missing"`

	Deleted  int `json:"deleted"`
	Inserted int `json:"inserted"`

	staleRowids []int64
	missingIDs  []string
}

// InSync reports whether the index matches the messages table.
func (r *FTSRepairReport) InSync() bool { return r.Stale == 0 && r.Missing == 0 }

// ftsStaleQuery lists the messages_fts rows to delete. The index is the
// outer loop (LEFT JOIN) and each row looks up its message by primary key,
// so this is one pass over the index, not a scan per message. Per
// message_id the first up-to-date row (lowest rowid) is kept; every other
// row is stale.
const ftsStaleQuery = `
	WITH j AS (
		SELECT f.rowid AS rid, f.message_id AS mid, m.id IS NULL AS orphan,
		       (m.id IS NOT NULL AND m.session_key IS f.session_key
		        AND m.role IS f.role AND m.content IS f.content) AS ok
		FROM messages_fts f LEFT JOIN messages m ON m.id = f.message_id
	)
	SELECT rid, orphan, ok FROM (
		SELECT rid, orphan, ok,
		       ROW_NUMBER() OVER (PARTITION BY mid ORDER BY ok DESC, rid) AS rn
		FROM j
	)
	WHERE NOT ok OR rn > 1`

// ftsMissingQuery lists the messages without an up-to-date index row.
// CROSS JOIN keeps messages_fts as the outer loop (see ftsStaleQuery); the
// NOT IN subquery is evaluated once into a temporary index.
const ftsMissingQuery = `
	SELECT id FROM messages
	WHERE id IS NOT NULL AND id NOT IN (
		SELECT f.message_id FROM messages_fts f CROSS JOIN messages m ON m.id = f.message_id
		WHERE m.session_key IS f.session_key AND m.role IS f.role AND m.content IS f.content
	)`

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// PlanFTSRepair compares messages with messages_fts and returns what a
// repair would delete and insert, without changing anything. It returns
// (nil, nil) when the database has no messages_fts table.
func PlanFTSRepair(ctx context.Context, q queryer) (*FTSRepairReport, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'messages_fts'`).Scan(&n); err != nil {
		return nil, fmt.Errorf("check messages_fts: %w", err)
	}
	if n == 0 {
		return nil, nil
	}

	rep := &FTSRepairReport{DryRun: true}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&rep.Messages); err != nil {
		return nil, fmt.Errorf("count messages: %w", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages_fts`).Scan(&rep.IndexRows); err != nil {
		return nil, fmt.Errorf("count messages_fts: %w", err)
	}

	rows, err := q.QueryContext(ctx, ftsStaleQuery)
	if err != nil {
		return nil, fmt.Errorf("find stale messages_fts rows: %w", err)
	}
	for rows.Next() {
		var rid int64
		var orphan, ok bool
		if err := rows.Scan(&rid, &orphan, &ok); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case orphan:
			rep.Orphaned++
		case !ok:
			rep.Outdated++
		default:
			rep.Duplicates++
		}
		rep.staleRowids = append(rep.staleRowids, rid)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("find stale messages_fts rows: %w", err)
	}
	rep.Stale = len(rep.staleRowids)

	rows, err = q.QueryContext(ctx, ftsMissingQuery)
	if err != nil {
		return nil, fmt.Errorf("find unindexed messages: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		rep.missingIDs = append(rep.missingIDs, id)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("find unindexed messages: %w", err)
	}
	rep.Missing = len(rep.missingIDs)
	return rep, nil
}

// RepairFTS re-plans inside one IMMEDIATE transaction (so the selection
// cannot race the gateway's writes), deletes the stale messages_fts rows by
// rowid and indexes the missing messages. SQLITE_BUSY is retried.
func RepairFTS(ctx context.Context, db *sql.DB) (*FTSRepairReport, error) {
	var rep *FTSRepairReport
	err := database.RetryOnBusy(5, func() error {
		tx, err := db.BeginTx(ctx, nil) // _txlock=immediate: write lock up front
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit

		rep, err = PlanFTSRepair(ctx, tx)
		if err != nil || rep == nil || rep.InSync() {
			return err
		}
		rep.DryRun = false

		del, err := tx.PrepareContext(ctx, `DELETE FROM messages_fts WHERE rowid = ?`)
		if err != nil {
			return err
		}
		defer del.Close()
		for _, rid := range rep.staleRowids {
			res, err := del.ExecContext(ctx, rid)
			if err != nil {
				return fmt.Errorf("delete stale messages_fts row: %w", err)
			}
			rep.Deleted += rowsAffected(res)
		}

		ins, err := tx.PrepareContext(ctx, `INSERT INTO messages_fts(message_id, session_key, role, content)
			SELECT id, session_key, role, content FROM messages WHERE id = ?`)
		if err != nil {
			return err
		}
		defer ins.Close()
		for _, id := range rep.missingIDs {
			res, err := ins.ExecContext(ctx, id)
			if err != nil {
				return fmt.Errorf("index message: %w", err)
			}
			rep.Inserted += rowsAffected(res)
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// Summary renders the drift counts in one line.
func (r *FTSRepairReport) Summary() string {
	return fmt.Sprintf("%d stale index rows (%d orphaned, %d outdated, %d duplicate) and %d unindexed messages; %d messages, %d index rows",
		r.Stale, r.Orphaned, r.Outdated, r.Duplicates, r.Missing, r.Messages, r.IndexRows)
}

// Execute runs the repair, or only plans it in dry-run mode.
func (t *FTSRebuildTask) Execute(ctx context.Context) TaskResult {
	if t.config.DryRun {
		rep, err := PlanFTSRepair(ctx, t.db)
		if err != nil {
			return TaskResult{Success: false, Message: "Failed to plan messages_fts repair", Error: err}
		}
		if rep == nil {
			return TaskResult{Success: true, Message: "No messages_fts table; nothing to repair"}
		}
		if rep.InSync() {
			return TaskResult{Success: true, Details: rep, Message: "Dry run: messages_fts is in sync (" + rep.Summary() + ")"}
		}
		return TaskResult{Success: true, Details: rep,
			Message: fmt.Sprintf("Dry run: would delete %d and insert %d messages_fts rows: %s", rep.Stale, rep.Missing, rep.Summary())}
	}

	rep, err := RepairFTS(ctx, t.db)
	if err != nil {
		return TaskResult{Success: false, Message: "messages_fts repair failed; nothing changed", Error: err}
	}
	if rep == nil {
		return TaskResult{Success: true, Message: "No messages_fts table; nothing to repair"}
	}
	if rep.InSync() {
		return TaskResult{Success: true, Details: rep, Message: "messages_fts is in sync (" + rep.Summary() + ")"}
	}
	t.logger.Printf("[FTSRebuild] Deleted %d stale rows, indexed %d messages", rep.Deleted, rep.Inserted)
	return TaskResult{
		Success:          true,
		Details:          rep,
		RecordsProcessed: rep.Deleted + rep.Inserted,
		Message:          fmt.Sprintf("Repaired messages_fts: deleted %d stale rows, indexed %d messages (found %s)", rep.Deleted, rep.Inserted, rep.Summary()),
	}
}

// ShouldRun reports whether the task is enabled (always).
func (t *FTSRebuildTask) ShouldRun() bool { return true }

// NextRun is unused (there is no schedule; conduit-3kgo).
func (t *FTSRebuildTask) NextRun() time.Time { return time.Now().Add(24 * time.Hour) }

// IsDestructive returns false: only the derived search index changes, and
// every row it deletes is either re-created from messages or refers to a
// message that no longer exists.
func (t *FTSRebuildTask) IsDestructive() bool { return false }
