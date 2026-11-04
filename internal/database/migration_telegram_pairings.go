package database

import (
	"database/sql"
	"log"
)

// Migration 13 (C1): telegram_pairings gets an explicit status column.
//
// Before, is_active=0 meant both "approved by the admin" and "superseded by
// a newer code", and IsUserPaired counted any is_active=0 row as paired, so
// a stranger who let a code expire and asked for another was paired with no
// approval. Now:
//
//	pending    is_active=1, waiting for the admin (may have expired)
//	approved   is_active=0, approved by the admin; the only paired state
//	superseded is_active=0, replaced by a newer code; never paired
//
// Existing is_active=0 rows cannot be told apart (approval wrote nothing
// else), so they fail closed to superseded: a user approved before this
// migration has to pair again (message the bot, admin approves the new
// code). The count is logged.
//
// L13: created_at and expires_at were bound as time.Time, which the driver
// stores as t.String() in the process's local zone, while the CLI compared
// them with SQLite's UTC datetime('now'). The pairing code now binds
// FormatStoredTime values; existing rows are rewritten into that layout here
// (same rules as migration 10: unparseable values are left as they are).
//
// Idempotent: the ALTER is skipped when the column exists, only rows still
// at the column default are reclassified, and the time rewrite skips
// canonical values.
func addTelegramPairingsStatus(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('telegram_pairings') WHERE name = 'status'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := tx.Exec(`ALTER TABLE telegram_pairings ADD COLUMN status TEXT NOT NULL DEFAULT 'pending'`); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE telegram_pairings SET status = 'superseded' WHERE is_active = 0 AND status = 'pending'`)
	if err != nil {
		return err
	}
	if reset, _ := res.RowsAffected(); reset > 0 {
		log.Printf("[database] migration 13: %d inactive Telegram pairing rows marked superseded; previously approved Telegram users must pair again", reset)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_telegram_pairings_user_status ON telegram_pairings (user_id, status)`); err != nil {
		return err
	}
	for _, col := range []string{"created_at", "expires_at"} {
		st, err := normalizeTimeColumnTx(tx, "telegram_pairings", col)
		if err != nil {
			return err
		}
		if st.Rewritten > 0 || st.Unparseable > 0 || st.NonText > 0 {
			log.Printf("[database] migration 13: %s", st)
		}
	}
	return nil
}
