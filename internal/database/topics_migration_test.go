package database

import (
	"database/sql"
	"testing"
)

// topicsColumns returns the column names of table.
func topicsColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
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
	return got
}

// TestMigration11_TopicsTablesCreated verifies migration #11 creates the
// topics and topic_pointers tables with the spec'd columns and the
// session_key index (conduit-43vk).
func TestMigration11_TopicsTablesCreated(t *testing.T) {
	// At version 11 exactly: migration 12 adds topics.prompt_profile.
	db := openDBAtVersion(t, 11)

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

// TestMigration12_TopicsPromptProfile verifies migration #12 adds the
// nullable topics.prompt_profile column (conduit-331g): existing topics keep
// NULL (full prompt) and new rows default to NULL.
func TestMigration12_TopicsPromptProfile(t *testing.T) {
	db := openDBAtVersion(t, 11)
	if _, err := db.Exec(`INSERT INTO topics (slug, name, session_key) VALUES ('alpha', 'Alpha', 'topic_x')`); err != nil {
		t.Fatal(err)
	}
	if topicsColumns(t, db, "topics")["prompt_profile"] {
		t.Fatal("prompt_profile exists before migration 12")
	}

	if err := RunMigrations(db); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	if v, err := getCurrentVersion(db); err != nil || v != 12 || v != len(GetMigrations()) {
		t.Fatalf("version = %d, %v; want 12 (latest)", v, err)
	}
	if !topicsColumns(t, db, "topics")["prompt_profile"] {
		t.Fatal("prompt_profile column missing after migration 12")
	}

	if _, err := db.Exec(`INSERT INTO topics (slug, name, session_key) VALUES ('beta', 'Beta', 'topic_y')`); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := db.QueryRow(`SELECT COUNT(*) FROM topics WHERE prompt_profile IS NULL`).Scan(&nulls); err != nil || nulls != 2 {
		t.Fatalf("NULL profiles = %d, %v; want 2 (existing and new rows)", nulls, err)
	}
	if _, err := db.Exec(`UPDATE topics SET prompt_profile = '{"skills":["gog"]}' WHERE slug = 'alpha'`); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

// Migration 12 is idempotent: replaying it (as the maintenance retention
// test does when it rewinds schema_migrations) must not fail on the
// already-present column or touch stored profiles.
func TestMigration12_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO topics (slug, name, session_key, prompt_profile) VALUES ('alpha', 'Alpha', 'topic_x', '{"skills":["gog"]}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version >= 11`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("replay migrations 11-12: %v", err)
	}
	var profile string
	if err := db.QueryRow(`SELECT prompt_profile FROM topics WHERE slug = 'alpha'`).Scan(&profile); err != nil || profile != `{"skills":["gog"]}` {
		t.Fatalf("profile after replay = %q, %v", profile, err)
	}
}
