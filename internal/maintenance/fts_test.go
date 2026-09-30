package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"conduit/internal/database"

	_ "modernc.org/sqlite"
)

// conduit-3dad: fts_rebuild repairs drift between messages and messages_fts.

type ftsTestRow struct {
	Rowid                      int64
	ID, Session, Role, Content string
}

func ftsSnapshot(t *testing.T, db *sql.DB) []ftsTestRow {
	t.Helper()
	rows, err := db.Query(`SELECT rowid, message_id, session_key, role, content FROM messages_fts ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []ftsTestRow
	for rows.Next() {
		var r ftsTestRow
		if err := rows.Scan(&r.Rowid, &r.ID, &r.Session, &r.Role, &r.Content); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// driftedGatewayDB returns a gateway.db with six messages and a
// messages_fts with 2 orphaned, 1 outdated and 1 duplicate row (4 stale)
// and 3 messages without an up-to-date row (m2 outdated, m3 and m4
// missing).
func driftedGatewayDB(t *testing.T) *sql.DB {
	t.Helper()
	db, _ := openGatewayDB(t)
	addSession(t, db, "telegram_1_aaaa", testNow)
	for i := 1; i <= 6; i++ {
		mustExec(t, db, `INSERT INTO messages (id, session_key, role, content) VALUES (?, 'telegram_1_aaaa', 'user', ?)`,
			fmt.Sprintf("m%d", i), fmt.Sprintf("apple banana %d", i))
	}
	// Drift the index directly (the triggers only fire for messages).
	mustExec(t, db, `INSERT INTO messages_fts(message_id, session_key, role, content) VALUES
		('gone-1', 'telegram_1_aaaa', 'user', 'orphan one'),
		('gone-2', 'cron_x', 'assistant', 'orphan two')`)
	mustExec(t, db, `INSERT INTO messages_fts(message_id, session_key, role, content) VALUES ('m1', 'telegram_1_aaaa', 'user', 'apple banana 1')`)
	mustExec(t, db, `UPDATE messages_fts SET content = 'stale cherry' WHERE message_id = 'm2'`)
	mustExec(t, db, `DELETE FROM messages_fts WHERE message_id IN ('m3', 'm4')`)
	return db
}

func assertInSync(t *testing.T, db *sql.DB) {
	t.Helper()
	rep, err := PlanFTSRepair(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.InSync() || rep.IndexRows != rep.Messages {
		t.Fatalf("not in sync after repair: %+v", rep)
	}
	got := map[string]string{}
	for _, r := range ftsSnapshot(t, db) {
		got[r.ID] = r.Content
	}
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("m%d", i)
		if want := fmt.Sprintf("apple banana %d", i); got[id] != want {
			t.Errorf("index row %s content = %q, want %q", id, got[id], want)
		}
	}
}

func ftsTask(db *sql.DB, dryRun bool) *FTSRebuildTask {
	return NewFTSRebuildTask(db, DatabaseConfig{DryRun: dryRun}, log.New(io.Discard, "", 0))
}

func TestFTSRebuild_DryRunCountsAndChangesNothing(t *testing.T) {
	db := driftedGatewayDB(t)
	before := ftsSnapshot(t, db)

	res := ftsTask(db, true).Execute(context.Background())
	if !res.Success {
		t.Fatalf("dry run failed: %s %v", res.Message, res.Error)
	}
	rep, ok := res.Details.(*FTSRepairReport)
	if !ok {
		t.Fatalf("details = %T", res.Details)
	}
	want := FTSRepairReport{DryRun: true, Messages: 6, IndexRows: 7, Orphaned: 2, Outdated: 1, Duplicates: 1, Stale: 4, Missing: 3}
	got := *rep
	got.staleRowids, got.missingIDs = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dry-run report = %+v\nwant %+v", got, want)
	}
	if !strings.Contains(res.Message, "Dry run: would delete 4 and insert 3") {
		t.Errorf("message = %q", res.Message)
	}
	if after := ftsSnapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("dry run changed messages_fts:\nbefore %v\nafter  %v", before, after)
	}
}

func TestFTSRebuild_RepairsDrift(t *testing.T) {
	db := driftedGatewayDB(t)

	res := ftsTask(db, false).Execute(context.Background())
	if !res.Success {
		t.Fatalf("repair failed: %s %v", res.Message, res.Error)
	}
	rep := res.Details.(*FTSRepairReport)
	if rep.DryRun || rep.Deleted != 4 || rep.Inserted != 3 || res.RecordsProcessed != 7 {
		t.Fatalf("repair report = %+v, records %d", rep, res.RecordsProcessed)
	}
	assertInSync(t, db)

	// Searching finds the re-indexed text and no longer the stale text.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'cherry OR orphan'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stale text still indexed: %d rows, %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'apple'`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("MATCH apple = %d rows, %v; want 6", n, err)
	}

	// A second run finds nothing to do.
	res = ftsTask(db, false).Execute(context.Background())
	if !res.Success || res.RecordsProcessed != 0 || !strings.Contains(res.Message, "in sync") {
		t.Fatalf("second run: %+v", res)
	}
}

// Messages outdated in more than one index row, and a message indexed
// twice with outdated text only: one fresh row each afterwards.
func TestFTSRebuild_MultipleOutdatedRows(t *testing.T) {
	db, _ := openGatewayDB(t)
	addSession(t, db, "tui_1_bbbb", testNow)
	mustExec(t, db, `INSERT INTO messages (id, session_key, role, content) VALUES ('only', 'tui_1_bbbb', 'user', 'fresh')`)
	mustExec(t, db, `UPDATE messages_fts SET content = 'old' WHERE message_id = 'only'`)
	mustExec(t, db, `INSERT INTO messages_fts(message_id, session_key, role, content) VALUES ('only', 'tui_1_bbbb', 'assistant', 'fresh')`)

	rep, err := PlanFTSRepair(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outdated != 2 || rep.Duplicates != 0 || rep.Missing != 1 {
		t.Fatalf("plan = %+v", rep)
	}
	if _, err := RepairFTS(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	rows := ftsSnapshot(t, db)
	if len(rows) != 1 || rows[0].Content != "fresh" || rows[0].Role != "user" {
		t.Fatalf("index = %+v", rows)
	}
}

func TestFTSRebuild_NoFTSTable(t *testing.T) {
	db, err := sql.Open("sqlite", database.BuildDSN(filepath.Join(t.TempDir(), "old.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mustExec(t, db, `CREATE TABLE messages (id TEXT PRIMARY KEY, session_key TEXT, role TEXT, content TEXT)`)
	for _, dry := range []bool{true, false} {
		res := ftsTask(db, dry).Execute(context.Background())
		if !res.Success || !strings.Contains(res.Message, "No messages_fts table") {
			t.Fatalf("dry=%v: %+v", dry, res)
		}
	}
}

// The selection is one pass over messages_fts with a primary-key lookup
// into messages, never a scan of messages per index row (or vice versa).
func TestFTSRebuild_QueryPlansUseMessagesPK(t *testing.T) {
	db, _ := openGatewayDB(t)
	for name, q := range map[string]string{"stale": ftsStaleQuery, "missing": ftsMissingQuery} {
		rows, err := db.Query(`EXPLAIN QUERY PLAN ` + q)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		p := strings.Join(plan, "; ")
		if !strings.Contains(p, "SEARCH m USING INDEX sqlite_autoindex_messages_1") {
			t.Errorf("%s query does not look messages up by primary key: %s", name, p)
		}
		if strings.Count(p, "SCAN f VIRTUAL TABLE") != 1 {
			t.Errorf("%s query scans messages_fts other than once: %s", name, p)
		}
	}
}
