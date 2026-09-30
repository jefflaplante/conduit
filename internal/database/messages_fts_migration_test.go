package database

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// conduit-3dad: migration 9 replaces the full-scan messages_fts delete and
// update triggers with ones that find the row through the FTS index.

// openDBAtVersion opens a temp DB with migrations 1..version applied.
func openDBAtVersion(t *testing.T, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", BuildDSN(filepath.Join(t.TempDir(), "gateway.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureMigrationsTable(db); err != nil {
		t.Fatal(err)
	}
	for _, m := range GetMigrations() {
		if m.Version > version {
			break
		}
		if err := runMigration(db, m); err != nil {
			t.Fatalf("migration %d: %v", m.Version, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id) VALUES ('s1', 'u', 'c')`); err != nil {
		t.Fatal(err)
	}
	return db
}

type ftsRow struct{ ID, Session, Role, Content string }

func dumpRows(t *testing.T, db *sql.DB, q string) []ftsRow {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []ftsRow
	for rows.Next() {
		var r ftsRow
		if err := rows.Scan(&r.ID, &r.Session, &r.Role, &r.Content); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertFTSMirrorsMessages checks messages_fts holds exactly one row per
// message, with the message's current values.
func assertFTSMirrorsMessages(t *testing.T, db *sql.DB, step string) {
	t.Helper()
	msgs := dumpRows(t, db, `SELECT id, session_key, role, content FROM messages ORDER BY id, content`)
	fts := dumpRows(t, db, `SELECT message_id, session_key, role, content FROM messages_fts ORDER BY message_id, content`)
	if !reflect.DeepEqual(msgs, fts) {
		t.Fatalf("%s: messages_fts drifted\nmessages: %v\nfts:      %v", step, msgs, fts)
	}
}

// Ids that exercise the MATCH path, token-sharing ids, quoting, and the
// full-scan fallback (no ASCII letter or digit).
var triggerTestIDs = []string{
	"3f2a1b4c-0000-4000-8000-00000000000a",
	"3f2a1b4c-0000-4000-8000-00000000000b", // shares 4 of 5 tokens
	"a-b", "a b", "A.B",                    // same tokens, different ids
	`x"y`, `"`, `""quoted""`,
	"---", "éé", "",
	"msg*", "NEAR(a b)", "col:val",
}

func insertTestMessages(t *testing.T, db *sql.DB) {
	t.Helper()
	for i, id := range triggerTestIDs {
		if _, err := db.Exec(`INSERT INTO messages (id, session_key, role, content) VALUES (?, 's1', 'user', ?)`,
			id, fmt.Sprintf("hello number %d", i)); err != nil {
			t.Fatalf("insert %q: %v", id, err)
		}
	}
}

func triggerNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'messages' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	return names
}

func TestMigration9_UpgradesV8DatabaseAndKeepsFTSInSync(t *testing.T) {
	db := openDBAtVersion(t, 8)
	insertTestMessages(t, db) // indexed by the migration-5 insert trigger
	assertFTSMirrorsMessages(t, db, "before migration 9")

	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	v, err := getCurrentVersion(db)
	if err != nil || v != 9 {
		t.Fatalf("version = %d, %v; want 9", v, err)
	}
	want := []string{"messages_fts_delete", "messages_fts_delete_scan", "messages_fts_insert", "messages_fts_update", "messages_fts_update_scan"}
	if got := triggerNames(t, db); !reflect.DeepEqual(got, want) {
		t.Fatalf("triggers = %v, want %v", got, want)
	}
	assertFTSMirrorsMessages(t, db, "after migration 9 (no data change)")

	// Update every other message (content and id), then delete the rest,
	// one at a time, checking the index after each statement.
	for i, id := range triggerTestIDs {
		if i%2 == 0 {
			if _, err := db.Exec(`UPDATE messages SET content = ?, id = ? WHERE id = ?`, "edited "+id, id+"~", id); err != nil {
				t.Fatalf("update %q: %v", id, err)
			}
			assertFTSMirrorsMessages(t, db, fmt.Sprintf("after updating %q", id))
		}
	}
	for i, id := range triggerTestIDs {
		if i%2 == 0 {
			id += "~"
		}
		if _, err := db.Exec(`DELETE FROM messages WHERE id = ?`, id); err != nil {
			t.Fatalf("delete %q: %v", id, err)
		}
		assertFTSMirrorsMessages(t, db, fmt.Sprintf("after deleting %q", id))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_fts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("messages_fts rows = %d, %v; want 0", n, err)
	}
}

func TestMigration9_Idempotent(t *testing.T) {
	db := openDBAtVersion(t, 9)
	for i := 0; i < 2; i++ {
		if _, err := db.Exec(messagesFTSIndexedTriggersSQL); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	if err := RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	if got := len(triggerNames(t, db)); got != 5 {
		t.Fatalf("%d triggers after re-running migration 9, want 5", got)
	}
	insertTestMessages(t, db)
	if _, err := db.Exec(`DELETE FROM messages WHERE id IN ('a-b', '---')`); err != nil {
		t.Fatal(err)
	}
	assertFTSMirrorsMessages(t, db, "after re-run and delete")
}

// The indexed triggers' DELETE is answered by the FTS index (an "M" query
// plan), not by a full scan of messages_fts.
func TestMigration9_DeleteUsesFTSIndex(t *testing.T) {
	db := openDBAtVersion(t, 9)
	plan := func(q string) string {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			parts = append(parts, detail)
		}
		return strings.Join(parts, "; ")
	}
	indexed := plan(`DELETE FROM messages_fts WHERE message_id MATCH '"' || replace('a"b', '"', '""') || '"' AND message_id = 'a"b'`)
	scan := plan(`DELETE FROM messages_fts WHERE message_id = 'a"b'`)
	if !strings.Contains(indexed, ":M") || strings.Contains(scan, ":M") {
		t.Fatalf("plans: indexed %q, scan %q", indexed, scan)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = 'messages_fts_delete'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "message_id MATCH") {
		t.Fatalf("messages_fts_delete does not use MATCH:\n%s", sqlText)
	}
}
