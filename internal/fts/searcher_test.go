package fts

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database with FTS5 tables.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Create tables matching the migration
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS document_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			heading TEXT NOT NULL DEFAULT '',
			chunk_index INTEGER NOT NULL,
			content TEXT NOT NULL,
			file_hash TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS document_chunks_fts USING fts5(
			content,
			heading,
			content=document_chunks,
			content_rowid=id,
			tokenize='porter unicode61'
		)`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(rowid, content, heading)
			VALUES (new.id, new.content, new.heading);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
			VALUES ('delete', old.id, old.content, old.heading);
		END`,
		`CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON document_chunks BEGIN
			INSERT INTO document_chunks_fts(document_chunks_fts, rowid, content, heading)
			VALUES ('delete', old.id, old.content, old.heading);
			INSERT INTO document_chunks_fts(rowid, content, heading)
			VALUES (new.id, new.content, new.heading);
		END`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
			message_id,
			session_key,
			role,
			content,
			tokenize='porter unicode61'
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create table: %v\nSQL: %s", err, stmt)
		}
	}

	return db
}

func TestSearchDocuments_BM25Ranking(t *testing.T) {
	db := setupTestDB(t)

	// Insert test chunks
	chunks := []struct {
		path, heading, content, hash string
	}{
		{"README.md", "## Setup", "Install the database configuration tool", "h1"},
		{"notes.md", "## Misc", "Random notes about cooking and gardening", "h2"},
		{"config.md", "## Database", "Database connection settings and configuration options", "h3"},
	}
	for i, c := range chunks {
		_, err := db.Exec(
			`INSERT INTO document_chunks (file_path, heading, chunk_index, content, file_hash) VALUES (?, ?, ?, ?, ?)`,
			c.path, c.heading, i, c.content, c.hash,
		)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
	}

	s := NewSearcher(db)
	results, err := s.SearchDocuments(context.Background(), "database configuration", 10)
	if err != nil {
		t.Fatalf("SearchDocuments: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// The chunk with both "database" and "configuration" should rank higher
	if results[0].FilePath != "config.md" {
		t.Errorf("expected config.md as top result, got %s", results[0].FilePath)
	}
}

func TestSearchMessages_BasicMatch(t *testing.T) {
	db := setupTestDB(t)

	// Insert test messages into FTS5
	messages := []struct {
		id, sessionKey, role, content string
	}{
		{"m1", "sess1", "user", "What is the weather like today?"},
		{"m2", "sess1", "assistant", "The weather is sunny and warm."},
		{"m3", "sess2", "user", "Tell me about database migrations."},
	}
	for _, m := range messages {
		_, err := db.Exec(
			`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`,
			m.id, m.sessionKey, m.role, m.content,
		)
		if err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}

	s := NewSearcher(db)

	// Search for weather
	results, err := s.SearchMessages(context.Background(), "weather", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}

	if len(results) < 1 {
		t.Fatal("expected at least 1 result for 'weather'")
	}

	// Both weather messages should be found
	foundM1, foundM2 := false, false
	for _, r := range results {
		if r.MessageID == "m1" {
			foundM1 = true
		}
		if r.MessageID == "m2" {
			foundM2 = true
		}
	}
	if !foundM1 || !foundM2 {
		t.Errorf("expected both weather messages, got m1=%v m2=%v", foundM1, foundM2)
	}

	// Search for database - should find m3
	results, err = s.SearchMessages(context.Background(), "database", 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for 'database', got %d", len(results))
	}
	if results[0].MessageID != "m3" {
		t.Errorf("expected m3, got %s", results[0].MessageID)
	}
}

func TestSearch_CombinedResults(t *testing.T) {
	db := setupTestDB(t)

	// Insert a document chunk
	_, err := db.Exec(
		`INSERT INTO document_chunks (file_path, heading, chunk_index, content, file_hash) VALUES (?, ?, ?, ?, ?)`,
		"guide.md", "## Deployment", 0, "Deploy the application using docker compose", "h1",
	)
	if err != nil {
		t.Fatal(err)
	}

	// Insert a message
	_, err = db.Exec(
		`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES (?, ?, ?, ?)`,
		"m1", "sess1", "user", "How do I deploy with docker?",
	)
	if err != nil {
		t.Fatal(err)
	}

	s := NewSearcher(db)
	results, err := s.Search(context.Background(), "deploy docker", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 combined results, got %d", len(results))
	}

	foundDoc, foundMsg := false, false
	for _, r := range results {
		if r.Source == "document" {
			foundDoc = true
		}
		if r.Source == "message" {
			foundMsg = true
		}
	}
	if !foundDoc || !foundMsg {
		t.Errorf("expected both document and message results, got doc=%v msg=%v", foundDoc, foundMsg)
	}
}

func TestSearchDocuments_EmptyQuery(t *testing.T) {
	db := setupTestDB(t)
	s := NewSearcher(db)
	results, err := s.SearchDocuments(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty query, got %d", len(results))
	}
}

func TestBuildFTSQuery(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello world", "hello OR world"},
		{"", ""},
		{"database", "database"},
		{"foo:bar", "foobar"},
		{"test \"quoted\"", "test OR quoted"},
	}

	for _, tc := range tests {
		got := buildFTSQuery(tc.input)
		if got != tc.expected {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
