package maintenance

import (
	"context"
	"database/sql"
	"io"
	"log"
	"testing"
	"time"

	"conduit/internal/sessions"

	_ "modernc.org/sqlite"
)

// conduit-31jg.73: updated_at is stored in the canonical UTC text layout
// (sessions.FormatUpdatedAt); the cleanup cutoff must be rendered the same
// way, whatever zone the cutoff time.Time is in, or the lexicographic
// comparison deletes the wrong rows.
func TestCleanupSessions_CanonicalUpdatedAtCutoff(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	setupTestTables(t, db)

	// Cutoff in a zone far from UTC.
	cutoff := time.Date(2026, 3, 10, 12, 0, 0, 0, time.FixedZone("AEST", 10*3600))
	older := sessions.FormatUpdatedAt(cutoff.Add(-time.Minute))
	newer := sessions.FormatUpdatedAt(cutoff.Add(time.Minute))
	if _, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id, updated_at) VALUES
		('older', 'u', 'c', ?), ('newer', 'u', 'c', ?)`, older, newer); err != nil {
		t.Fatal(err)
	}

	task := NewSessionCleanupTask(db, SessionConfig{CleanupEnabled: true}, log.New(io.Discard, "", 0))
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	res := task.cleanupSessions(context.Background(), tx, cutoff)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.RecordsProcessed != 1 {
		t.Fatalf("cleanupSessions = %+v, want 1 row deleted", res)
	}
	var key string
	if err := db.QueryRow(`SELECT key FROM sessions`).Scan(&key); err != nil || key != "newer" {
		t.Fatalf("remaining session = %q (%v), want \"newer\"", key, err)
	}
}
