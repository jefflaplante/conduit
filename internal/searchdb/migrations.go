package searchdb

import (
	"fmt"
)

// SearchMigration represents a search database migration
type SearchMigration struct {
	Version int
	Name    string
	SQL     string
}

// getSearchMigrations returns all search database migrations in order.
// These are separate from gateway.db migrations to allow independent evolution.
func getSearchMigrations() []SearchMigration {
	return []SearchMigration{
		{
			Version: 1,
			Name:    "create_document_chunks_and_beads_fts",
			SQL: `
				-- Document chunk storage (mirrors gateway.db document_chunks for FTS)
				CREATE TABLE IF NOT EXISTS document_chunks (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					file_path TEXT NOT NULL,
					heading TEXT NOT NULL DEFAULT '',
					chunk_index INTEGER NOT NULL,
					content TEXT NOT NULL,
					file_hash TEXT NOT NULL,
					updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
				);

				CREATE INDEX IF NOT EXISTS idx_search_chunks_file_path ON document_chunks(file_path);
				CREATE INDEX IF NOT EXISTS idx_search_chunks_file_hash ON document_chunks(file_hash);

				-- FTS5 index over document chunks (content-synced with triggers)
				CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
					content,
					heading,
					content=document_chunks,
					content_rowid=id,
					tokenize='porter unicode61'
				);

				-- Triggers to keep FTS5 in sync with document_chunks
				CREATE TRIGGER IF NOT EXISTS search_chunks_ai AFTER INSERT ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(rowid, content, heading)
					VALUES (new.id, new.content, new.heading);
				END;

				CREATE TRIGGER IF NOT EXISTS search_chunks_ad AFTER DELETE ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
					VALUES ('delete', old.id, old.content, old.heading);
				END;

				CREATE TRIGGER IF NOT EXISTS search_chunks_au AFTER UPDATE ON document_chunks BEGIN
					INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
					VALUES ('delete', old.id, old.content, old.heading);
					INSERT INTO document_chunks_fts(rowid, content, heading)
					VALUES (new.id, new.content, new.heading);
				END;

				-- Standalone FTS5 table for beads issues (no backing data table needed)
				CREATE VIRTUAL TABLE IF NOT EXISTS beads_fts USING fts5(
					issue_id,
					title,
					description,
					status,
					issue_type,
					owner,
					tokenize='porter unicode61'
				);
			`,
		},
		{
			Version: 2,
			Name:    "create_messages_fts",
			SQL: `
				-- Standalone FTS5 index for session messages
				-- This mirrors messages from gateway.db but lives in search.db
				CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
					message_id,
					session_key,
					role,
					content,
					tokenize='porter unicode61'
				);

				-- Index for efficient session-scoped searches
				-- Note: FTS5 doesn't support traditional indexes, but we can use
				-- column filters in MATCH queries for session_key
			`,
		},
	}
}

// runMigrations executes all pending search database migrations.
func (s *SearchDB) runMigrations() error {
	if err := s.ensureMigrationsTable(); err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	currentVersion, err := s.getCurrentVersion()
	if err != nil {
		return fmt.Errorf("failed to get current version: %w", err)
	}

	migrations := getSearchMigrations()
	for _, migration := range migrations {
		if migration.Version <= currentVersion {
			continue
		}

		if err := s.runMigration(migration); err != nil {
			return fmt.Errorf("failed to run migration %d (%s): %w", migration.Version, migration.Name, err)
		}
	}

	return nil
}

// ensureMigrationsTable creates the search-specific migrations table.
func (s *SearchDB) ensureMigrationsTable() error {
	sql := `
		CREATE TABLE IF NOT EXISTS search_schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`
	_, err := s.db.Exec(sql)
	return err
}

// getCurrentVersion returns the current search schema version.
func (s *SearchDB) getCurrentVersion() (int, error) {
	var version int
	err := s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM search_schema_migrations").Scan(&version)
	if err != nil {
		// Table doesn't exist yet
		if err.Error() == "SQL logic error: no such table: search_schema_migrations (1)" {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}

// runMigration executes a single migration within a transaction.
func (s *SearchDB) runMigration(migration SearchMigration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(migration.SQL); err != nil {
		return err
	}

	if _, err := tx.Exec(
		"INSERT INTO search_schema_migrations (version, name) VALUES (?, ?)",
		migration.Version, migration.Name,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// RebuildFTS5Indexes drops and recreates all FTS5 indexes.
// This is useful for fixing corrupted indexes or after bulk data changes.
func (s *SearchDB) RebuildFTS5Indexes() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Rebuild document_chunks_fts
	if _, err := s.db.Exec("INSERT INTO document_chunks_fts(document_chunks_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("failed to rebuild document_chunks_fts: %w", err)
	}

	// Note: beads_fts and messages_fts are standalone (no content= table),
	// so they don't support 'rebuild' command. They would need to be
	// dropped and recreated with data re-inserted.

	return nil
}

// Vacuum runs VACUUM on the search database to reclaim space.
func (s *SearchDB) Vacuum() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("VACUUM")
	return err
}

// GetStats returns statistics about the search database.
func (s *SearchDB) GetStats() (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := make(map[string]interface{})

	// Document chunks count
	var docCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&docCount); err == nil {
		stats["document_chunks"] = docCount
	}

	// Messages count
	var msgCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM messages_fts").Scan(&msgCount); err == nil {
		stats["messages_indexed"] = msgCount
	}

	// Beads count
	var beadsCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM beads_fts").Scan(&beadsCount); err == nil {
		stats["beads_indexed"] = beadsCount
	}

	// Database file size (approximate via page count)
	var pageCount, pageSize int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err == nil {
		if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err == nil {
			stats["size_bytes"] = pageCount * pageSize
		}
	}

	stats["path"] = s.path

	return stats, nil
}
