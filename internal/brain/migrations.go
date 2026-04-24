package brain

import (
	"database/sql"
	"fmt"
	"log"
)

type migration struct {
	Version int
	SQL     string
}

var migrations = []migration{
	// Migration 1: Initial schema
	{
		Version: 1,
		SQL: `CREATE TABLE IF NOT EXISTS brain_ltm (
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
	},
	// Migration 2: REM Sleep support
	{
		Version: 2,
		SQL: `ALTER TABLE brain_ltm ADD COLUMN source_hash TEXT DEFAULT '';

		CREATE TABLE IF NOT EXISTS brain_archive (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			source TEXT DEFAULT '',
			tier TEXT DEFAULT 'longterm',
			salience REAL DEFAULT 0.0,
			archived_at DATETIME DEFAULT (datetime('now')),
			reason TEXT DEFAULT ''
		);

		CREATE TABLE IF NOT EXISTS brain_relationships (
			key_a TEXT NOT NULL,
			key_b TEXT NOT NULL,
			relationship TEXT DEFAULT 'related',
			confidence REAL DEFAULT 0.5,
			created_at DATETIME DEFAULT (datetime('now')),
			PRIMARY KEY (key_a, key_b)
		);

		CREATE INDEX IF NOT EXISTS idx_brain_rel_key_a ON brain_relationships(key_a);
		CREATE INDEX IF NOT EXISTS idx_brain_rel_key_b ON brain_relationships(key_b);`,
	},
	// Migration 3: Staleness tracking and source indexes
	{
		Version: 3,
		SQL: `ALTER TABLE brain_ltm ADD COLUMN stale INTEGER DEFAULT 0;
		CREATE INDEX IF NOT EXISTS idx_brain_ltm_source ON brain_ltm(source);
		CREATE INDEX IF NOT EXISTS idx_brain_ltm_source_hash ON brain_ltm(source_hash);`,
	},
	// Migration 4: SPAR Reflect - brain_reflections table
	{
		Version: 4,
		SQL: `CREATE TABLE IF NOT EXISTS brain_reflections (
			id TEXT PRIMARY KEY,
			session_key TEXT NOT NULL,
			timestamp DATETIME NOT NULL,
			source TEXT NOT NULL,
			type TEXT NOT NULL,
			tool TEXT,
			outcome TEXT NOT NULL,
			retry_count INTEGER DEFAULT 0,
			duration_ms INTEGER DEFAULT 0,
			insight TEXT,
			score INTEGER DEFAULT 0,
			tags TEXT,
			related_keys TEXT,
			rem_processed INTEGER DEFAULT 0
		);

		CREATE INDEX idx_reflections_session ON brain_reflections (session_key);
		CREATE INDEX idx_reflections_timestamp ON brain_reflections (timestamp);
		CREATE INDEX idx_reflections_tool ON brain_reflections (tool);
		CREATE INDEX idx_reflections_type ON brain_reflections (type);
		CREATE INDEX idx_reflections_rem ON brain_reflections (rem_processed);`,
	},
	// Migration 5: TTL/expiry support for time-sensitive entries
	{
		Version: 5,
		SQL: `ALTER TABLE brain_ltm ADD COLUMN expires_at DATETIME;
		CREATE INDEX IF NOT EXISTS idx_brain_ltm_expires_at ON brain_ltm(expires_at);`,
	},
	// Migration 6: Spreading activation — transient warmth for recently-activated neighbours
	{
		Version: 6,
		SQL: `ALTER TABLE brain_ltm ADD COLUMN warmth REAL NOT NULL DEFAULT 0.0;
		CREATE INDEX IF NOT EXISTS idx_brain_ltm_warmth ON brain_ltm(warmth);`,
	},
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
		m := migrations[i]
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d failed: %w", m.Version, err)
		}
		if _, err := tx.Exec("INSERT INTO brain_migrations (version) VALUES (?)", m.Version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.Version, err)
		}
		log.Printf("Brain: applied migration %d", m.Version)
	}
	return nil
}
