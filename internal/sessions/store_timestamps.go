package sessions

import (
	"fmt"
	"strings"
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
