package database

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Migration represents a database migration
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// GetMigrations returns all available migrations in order
func GetMigrations() []Migration {
	return []Migration{
		{
			Version: 1,
			Name:    "create_sessions_table",
			SQL: `
				CREATE TABLE IF NOT EXISTS sessions (
					key TEXT PRIMARY KEY,
					user_id TEXT NOT NULL,
					channel_id TEXT NOT NULL,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					message_count INTEGER DEFAULT 0,
					context TEXT DEFAULT '{}'
				);
				
				CREATE TABLE IF NOT EXISTS messages (
					id TEXT PRIMARY KEY,
					session_key TEXT NOT NULL,
					role TEXT NOT NULL,
					content TEXT NOT NULL,
					timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
					metadata TEXT DEFAULT '{}',
					FOREIGN KEY (session_key) REFERENCES sessions (key)
				);
				
				CREATE INDEX IF NOT EXISTS idx_sessions_user_channel ON sessions (user_id, channel_id);
				CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions (updated_at);
				CREATE INDEX IF NOT EXISTS idx_messages_session_key ON messages (session_key);
				CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages (timestamp);
			`,
		},
		{
			Version: 2,
			Name:    "create_auth_tokens_table",
			SQL: `
				-- Create auth_tokens table for API authentication
				CREATE TABLE IF NOT EXISTS auth_tokens (
					token_id TEXT PRIMARY KEY,
					client_name TEXT NOT NULL,
					hashed_token TEXT NOT NULL UNIQUE,
					created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
					expires_at DATETIME,
					last_used_at DATETIME,
					is_active BOOLEAN DEFAULT 1,
					metadata TEXT DEFAULT '{}'
				);
				
				-- Create indexes for performance
				CREATE INDEX IF NOT EXISTS idx_auth_tokens_client_name ON auth_tokens (client_name);
				CREATE INDEX IF NOT EXISTS idx_auth_tokens_expires_at ON auth_tokens (expires_at);
				CREATE INDEX IF NOT EXISTS idx_auth_tokens_hashed_token ON auth_tokens (hashed_token);
				CREATE INDEX IF NOT EXISTS idx_auth_tokens_active ON auth_tokens (is_active);
				
				-- Create a migration tracking table
				CREATE TABLE IF NOT EXISTS schema_migrations (
					version INTEGER PRIMARY KEY,
					name TEXT NOT NULL,
					applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);
			`,
		},
		{
			Version: 3,
			Name:    "create_telegram_pairings_table",
			SQL: `
				-- Create telegram_pairings table for Telegram pairing system
				CREATE TABLE IF NOT EXISTS telegram_pairings (
					code VARCHAR(36) PRIMARY KEY,
					user_id TEXT NOT NULL,
					created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					expires_at TIMESTAMP NOT NULL,
					is_active BOOLEAN DEFAULT 1,
					metadata TEXT DEFAULT '{}'
				);

				-- Create indexes for performance
				CREATE INDEX IF NOT EXISTS idx_telegram_pairings_user_id ON telegram_pairings (user_id);
				CREATE INDEX IF NOT EXISTS idx_telegram_pairings_expires_at ON telegram_pairings (expires_at);
				CREATE INDEX IF NOT EXISTS idx_telegram_pairings_is_active ON telegram_pairings (is_active);
			`,
		},
		{
			Version: 4,
			Name:    "create_fts5_search_tables",
			SQL: `
				-- Chunk storage for workspace markdown files
				CREATE TABLE IF NOT EXISTS document_chunks (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					file_path TEXT NOT NULL,
					heading TEXT NOT NULL DEFAULT '',
					chunk_index INTEGER NOT NULL,
					content TEXT NOT NULL,
					file_hash TEXT NOT NULL,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);

				CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON document_chunks(file_path);
				CREATE INDEX IF NOT EXISTS idx_chunks_file_hash ON document_chunks(file_hash);

				-- FTS5 index over document chunks
				CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
					content,
					heading,
					content=document_chunks,
					content_rowid=id,
					tokenize='porter unicode61'
				);

				-- Triggers to keep FTS5 in sync with document_chunks
				CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(rowid, content, heading)
					VALUES (new.id, new.content, new.heading);
				END;

				CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
					VALUES ('delete', old.id, old.content, old.heading);
				END;

				CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
					VALUES ('delete', old.id, old.content, old.heading);
					INSERT INTO document_chunks_fts(rowid, content, heading)
					VALUES (new.id, new.content, new.heading);
				END;

				-- Standalone FTS5 index for session messages (standalone because messages uses TEXT PK)
				CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
					message_id,
					session_key,
					role,
					content,
					tokenize='porter unicode61'
				);

				-- Backfill existing messages into FTS5
				INSERT INTO messages_fts(message_id, session_key, role, content)
				SELECT id, session_key, role, content FROM messages;
			`,
		},
	}
}

// RunMigrations executes all pending migrations
func RunMigrations(db *sql.DB) error {
	// First, create the migrations table if it doesn't exist
	if err := ensureMigrationsTable(db); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// Get current schema version
	currentVersion, err := getCurrentVersion(db)
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	// Run pending migrations
	migrations := GetMigrations()
	for _, migration := range migrations {
		if migration.Version <= currentVersion {
			continue // Already applied
		}

		if err := runMigration(db, migration); err != nil {
			return fmt.Errorf("failed to run migration %d (%s): %w", migration.Version, migration.Name, err)
		}
	}

	return nil
}

// ensureMigrationsTable creates the schema_migrations table if it doesn't exist
func ensureMigrationsTable(db *sql.DB) error {
	sql := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`
	_, err := db.Exec(sql)
	return err
}

// getCurrentVersion returns the current schema version
func getCurrentVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		// If table doesn't exist, return version 0
		if err.Error() == "SQL logic error: no such table: schema_migrations (1)" {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}

// runMigration executes a single migration
func runMigration(db *sql.DB, migration Migration) error {
	// Start transaction
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Execute migration SQL
	if _, err := tx.Exec(migration.SQL); err != nil {
		return err
	}

	// Record migration as applied
	if _, err := tx.Exec(
		"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
		migration.Version, migration.Name,
	); err != nil {
		return err
	}

	// Commit transaction
	return tx.Commit()
}

// ConfigureDatabase applies SQLite optimizations and runs migrations
func ConfigureDatabase(db *sql.DB) error {
	// Configure connection pool for SQLite
	// SQLite serializes writes, so limit connections to avoid contention.
	// WAL mode allows concurrent readers, so we allow a few connections.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(0) // Don't expire connections

	// Apply SQLite performance configurations
	pragmas := []string{
		"PRAGMA journal_mode=WAL",   // Write-ahead logging for better concurrency
		"PRAGMA busy_timeout=5000",  // Wait up to 5 seconds for locks
		"PRAGMA synchronous=NORMAL", // Safer sync mode with good performance
		"PRAGMA cache_size=10000",   // Increase cache size for better performance
		"PRAGMA foreign_keys=ON",    // Enforce foreign key constraints
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("failed to apply pragma '%s': %w", pragma, err)
		}
	}

	// Run all pending migrations
	if err := RunMigrations(db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}
