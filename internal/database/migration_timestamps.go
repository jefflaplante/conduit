package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// Migration 10 (conduit-a636): rewrite messages.timestamp and
// sessions.created_at into StoredTimeLayout.
//
// Both columns were written by binding a time.Time with no driver write
// format, so they hold time.Time.String() text such as
// "2026-02-13 01:03:25.453024355 -0800 PST m=+2227.791803474": the writing
// process's zone plus a monotonic reading. Rows written by
// CURRENT_TIMESTAMP defaults hold "2026-02-13 09:03:25" (UTC). Text in
// these mixed forms does not sort chronologically, so ORDER BY timestamp
// and SQL cutoffs were wrong across zone/DST changes. The store now binds
// FormatStoredTime values; this migration converts the existing rows.
//
// Parsing needs Go (zone offsets, optional zone names, monotonic suffix), so
// the migration is a Go function run inside the migration transaction.
// Every value is parsed, re-rendered in UTC and parsed back; it is written
// only if the round trip gives the same instant. Values already canonical
// are skipped (the rewrite is idempotent). Values that do not parse, and
// non-text values, are left untouched and counted in the log; they never
// fail the migration.
//
// messages_fts: the UPDATE triggers on messages would delete and re-insert
// every message's index row although the timestamp is not indexed. They are
// dropped for the rewrite and re-created from their exact stored SQL in the
// same transaction, so the FTS index is not touched and stays exactly as in
// sync as it was.

// timeColumnStats counts the outcome of normalizing one column.
type timeColumnStats struct {
	Table, Column string
	Rewritten     int
	Canonical     int
	Unparseable   int
	NonText       int      // non-NULL values stored as numbers/blobs, left as they are
	Samples       []string // up to 3 unparseable values, for the log
}

func (s timeColumnStats) String() string {
	msg := fmt.Sprintf("%s.%s: %d rewritten, %d already canonical, %d unparseable, %d non-text",
		s.Table, s.Column, s.Rewritten, s.Canonical, s.Unparseable, s.NonText)
	if len(s.Samples) > 0 {
		msg += fmt.Sprintf(" (e.g. %q)", s.Samples)
	}
	return msg
}

// normalizeTimestampsMigration is migration 10's function.
func normalizeTimestampsMigration(tx *sql.Tx) error {
	stats, err := normalizeTimestampColumnsTx(tx)
	if err != nil {
		return err
	}
	for _, s := range stats {
		if s.Rewritten > 0 || s.Unparseable > 0 || s.NonText > 0 {
			log.Printf("[database] migration 10: %s", s)
		}
		if s.Unparseable > 0 || s.NonText > 0 {
			log.Printf("[database] WARNING: migration 10 left %d %s.%s values unconverted; they keep their old text",
				s.Unparseable+s.NonText, s.Table, s.Column)
		}
	}
	return nil
}

// normalizeTimestampColumnsTx rewrites messages.timestamp and
// sessions.created_at in tx. Idempotent.
func normalizeTimestampColumnsTx(tx *sql.Tx) ([]timeColumnStats, error) {
	var out []timeColumnStats
	for _, c := range [][2]string{{"messages", "timestamp"}, {"sessions", "created_at"}} {
		restore, err := suspendUpdateTriggers(tx, c[0])
		if err != nil {
			return nil, err
		}
		st, err := normalizeTimeColumnTx(tx, c[0], c[1])
		if err != nil {
			return nil, err
		}
		if err := restore(); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// suspendUpdateTriggers drops the triggers on table that mention UPDATE and
// returns a function that re-creates them from their exact stored SQL.
func suspendUpdateTriggers(tx *sql.Tx, table string) (func() error, error) {
	rows, err := tx.Query(`SELECT name, sql FROM sqlite_master
		WHERE type = 'trigger' AND tbl_name = ? AND upper(sql) LIKE '%UPDATE%'
		ORDER BY name`, table)
	if err != nil {
		return nil, fmt.Errorf("list %s triggers: %w", table, err)
	}
	var names, defs []string
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			rows.Close()
			return nil, err
		}
		names, defs = append(names, name), append(defs, def)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, n := range names {
		if _, err := tx.Exec(`DROP TRIGGER "` + strings.ReplaceAll(n, `"`, `""`) + `"`); err != nil {
			return nil, fmt.Errorf("suspend trigger %s: %w", n, err)
		}
	}
	return func() error {
		for i, def := range defs {
			if _, err := tx.Exec(def); err != nil {
				return fmt.Errorf("restore trigger %s: %w", names[i], err)
			}
		}
		return nil
	}, nil
}

// normalizeTimeColumnTx rewrites every text value of table.column that is
// not already canonical into StoredTimeLayout.
func normalizeTimeColumnTx(tx *sql.Tx, table, column string) (timeColumnStats, error) {
	st := timeColumnStats{Table: table, Column: column}
	// CAST keeps the stored text exactly (the driver would otherwise parse
	// DATETIME columns into time.Time on scan).
	rows, err := tx.Query(fmt.Sprintf(`SELECT rowid, CAST(%[1]s AS TEXT), typeof(%[1]s), %[2]s FROM %[3]s WHERE %[1]s IS NOT NULL`,
		column, CanonicalTimeGlobSQL(column), table))
	if err != nil {
		return st, fmt.Errorf("scan %s.%s: %w", table, column, err)
	}
	type fix struct {
		rowid int64
		val   string
	}
	var fixes []fix
	for rows.Next() {
		var (
			rowid     int64
			v, typ    string
			canonical bool
		)
		if err := rows.Scan(&rowid, &v, &typ, &canonical); err != nil {
			rows.Close()
			return st, fmt.Errorf("scan %s.%s: %w", table, column, err)
		}
		switch {
		case typ != "text":
			st.NonText++
		case canonical:
			st.Canonical++
		default:
			if c, ok := canonicalStoredTime(v); ok {
				fixes = append(fixes, fix{rowid, c})
			} else {
				st.Unparseable++
				if len(st.Samples) < 3 {
					st.Samples = append(st.Samples, v)
				}
			}
		}
	}
	if err := rows.Close(); err != nil {
		return st, err
	}
	if len(fixes) == 0 {
		return st, nil
	}
	stmt, err := tx.Prepare(fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ?`, table, column))
	if err != nil {
		return st, err
	}
	defer stmt.Close()
	for _, f := range fixes {
		if _, err := stmt.Exec(f.val, f.rowid); err != nil {
			return st, fmt.Errorf("rewrite %s.%s rowid %d: %w", table, column, f.rowid, err)
		}
		st.Rewritten++
	}
	return st, nil
}
