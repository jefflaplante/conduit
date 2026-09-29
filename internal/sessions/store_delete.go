package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"conduit/internal/database"
)

// Session deletion shared by maintenance retention (conduit-2cxu) and the
// scheduler's end-of-run cleanup of prompt-only cron/heartbeat sessions
// (conduit-385r).

// DependentTables records which optional tables keyed by a session key exist
// in the gateway DB. Neither is created by the gateway migrations:
// claude_code_sessions is created on first use by ClaudeCodeSessionMapper,
// session_summaries only exists in some legacy databases.
type DependentTables struct {
	Summaries          bool // session_summaries(session_key)
	ClaudeCodeMappings bool // claude_code_sessions(conduit_session_id)
}

// RowQuerier is satisfied by *sql.DB and *sql.Tx.
type RowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DetectDependentTables reports which optional session-keyed tables exist.
func DetectDependentTables(ctx context.Context, q RowQuerier) (DependentTables, error) {
	var t DependentTables
	var err error
	if t.Summaries, err = tableExists(ctx, q, "session_summaries"); err != nil {
		return t, err
	}
	if t.ClaudeCodeMappings, err = tableExists(ctx, q, "claude_code_sessions"); err != nil {
		return t, err
	}
	return t, nil
}

func tableExists(ctx context.Context, q RowQuerier, name string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return n > 0, nil
}

// DeletedRows counts the rows DeleteSessionTx removed.
type DeletedRows struct {
	Messages  int
	Summaries int
	Mappings  int
	Sessions  int
}

// DeleteSessionTx deletes one session and every row keyed by it inside tx:
// its messages (gateway.db's messages_fts triggers keep that index in sync),
// its session_summaries and claude_code_sessions rows when those tables
// exist, and the session row itself (which also holds the session context).
// It does not touch search.db or the in-memory state tracker; callers that
// own a Store handle those after commit (see DeleteSessionIfPromptOnly).
func DeleteSessionTx(ctx context.Context, tx *sql.Tx, key string, tables DependentTables) (DeletedRows, error) {
	var d DeletedRows
	exec := func(n *int, query string) error {
		res, err := tx.ExecContext(ctx, query, key)
		if err != nil {
			return err
		}
		*n += rowsAffected(res)
		return nil
	}
	if err := exec(&d.Messages, `DELETE FROM messages WHERE session_key = ?`); err != nil {
		return d, fmt.Errorf("delete messages: %w", err)
	}
	if tables.Summaries {
		if err := exec(&d.Summaries, `DELETE FROM session_summaries WHERE session_key = ?`); err != nil {
			return d, fmt.Errorf("delete session summaries: %w", err)
		}
	}
	if tables.ClaudeCodeMappings {
		if err := exec(&d.Mappings, `DELETE FROM claude_code_sessions WHERE conduit_session_id = ?`); err != nil {
			return d, fmt.Errorf("delete claude code mapping: %w", err)
		}
	}
	if err := exec(&d.Sessions, `DELETE FROM sessions WHERE key = ?`); err != nil {
		return d, fmt.Errorf("delete session: %w", err)
	}
	return d, nil
}

func rowsAffected(res sql.Result) int {
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return int(n)
}

// DeleteSessionIfPromptOnly deletes the session key and its dependent rows
// (DeleteSessionTx) when its transcript holds nothing but plain prompts:
// every message, if any, is a user row with no metadata. That is exactly
// what a scheduler run leaves behind when its turn produced no reply
// (silent, empty, failed or stopped): the TurnRunner stores the prompt
// with no metadata and nothing else (conduit-385r).
//
// Anything else keeps the session: an assistant row (a reply, a sub-agent
// "Error:" row, a compaction summary, a restart notice) or a user row with
// metadata (inter-session deliveries, sub-agent wakes and restart-resume
// notes all carry "source"). The check and the delete run in one IMMEDIATE
// transaction, so a message written concurrently either lands before the
// check (and keeps the session) or fails against the deleted row.
//
// It reports whether the session was deleted; a missing session is not an
// error. After commit the deleted messages are dropped from the search.db
// mirror (MessagesDeletedCallback) and the session from the state tracker.
func (s *Store) DeleteSessionIfPromptOnly(ctx context.Context, key string) (bool, error) {
	// Defense in depth: only scheduler run sessions are ever eligible, even
	// if a caller forgets its own check. An interactive session whose
	// replies all failed must never be deleted by this path.
	if !strings.HasPrefix(key, "cron_") && !strings.HasPrefix(key, "heartbeat_") {
		return false, fmt.Errorf("delete prompt-only session %s: refusing non-scheduler session", key)
	}
	tables, err := DetectDependentTables(ctx, s.db)
	if err != nil {
		return false, err
	}
	var ids []string
	deleted := false
	err = database.RetryOnBusy(5, func() error {
		ids, deleted = nil, false
		tx, err := s.db.BeginTx(ctx, nil) // _txlock=immediate: write lock up front
		if err != nil {
			return err
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit

		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE key = ?`, key).Scan(&exists); err != nil {
			return fmt.Errorf("check session: %w", err)
		}
		if exists == 0 {
			return nil
		}
		var kept int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages
			WHERE session_key = ? AND NOT (role = 'user' AND COALESCE(metadata, '{}') IN ('', '{}'))`,
			key).Scan(&kept); err != nil {
			return fmt.Errorf("check messages: %w", err)
		}
		if kept > 0 {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM messages WHERE session_key = ?`, key)
		if err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("list messages: %w", err)
			}
			ids = append(ids, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("list messages: %w", err)
		}
		if _, err := DeleteSessionTx(ctx, tx, key, tables); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("delete prompt-only session %s: %w", key, err)
	}
	if !deleted {
		return false, nil
	}
	if len(ids) > 0 && s.onMessagesDeleted != nil {
		s.onMessagesDeleted(key, ids)
	}
	s.stateTracker.RemoveSession(key)
	return true, nil
}
