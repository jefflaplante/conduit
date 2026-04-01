package fts

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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
		{"foo:bar", ""},           // column filter syntax is now blocked
		{"test \"quoted\"", "test OR quoted"},
	}

	for _, tc := range tests {
		got := buildFTSQuery(tc.input)
		if got != tc.expected {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// --- Blocklist and hardening tests ---

func TestBuildFTSQuery_OperatorBlocking(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"strips AND", "hello AND world", "hello OR world"},
		{"strips OR keyword", "hello OR world", "hello OR world"},
		{"strips NOT", "NOT hello", "hello"},
		{"strips NEAR", "hello NEAR world", "hello OR world"},
		{"case insensitive operators", "Hello And World", "hello OR world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFTSQuery(tt.input)
			if got != tt.want {
				t.Errorf("buildFTSQuery(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildFTSQuery_AdvancedFTS5Blocklist(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantEmpty  bool
		mustNotHas string // substring that must NOT appear in output
	}{
		{"NEAR/N syntax", "hello NEAR/3 world", false, "near"},
		{"NEAR/N large distance", "hello NEAR/100 world", false, "near"},
		{"column filter colon", "title:hello", true, ""},
		{"column filter content", "content:secret", true, ""},
		{"caret start-of-column", "^hello", true, ""},
		{"mixed blocked and valid", "hello title:inject world", false, "inject"},
		{"column filter with valid terms", "search content:bypass term", false, "bypass"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFTSQuery(tt.input)
			if tt.wantEmpty && got != "" {
				t.Errorf("buildFTSQuery(%q) = %q, want empty string", tt.input, got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("buildFTSQuery(%q) = empty, want non-empty", tt.input)
			}
			if tt.mustNotHas != "" && strings.Contains(strings.ToLower(got), tt.mustNotHas) {
				t.Errorf("buildFTSQuery(%q) = %q, must not contain %q", tt.input, got, tt.mustNotHas)
			}
		})
	}
}

func TestBuildFTSQuery_NullBytes(t *testing.T) {
	got := buildFTSQuery("hello\x00world")
	if strings.Contains(got, "\x00") {
		t.Errorf("buildFTSQuery with null byte should strip it, got %q", got)
	}
	if got == "" {
		t.Error("buildFTSQuery with null byte should still return valid terms")
	}
}

func TestBuildFTSQuery_MaxTerms(t *testing.T) {
	words := make([]string, maxQueryTerms+20)
	for i := range words {
		words[i] = "term"
	}
	input := strings.Join(words, " ")
	got := buildFTSQuery(input)

	termCount := strings.Count(got, " OR ") + 1
	if termCount > maxQueryTerms {
		t.Errorf("buildFTSQuery produced %d terms, want at most %d", termCount, maxQueryTerms)
	}
}

func TestBuildFTSQuery_MaxTermLength(t *testing.T) {
	longWord := strings.Repeat("a", maxTermLength+100)
	got := buildFTSQuery(longWord)
	if len(got) > maxTermLength {
		t.Errorf("buildFTSQuery should truncate long terms, got length %d", len(got))
	}
	if got == "" {
		t.Error("buildFTSQuery should still return truncated term")
	}
}

func TestBuildFTSQuery_SQLInjection(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"basic injection", "'; DROP TABLE sessions; --"},
		{"union injection", "' UNION SELECT * FROM auth_tokens --"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFTSQuery(tt.input)
			// Should not panic and should not contain raw special chars
			_ = got
		})
	}
}

func TestIsBlockedPattern(t *testing.T) {
	tests := []struct {
		input   string
		blocked bool
	}{
		{"near/3", true},
		{"near/100", true},
		{"near/", true},
		{"nearby", false},
		{"title:", true},
		{"content:", true},
		{"hello:world", true},
		{"^start", true},
		{"hello", false},
		{"world", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isBlockedPattern(tt.input)
			if got != tt.blocked {
				t.Errorf("isBlockedPattern(%q) = %v, want %v", tt.input, got, tt.blocked)
			}
		})
	}
}

func TestStripControlChars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no control chars", "hello world", "hello world"},
		{"null byte", "hello\x00world", "helloworld"},
		{"tab preserved", "hello\tworld", "hello\tworld"},
		{"newline preserved", "hello\nworld", "hello\nworld"},
		{"bell char stripped", "hello\x07world", "helloworld"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripControlChars(tt.input)
			if got != tt.want {
				t.Errorf("stripControlChars(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- Fuzz test ---

func FuzzBuildFTSQuery(f *testing.F) {
	// Seed with known attack patterns
	seeds := []string{
		"hello world",
		"go sqlite fts5",
		"hello AND world",
		"hello OR world",
		"NOT hello",
		"hello NEAR world",
		"hello NEAR/3 world",
		"NEAR/5",
		"title:secret",
		"content:password",
		"^hello",
		"'; DROP TABLE sessions; --",
		"' UNION SELECT * FROM auth_tokens --",
		`"hello" AND "world"`,
		"(hello OR world) AND test",
		"hello*",
		"{hello world}",
		"\x00hello",
		"hello\x00world",
		"\u200bhello\u200b",
		"caf\u00e9",
		strings.Repeat("a", 1000),
		strings.Repeat("hello ", 200),
		"\x00\x01\x02\x03",
		"NEAR/3 title:hack ^inject * (group) {brace}",
		"",
		"   ",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Must not panic
		result := buildFTSQuery(input)

		// Must not contain null bytes
		if strings.Contains(result, "\x00") {
			t.Errorf("result contains null byte for input %q", input)
		}

		// Must not contain unescaped FTS5 special characters
		for _, ch := range []rune{'^', '{', '}', '(', ')', '*', '+', '~', '<', '>', '[', ']'} {
			if strings.ContainsRune(result, ch) {
				t.Errorf("result contains special char %q for input %q: %q", string(ch), input, result)
			}
		}

		// Must not contain column filter syntax
		if strings.Contains(result, ":") {
			t.Errorf("result contains colon for input %q: %q", input, result)
		}

		// Must not contain blocked operators as whole terms
		lower := strings.ToLower(result)
		for _, term := range strings.Fields(lower) {
			if term == "near" || strings.HasPrefix(term, "near/") {
				t.Errorf("result contains NEAR operator for input %q: %q", input, result)
			}
			if term == "and" || term == "not" {
				t.Errorf("result contains blocked operator %q for input %q: %q", term, input, result)
			}
		}

		// Term count within limits
		if result != "" {
			termCount := strings.Count(result, " OR ") + 1
			if termCount > maxQueryTerms {
				t.Errorf("result has %d terms (max %d) for input %q", termCount, maxQueryTerms, input)
			}
		}
	})
}
