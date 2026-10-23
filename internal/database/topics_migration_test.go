package database

import (
	"testing"
)

// TestMigration11_TopicsTablesCreated verifies migration #11 creates the
// topics and topic_pointers tables with the spec'd columns and the
// session_key index (conduit-43vk).
func TestMigration11_TopicsTablesCreated(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	tests := []struct {
		table string
		cols  []string
	}{
		{"topics", []string{"id", "slug", "name", "status", "persona", "summary", "session_key", "created_at", "updated_at"}},
		{"topic_pointers", []string{"chat_id", "topic_slug"}},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, tt.table)
			if err != nil {
				t.Fatalf("table_info: %v", err)
			}
			defer rows.Close()
			got := map[string]bool{}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					t.Fatal(err)
				}
				got[name] = true
			}
			for _, c := range tt.cols {
				if !got[c] {
					t.Errorf("%s missing column %s (have %v)", tt.table, c, got)
				}
			}
			if len(got) != len(tt.cols) {
				t.Errorf("%s has %d columns, want %d: %v", tt.table, len(got), len(tt.cols), got)
			}
		})
	}

	var idx int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_topics_session'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if idx != 1 {
		t.Fatalf("idx_topics_session missing")
	}

	// Defaults per spec.
	if _, err := db.Exec(`INSERT INTO topics (slug, name, session_key) VALUES ('alpha', 'Alpha', 'topic_x')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var status, persona, summary string
	if err := db.QueryRow(`SELECT status, persona, summary FROM topics WHERE slug='alpha'`).Scan(&status, &persona, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "active" || persona != "" || summary != "" {
		t.Fatalf("defaults = %q %q %q", status, persona, summary)
	}
	if _, err := db.Exec(`INSERT INTO topics (slug, name, session_key) VALUES ('alpha', 'Dup', 'topic_y')`); err == nil {
		t.Fatalf("slug must be UNIQUE")
	}
}
