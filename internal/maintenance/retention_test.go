package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/database"
	"conduit/internal/sessions"

	_ "modernc.org/sqlite"
)

// conduit-2cxu: session retention prunes automated sessions only.

var testNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// openGatewayDB opens a temp gateway.db with the real migrations and DSN.
func openGatewayDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.db")
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatal(err)
	}
	return db, path
}

// goString renders t the way the driver's default time binding stored
// messages.timestamp: time.Time.String() with a monotonic reading.
func goString(t time.Time) string {
	return t.String() + " m=+2227.791803474"
}

func daysAgo(d int) time.Time { return testNow.AddDate(0, 0, -d) }

func addSession(t *testing.T, db *sql.DB, key string, updated time.Time) {
	t.Helper()
	addSessionRaw(t, db, key, sessions.FormatUpdatedAt(updated))
}

func addSessionRaw(t *testing.T, db *sql.DB, key, updatedAt string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id, created_at, updated_at) VALUES (?, 'u', 'c', ?, ?)`,
		key, updatedAt, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
}

var msgSeq int

func addMessage(t *testing.T, db *sql.DB, key, content, ts string) {
	t.Helper()
	msgSeq++
	_, err := db.Exec(`INSERT INTO messages (id, session_key, role, content, timestamp) VALUES (?, ?, 'user', ?, ?)`,
		fmt.Sprintf("m%d", msgSeq), key, content, ts)
	if err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func sessionExists(t *testing.T, db *sql.DB, key string) bool {
	return count(t, db, `SELECT COUNT(*) FROM sessions WHERE key = ?`, key) == 1
}

func defaultPolicy() RetentionPolicy {
	return RetentionPolicy{RetentionDays: 30}
}

// seed builds a realistic mix; returns the keys expected to be pruned.
func seed(t *testing.T, db *sql.DB) (pruned, kept []string) {
	t.Helper()
	// Old automated sessions with messages.
	addSession(t, db, "cron_43e3b7bb_1770937740057151230_cron_2a54cb2d", daysAgo(90))
	addMessage(t, db, "cron_43e3b7bb_1770937740057151230_cron_2a54cb2d", "zebracron report", goString(daysAgo(90)))
	addMessage(t, db, "cron_43e3b7bb_1770937740057151230_cron_2a54cb2d", "zebracron answer", goString(daysAgo(90)))
	addSession(t, db, "subagent_1771029792753060494_subagent_50ed2c01", daysAgo(45))
	addMessage(t, db, "subagent_1771029792753060494_subagent_50ed2c01", "subtask", goString(daysAgo(45)))
	addSession(t, db, "test_email-fast-check_58932596", daysAgo(31))
	addMessage(t, db, "test_email-fast-check_58932596", "check mail", goString(daysAgo(31)))
	// Old empty automated sessions.
	addSession(t, db, "heartbeat_1771343100002338405_heartbeat_b1bf6b44", daysAgo(200))
	addSession(t, db, "cron_x_1_cron_aaaa0001", daysAgo(60))
	pruned = []string{
		"cron_43e3b7bb_1770937740057151230_cron_2a54cb2d",
		"subagent_1771029792753060494_subagent_50ed2c01",
		"test_email-fast-check_58932596",
		"heartbeat_1771343100002338405_heartbeat_b1bf6b44",
		"cron_x_1_cron_aaaa0001",
	}

	// Protected, very old.
	addSession(t, db, "telegram_1000000001_caaaffa7", daysAgo(400))
	addMessage(t, db, "telegram_1000000001_caaaffa7", "zebratelegram hello", goString(daysAgo(400)))
	addSession(t, db, "tui_owner_client_0a1b2c3d", daysAgo(400))
	addMessage(t, db, "tui_owner_client_0a1b2c3d", "tui hello", goString(daysAgo(400)))
	addSession(t, db, "tui_jeff_empty_1", daysAgo(400)) // empty protected
	// Unknown prefixes and near-miss prefixes, very old.
	addSession(t, db, "ws_user_abcd", daysAgo(400))
	addMessage(t, db, "ws_user_abcd", "ws hello", goString(daysAgo(400)))
	addSession(t, db, "nounderscore", daysAgo(400))
	addSession(t, db, "cronjob_1_x", daysAgo(400))
	addSession(t, db, "testing_1_x", daysAgo(400))
	addSession(t, db, "CRON_upper_1", daysAgo(400))
	// Recent automated.
	addSession(t, db, "heartbeat_recent_heartbeat_1", daysAgo(1))
	// Old updated_at but a recent message: still active.
	addSession(t, db, "test_alerts-flush_6d81874f", daysAgo(60))
	addMessage(t, db, "test_alerts-flush_6d81874f", "flush", goString(daysAgo(2)))
	kept = []string{
		"telegram_1000000001_caaaffa7", "tui_owner_client_0a1b2c3d", "tui_jeff_empty_1",
		"ws_user_abcd", "nounderscore", "cronjob_1_x", "testing_1_x", "CRON_upper_1",
		"heartbeat_recent_heartbeat_1", "test_alerts-flush_6d81874f",
	}
	return pruned, kept
}

func TestPrune_SelectsOnlyOldAutomatedSessions(t *testing.T) {
	db, _ := openGatewayDB(t)
	pruned, kept := seed(t, db)
	ctx := context.Background()

	rep, err := PlanPrune(ctx, db, defaultPolicy(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if rep.PruneSessions != len(pruned) || rep.PruneMessages != 4 {
		t.Fatalf("plan = %d sessions / %d messages, want %d / 4", rep.PruneSessions, rep.PruneMessages, len(pruned))
	}
	// telegram(1 old msg) + tui(1) + ws(1) = 3 old protected messages; 8 old
	// non-prunable sessions.
	if rep.KeptProtectedSessions != 8 || rep.KeptProtectedMessages != 3 {
		t.Fatalf("kept protected = %d sessions / %d messages, want 8 / 3 (%+v)",
			rep.KeptProtectedSessions, rep.KeptProtectedMessages, rep.KeptProtected)
	}
	if rep.KeptRecent != 2 {
		t.Fatalf("kept recent = %d, want 2", rep.KeptRecent)
	}
	byPrefix := map[string]PrefixCount{}
	for _, c := range rep.Prune {
		byPrefix[c.Prefix] = c
	}
	if c := byPrefix["cron_"]; c.Sessions != 2 || c.EmptySessions != 1 || c.Messages != 2 {
		t.Fatalf("cron_ plan = %+v", c)
	}
	if c := byPrefix["heartbeat_"]; c.Sessions != 1 || c.EmptySessions != 1 || !c.Oldest.Equal(daysAgo(200)) {
		t.Fatalf("heartbeat_ plan = %+v", c)
	}

	if err := ExecutePrune(ctx, db, rep, 0); err != nil {
		t.Fatal(err)
	}
	if rep.SessionsDeleted != len(pruned) || rep.MessagesDeleted != 4 || rep.SkippedChanged != 0 {
		t.Fatalf("deleted %d sessions / %d messages (skipped %d)", rep.SessionsDeleted, rep.MessagesDeleted, rep.SkippedChanged)
	}
	for _, k := range pruned {
		if sessionExists(t, db, k) {
			t.Errorf("session %s not deleted", k)
		}
	}
	for _, k := range kept {
		if !sessionExists(t, db, k) {
			t.Errorf("session %s deleted, must be kept", k)
		}
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messages m LEFT JOIN sessions s ON s.key = m.session_key WHERE s.key IS NULL`); n != 0 {
		t.Fatalf("%d orphan messages", n)
	}
}

func TestPrune_ProtectedNeverDeletedWhateverThePolicy(t *testing.T) {
	db, _ := openGatewayDB(t)
	addSession(t, db, "telegram_1_a", daysAgo(3650))
	addSession(t, db, "tui_x_b", daysAgo(3650))
	for _, bad := range [][]string{{"telegram"}, {"tui_"}, {"Telegram"}, {"telegram_1"}, {"TUI"}, {"cron*"}, {"cron%"}, {""}, {"_"}} {
		if _, err := PlanPrune(context.Background(), db, RetentionPolicy{RetentionDays: 1, PrunablePrefixes: bad}, testNow); err == nil {
			t.Errorf("policy %q accepted", bad)
		}
	}
	// "t" means "t_": it is not a prefix of "telegram_..." keys.
	rep, err := PlanPrune(context.Background(), db, RetentionPolicy{RetentionDays: 1, PrunablePrefixes: []string{"t", "tu"}}, testNow)
	if err != nil || rep.PruneSessions != 0 {
		t.Fatalf("plan with t/tu = %v, %+v", err, rep)
	}
	// Even a hand-built candidate list cannot delete them.
	rep = &PruneReport{PrunablePrefixes: []string{"telegram_"}, candidates: []pruneCandidate{{key: "telegram_1_a"}}}
	if err := ExecutePrune(context.Background(), db, rep, 10); err == nil {
		t.Fatal("ExecutePrune deleted a protected session")
	}
	if !sessionExists(t, db, "telegram_1_a") || !sessionExists(t, db, "tui_x_b") {
		t.Fatal("protected session deleted")
	}
}

func TestPrune_DryRunMakesNoChangesAndMatchesRealRun(t *testing.T) {
	db, path := openGatewayDB(t)
	seed(t, db)
	logger := log.New(io.Discard, "", 0)

	before := count(t, db, `SELECT COUNT(*) FROM sessions`) + count(t, db, `SELECT COUNT(*) FROM messages`)
	cfg := DefaultConfig().Sessions
	cfg.DryRun = true
	dry := NewSessionCleanupTask(db, path, cfg, logger)
	dry.now = func() time.Time { return testNow }
	res := dry.Execute(context.Background())
	if !res.Success {
		t.Fatalf("dry run failed: %+v", res)
	}
	after := count(t, db, `SELECT COUNT(*) FROM sessions`) + count(t, db, `SELECT COUNT(*) FROM messages`)
	if before != after {
		t.Fatalf("dry run changed row counts %d -> %d", before, after)
	}
	if backups, _ := filepath.Glob(path + ".backup.*"); len(backups) != 0 {
		t.Fatalf("dry run wrote a backup: %v", backups)
	}
	dryRep := res.Details.(*PruneReport)

	cfg.DryRun = false
	cfg.BatchSize = 2
	real := NewSessionCleanupTask(db, path, cfg, logger)
	real.now = func() time.Time { return testNow }
	res = real.Execute(context.Background())
	if !res.Success {
		t.Fatalf("run failed: %+v", res)
	}
	rep := res.Details.(*PruneReport)
	if rep.SessionsDeleted != dryRep.PruneSessions || rep.MessagesDeleted != dryRep.PruneMessages {
		t.Fatalf("real run deleted %d/%d, dry run said %d/%d",
			rep.SessionsDeleted, rep.MessagesDeleted, dryRep.PruneSessions, dryRep.PruneMessages)
	}
	// 5 sessions in batches of 2.
	if rep.Batches != 3 {
		t.Fatalf("batches = %d, want 3", rep.Batches)
	}

	// Backup: 0600, a readable DB holding the pre-prune rows.
	if rep.BackupPath == "" || !strings.HasPrefix(filepath.Base(rep.BackupPath), "gateway.db.backup.") {
		t.Fatalf("backup path = %q", rep.BackupPath)
	}
	st, err := os.Stat(rep.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", st.Mode().Perm())
	}
	bdb, err := sql.Open("sqlite", "file:"+rep.BackupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer bdb.Close()
	if n := count(t, bdb, `SELECT COUNT(*) FROM sessions`); n != count(t, db, `SELECT COUNT(*) FROM sessions`)+rep.SessionsDeleted {
		t.Fatalf("backup has %d sessions", n)
	}
}

func TestPrune_NoBackupFlag(t *testing.T) {
	db, path := openGatewayDB(t)
	seed(t, db)
	cfg := DefaultConfig().Sessions
	cfg.BackupBeforePrune = false
	task := NewSessionCleanupTask(db, path, cfg, log.New(io.Discard, "", 0))
	task.now = func() time.Time { return testNow }
	if res := task.Execute(context.Background()); !res.Success || res.Details.(*PruneReport).BackupPath != "" {
		t.Fatalf("res = %+v", res)
	}
	if backups, _ := filepath.Glob(path + ".backup.*"); len(backups) != 0 {
		t.Fatalf("backup written despite BackupBeforePrune=false: %v", backups)
	}
}

func TestPrune_RelatedTablesAndFTS(t *testing.T) {
	db, _ := openGatewayDB(t)
	seed(t, db)
	// Tables other components create lazily.
	if _, err := db.Exec(`CREATE TABLE session_summaries (id INTEGER PRIMARY KEY, session_key TEXT NOT NULL, summary TEXT);
		CREATE TABLE claude_code_sessions (conduit_session_id TEXT PRIMARY KEY, cc_session_id TEXT NOT NULL);
		INSERT INTO session_summaries (session_key, summary) VALUES
			('cron_43e3b7bb_1770937740057151230_cron_2a54cb2d', 's'), ('telegram_1000000001_caaaffa7', 's');
		INSERT INTO claude_code_sessions VALUES
			('subagent_1771029792753060494_subagent_50ed2c01', 'cc1'), ('tui_owner_client_0a1b2c3d', 'cc2');`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'zebracron'`); n != 2 {
		t.Fatalf("fts precondition: %d", n)
	}

	rep, err := PlanPrune(context.Background(), db, defaultPolicy(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if err := ExecutePrune(context.Background(), db, rep, 2); err != nil {
		t.Fatal(err)
	}
	if rep.SummariesDeleted != 1 || rep.MappingsDeleted != 1 {
		t.Fatalf("summaries/mappings deleted = %d/%d, want 1/1", rep.SummariesDeleted, rep.MappingsDeleted)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM session_summaries WHERE session_key LIKE 'telegram%'`); n != 1 {
		t.Fatal("protected summary deleted")
	}
	if n := count(t, db, `SELECT COUNT(*) FROM claude_code_sessions`); n != 1 {
		t.Fatalf("claude_code_sessions rows = %d, want 1", n)
	}
	// FTS stays in step with messages (triggers).
	if f, m := count(t, db, `SELECT COUNT(*) FROM messages_fts`), count(t, db, `SELECT COUNT(*) FROM messages`); f != m {
		t.Fatalf("messages_fts has %d rows, messages %d", f, m)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'zebracron'`); n != 0 {
		t.Fatalf("pruned messages still searchable: %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'zebratelegram'`); n != 1 {
		t.Fatalf("protected message missing from FTS: %d", n)
	}
}

func TestPrune_SearchIndexMirror(t *testing.T) {
	db, _ := openGatewayDB(t)
	seed(t, db)
	sdb, err := sql.Open("sqlite", database.BuildDSN(filepath.Join(t.TempDir(), "gateway.search.db")))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE VIRTUAL TABLE messages_fts USING fts5(message_id, session_key, role, content, tokenize='porter unicode61')`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id, session_key, role, content FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, k, r, c string
		if err := rows.Scan(&id, &k, &r, &c); err != nil {
			t.Fatal(err)
		}
		if _, err := sdb.Exec(`INSERT INTO messages_fts VALUES (?, ?, ?, ?)`, id, k, r, c); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()

	cfg := DefaultConfig().Sessions
	cfg.BackupBeforePrune = false
	task := NewSessionCleanupTask(db, "", cfg, log.New(io.Discard, "", 0))
	task.now = func() time.Time { return testNow }
	task.SetSearchDB(sdb)
	res := task.Execute(context.Background())
	if !res.Success {
		t.Fatalf("%+v", res)
	}
	if rep := res.Details.(*PruneReport); rep.SearchIndexDeleted != 4 {
		t.Fatalf("search index rows deleted = %d, want 4", rep.SearchIndexDeleted)
	}
	if f, m := count(t, sdb, `SELECT COUNT(*) FROM messages_fts`), count(t, db, `SELECT COUNT(*) FROM messages`); f != m {
		t.Fatalf("search.db messages_fts %d rows, gateway messages %d", f, m)
	}
}

func TestPrune_SkipsSessionsActiveSincePlan(t *testing.T) {
	db, _ := openGatewayDB(t)
	seed(t, db)
	rep, err := PlanPrune(context.Background(), db, defaultPolicy(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	// A message arrives for one candidate, another is touched.
	addMessage(t, db, "test_email-fast-check_58932596", "new", goString(testNow))
	if _, err := db.Exec(`UPDATE sessions SET updated_at = ? WHERE key = 'cron_x_1_cron_aaaa0001'`, sessions.FormatUpdatedAt(testNow)); err != nil {
		t.Fatal(err)
	}
	if err := ExecutePrune(context.Background(), db, rep, 100); err != nil {
		t.Fatal(err)
	}
	if rep.SkippedChanged != 2 || rep.SessionsDeleted != 3 {
		t.Fatalf("skipped %d deleted %d, want 2 / 3", rep.SkippedChanged, rep.SessionsDeleted)
	}
	if !sessionExists(t, db, "test_email-fast-check_58932596") || !sessionExists(t, db, "cron_x_1_cron_aaaa0001") {
		t.Fatal("a session active since the plan was deleted")
	}
}

// Stored timestamps come in several text formats; the cutoff comparison
// must be chronological for all of them.
func TestPrune_TimestampFormats(t *testing.T) {
	db, _ := openGatewayDB(t)
	cutoff := testNow.AddDate(0, 0, -30) // 2026-08-30 12:00 UTC
	mst := time.FixedZone("MST", -7*3600)
	cest := time.FixedZone("CEST", 2*3600)

	cases := []struct {
		key     string
		updated string // raw updated_at
		msgTS   string // "" = no message
		prune   bool
	}{
		// Canonical updated_at, empty.
		{"cron_a_1", sessions.FormatUpdatedAt(cutoff.Add(-time.Second)), "", true},
		{"cron_a_2", sessions.FormatUpdatedAt(cutoff.Add(time.Second)), "", false},
		// Legacy updated_at: Go String() with monotonic suffix, UTC.
		{"cron_b_1", goString(cutoff.Add(-time.Minute)), "", true},
		// Go String() in a zone west of UTC: text "2026-08-30 05:30" sorts
		// before the cutoff text, but it is 12:30 UTC, after the cutoff.
		{"cron_b_2", goString(cutoff.Add(30 * time.Minute).In(mst)), "", false},
		// East of UTC: text "2026-08-30 13:59:59" sorts after the cutoff
		// text, but it is 11:59:59 UTC, before the cutoff.
		{"cron_b_3", goString(cutoff.Add(-time.Second).In(cest)), "", true},
		// RFC 3339 and CURRENT_TIMESTAMP forms.
		{"cron_c_1", cutoff.Add(-time.Hour).Format(time.RFC3339Nano), "", true},
		{"cron_c_2", cutoff.Add(time.Hour).In(cest).Format(time.RFC3339), "", false},
		{"cron_c_3", cutoff.Add(-time.Hour).Format("2006-01-02 15:04:05"), "", true},
		// Messages decide when newer than updated_at.
		{"cron_d_1", sessions.FormatUpdatedAt(daysAgo(90)), goString(cutoff.Add(time.Minute).In(mst)), false},
		{"cron_d_2", sessions.FormatUpdatedAt(daysAgo(90)), goString(cutoff.Add(-time.Minute).In(cest)), true},
		{"cron_d_3", sessions.FormatUpdatedAt(daysAgo(90)), cutoff.Add(time.Minute).Format(time.RFC3339), false},
		// Unparseable timestamps are kept.
		{"cron_e_1", "garbage", "", false},
		{"cron_e_2", sessions.FormatUpdatedAt(daysAgo(90)), "not a time", false},
	}
	for _, c := range cases {
		addSessionRaw(t, db, c.key, c.updated)
		if c.msgTS != "" {
			addMessage(t, db, c.key, "x", c.msgTS)
		}
	}
	rep, err := PlanPrune(context.Background(), db, defaultPolicy(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if rep.KeptUnparseable != 2 {
		t.Errorf("kept unparseable = %d, want 2", rep.KeptUnparseable)
	}
	if err := ExecutePrune(context.Background(), db, rep, 0); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if gone := !sessionExists(t, db, c.key); gone != c.prune {
			t.Errorf("%s (updated %q, msg %q): pruned=%v, want %v", c.key, c.updated, c.msgTS, gone, c.prune)
		}
	}
}

func TestPrune_RetentionMustBePositive(t *testing.T) {
	db, _ := openGatewayDB(t)
	if _, err := PlanPrune(context.Background(), db, RetentionPolicy{RetentionDays: 0}, testNow); err == nil {
		t.Fatal("retention 0 accepted")
	}
}

func TestBackupDatabase_Mode0600AndUnique(t *testing.T) {
	db, path := openGatewayDB(t)
	addSession(t, db, "cron_a", testNow)
	dir := filepath.Join(t.TempDir(), "backups")
	p, err := BackupDatabase(context.Background(), db, path, dir, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != dir {
		t.Fatalf("backup in %s, want %s", filepath.Dir(p), dir)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v mode %v", err, st.Mode().Perm())
	}
	// Same timestamp again must not overwrite.
	if _, err := BackupDatabase(context.Background(), db, path, dir, testNow); err == nil {
		t.Fatal("second backup with the same name overwrote the first")
	}
}
