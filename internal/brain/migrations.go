package brain

import (
	"database/sql"
	"fmt"
	"log"
)

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS brain_ltm (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT (datetime('now')),
		accessed_at DATETIME NOT NULL DEFAULT (datetime('now')),
		access_count INTEGER NOT NULL DEFAULT 0,
		salience REAL NOT NULL DEFAULT 0.0,
		source TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_brain_ltm_salience ON brain_ltm(salience);
	CREATE INDEX IF NOT EXISTS idx_brain_ltm_accessed ON brain_ltm(accessed_at);`,
}

func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS brain_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var currentVersion int
	if err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM brain_migrations").Scan(&currentVersion); err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	for i := currentVersion; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d failed: %w", i+1, err)
		}
		if _, err := tx.Exec("INSERT INTO brain_migrations (version) VALUES (?)", i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
		log.Printf("Brain: applied migration %d", i+1)
	}
	return nil
}
