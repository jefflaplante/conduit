package sessions

import (
	"fmt"
	"time"

	"conduit/internal/database"
)

// updatedAtLayout is the canonical on-disk format for sessions.updated_at:
// UTC, fixed-width nanoseconds, so lexicographic ORDER BY / < comparisons
// match chronological order and modernc parses it back into a time.Time.
//
// conduit-31jg.24: previously updated_at was written both by Go (the
// driver's time.Time.String(), in the process's local zone, with a
// monotonic "m=+..." suffix) and by SQL CURRENT_TIMESTAMP (UTC, second
// precision), so ORDER BY updated_at could pick the wrong "latest" session.
//
// conduit-a636: messages.timestamp and sessions.created_at use the same
// layout (database.StoredTimeLayout); gateway.db migration 10 converted
// their existing rows.
const updatedAtLayout = database.StoredTimeLayout

// canonicalUpdatedAtGlob matches an updated_at already in updatedAtLayout.
var canonicalUpdatedAtGlob = database.CanonicalTimeGlobSQL("updated_at")

// formatUpdatedAt renders t in the canonical updated_at format.
func formatUpdatedAt(t time.Time) string {
	return database.FormatStoredTime(t)
}

// formatStoredTime renders t for messages.timestamp / sessions.created_at.
// Binding the time.Time itself would store the driver's t.String() text
// (process zone + monotonic reading), which does not sort. conduit-a636.
func formatStoredTime(t time.Time) string {
	return database.FormatStoredTime(t)
}

// FormatUpdatedAt renders t in the canonical sessions.updated_at format, for
// other packages that compare against the column (maintenance cleanup).
// Binding a time.Time instead compares against the driver's text rendering,
// which does not sort with the canonical layout.
func FormatUpdatedAt(t time.Time) string { return formatUpdatedAt(t) }

// nowUpdatedAt returns the current time in the canonical updated_at format.
func nowUpdatedAt() string { return formatUpdatedAt(time.Now()) }

// ParseStoredTime parses a timestamp as stored in gateway.db text columns:
// the canonical layout, Go time.Time.String() output from the driver's
// default time binding (including a trailing monotonic "m=+..." reading and
// any zone name, as legacy messages.timestamp and sessions.created_at rows
// hold), RFC 3339 variants, and SQLite CURRENT_TIMESTAMP. Zone-less forms
// are UTC. conduit-2cxu: the maintenance CLI compares these in Go so that a
// database whose rows migration 10 could not convert is still handled.
func ParseStoredTime(v string) (time.Time, bool) { return database.ParseStoredTime(v) }

// parseLegacyTime parses a legacy updated_at string. Zone-less forms are
// UTC (that is what SQLite's CURRENT_TIMESTAMP produces).
func parseLegacyTime(v string) (time.Time, bool) { return database.ParseStoredTime(v) }

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
