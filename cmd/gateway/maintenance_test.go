package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/database"
	"conduit/internal/sessions"
)

// captureMaintenanceStdout runs fn with os.Stdout redirected.
func captureMaintenanceStdout(t *testing.T, fn func() error) string {
	t.Helper()
	out, runErr := captureMaintenanceStdoutErr(t, fn)
	if runErr != nil {
		t.Fatalf("command failed: %v", runErr)
	}
	return out
}

func captureMaintenanceStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()
	runErr := fn()
	os.Stdout = old
	_ = w.Close()
	<-done
	return buf.String(), runErr
}

// maintenanceFixture is a gateway config + database with automated and
// protected sessions, all 90 days old.
type maintenanceFixture struct {
	cfgPath, dbPath string
}

func newMaintenanceFixture(t *testing.T, mc config.MaintenanceConfig) maintenanceFixture {
	t.Helper()
	dir := t.TempDir()
	f := maintenanceFixture{cfgPath: filepath.Join(dir, "config.json"), dbPath: filepath.Join(dir, "gateway.db")}
	cfg := config.Default()
	cfg.Database.Path = f.dbPath
	cfg.Maintenance = mc
	if err := cfg.Save(f.cfgPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database.BuildDSN(f.dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ConfigureDatabase(db); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -90)
	for i, key := range []string{
		"cron_job_1_cron_aaaa", "heartbeat_1_heartbeat_bbbb", "subagent_1_subagent_cccc", "test_check_dddd",
		"telegram_123_eeee", "tui_jeff_ffff", "ws_user_gggg",
	} {
		if _, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id, updated_at) VALUES (?, 'u', 'c', ?)`,
			key, sessions.FormatUpdatedAt(old)); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 { // some sessions have a message, stored the way the driver wrote them
			if _, err := db.Exec(`INSERT INTO messages (id, session_key, role, content, timestamp) VALUES (?, ?, 'user', 'hi', ?)`,
				key+"-m", key, old.String()); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

// useMaintenanceFlags points the global --config/--database at the fixture
// and resets the maintenance flags afterwards.
func useMaintenanceFlags(t *testing.T, cfg, db string) {
	t.Helper()
	origCfg, origDB := cfgFile, dbPath
	cfgFile = cfg
	flag := rootCmd.PersistentFlags().Lookup("database")
	if db != "" {
		dbPath = db
		flag.Changed = true
	}
	t.Cleanup(func() {
		cfgFile, dbPath = origCfg, origDB
		flag.Changed = false
		maintenanceJSONOutput, maintenanceDryRun, maintenanceNoBackup = false, false, false
		maintenanceRetentionDays = 0
	})
}

func sessionKeys(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", database.BuildDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT key FROM sessions ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	return keys
}

// conduit-3kgo: status no longer claims a scheduler or a schedule exists.
// conduit-2cxu: it reports the database, per-prefix counts and the plan.
func TestMaintenanceStatus_Honest(t *testing.T) {
	f := newMaintenanceFixture(t, config.MaintenanceConfig{})
	useMaintenanceFlags(t, f.cfgPath, "")
	for _, jsonOut := range []bool{false, true} {
		maintenanceJSONOutput = jsonOut
		out := captureMaintenanceStdout(t, func() error { return showMaintenanceStatus(nil, nil) })
		for _, bad := range []string{"running scheduler", "Schedule", "schedule\""} {
			if strings.Contains(out, bad) {
				t.Errorf("json=%v: status output mentions %q:\n%s", jsonOut, bad, out)
			}
		}
		if !strings.Contains(out, "no background schedule") || !strings.Contains(out, f.dbPath) {
			t.Errorf("json=%v: status output lacks the no-schedule note or db path:\n%s", jsonOut, out)
		}
		if jsonOut {
			var v map[string]interface{}
			if err := json.Unmarshal([]byte(out), &v); err != nil {
				t.Errorf("status --json is not JSON: %v", err)
			}
		} else {
			for _, want := range []string{"telegram_", "Would delete:", "Kept although older than the cutoff (not prunable): 3 sessions, 2 messages"} {
				if !strings.Contains(out, want) {
					t.Errorf("status lacks %q:\n%s", want, out)
				}
			}
		}
	}
	maintenanceJSONOutput = false
	if got := len(sessionKeys(t, f.dbPath)); got != 7 {
		t.Fatalf("status changed the database: %d sessions", got)
	}

	out := captureMaintenanceStdout(t, func() error { return showMaintenanceConfig(nil, nil) })
	if strings.Contains(out, "Maintenance Window") || strings.Contains(out, "Schedule") {
		t.Errorf("config output still shows window/schedule:\n%s", out)
	}
}

// conduit-2cxu: `maintenance run` opens the configured database (it used to
// fail with "database initialization not implemented"), a dry run changes
// nothing, and a real run deletes only automated sessions after a backup.
func TestMaintenanceRun_PrunesAutomatedOnly(t *testing.T) {
	f := newMaintenanceFixture(t, config.MaintenanceConfig{RetentionDays: 30, BatchSize: 2})
	useMaintenanceFlags(t, f.cfgPath, "")

	maintenanceDryRun = true
	out := captureMaintenanceStdout(t, func() error { return runMaintenanceTasks(nil, nil) })
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "would delete 4 sessions and 2 messages") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if got := len(sessionKeys(t, f.dbPath)); got != 7 {
		t.Fatalf("dry run deleted sessions: %d left", got)
	}

	maintenanceDryRun = false
	out = captureMaintenanceStdout(t, func() error { return runMaintenanceTasks(nil, nil) })
	if !strings.Contains(out, "Deleted 4 sessions and 2 messages in 2 batches") || !strings.Contains(out, "Backup: ") {
		t.Fatalf("run output:\n%s", out)
	}
	want := []string{"telegram_123_eeee", "tui_jeff_ffff", "ws_user_gggg"}
	if got := sessionKeys(t, f.dbPath); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sessions left = %v, want %v", got, want)
	}
	backups, _ := filepath.Glob(f.dbPath + ".backup.*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if st, err := os.Stat(backups[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("backup %v mode %v", err, st.Mode().Perm())
	}
}

// --database overrides the config's database.path, and a config file that
// is missing is fine when --database is given.
func TestMaintenanceRun_DatabaseFlagAndNoBackup(t *testing.T) {
	f := newMaintenanceFixture(t, config.MaintenanceConfig{})
	useMaintenanceFlags(t, filepath.Join(t.TempDir(), "missing.json"), f.dbPath)
	maintenanceNoBackup = true
	maintenanceJSONOutput = true
	out := captureMaintenanceStdout(t, func() error { return runSpecificMaintenanceTask(nil, []string{"session_cleanup"}) })
	var v struct {
		Database string `json:"database"`
		Results  []struct {
			Task string `json:"task"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Database != f.dbPath || len(v.Results) != 1 {
		t.Fatalf("json = %s (%v)", out, err)
	}
	if b, _ := filepath.Glob(f.dbPath + ".backup.*"); len(b) != 0 {
		t.Fatalf("--no-backup wrote %v", b)
	}
	if got := len(sessionKeys(t, f.dbPath)); got != 3 {
		t.Fatalf("%d sessions left, want 3", got)
	}
}

func TestMaintenance_RefusesBadConfigAndMissingDB(t *testing.T) {
	f := newMaintenanceFixture(t, config.MaintenanceConfig{})
	// A config whose prunable prefixes reach Telegram does not load.
	raw, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["maintenance"] = map[string]any{"prunable_prefixes": []string{"cron", "telegram"}}
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(f.cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	useMaintenanceFlags(t, f.cfgPath, "")
	if _, err := captureMaintenanceStdoutErr(t, func() error { return runMaintenanceTasks(nil, nil) }); err == nil || !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("err = %v, want refusal naming telegram", err)
	}
	if got := len(sessionKeys(t, f.dbPath)); got != 7 {
		t.Fatalf("%d sessions left", got)
	}

	// A missing database is an error, not a fresh empty file.
	missing := filepath.Join(t.TempDir(), "nope.db")
	if _, err := initDatabase(missing); err == nil {
		t.Fatal("opened a missing database")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("initDatabase created the database file")
	}

	maintenanceRetentionDays = -1
	if _, _, err := loadMaintenanceConfig(nil); err == nil {
		t.Fatal("--retention-days -1 accepted")
	}
}

// conduit-3dad: `run-task fts_rebuild` reports messages_fts drift with
// --dry-run, repairs it without, and `status` shows the drift.
func TestMaintenanceRunTask_FTSRebuild(t *testing.T) {
	f := newMaintenanceFixture(t, config.MaintenanceConfig{})
	db, err := sql.Open("sqlite", database.BuildDSN(f.dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`INSERT INTO messages_fts(message_id, session_key, role, content) VALUES ('gone', 'cron_x', 'user', 'orphan')`,
		`DELETE FROM messages_fts WHERE message_id = 'telegram_123_eeee-m'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	useMaintenanceFlags(t, f.cfgPath, "")

	out := captureMaintenanceStdout(t, func() error { return showMaintenanceStatus(nil, nil) })
	if !strings.Contains(out, "Search index (messages_fts): 1 stale index rows (1 orphaned, 0 outdated, 0 duplicate) and 1 unindexed messages") {
		t.Fatalf("status lacks the drift:\n%s", out)
	}

	maintenanceDryRun = true
	out = captureMaintenanceStdout(t, func() error { return runSpecificMaintenanceTask(nil, []string{"fts_rebuild"}) })
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "would delete 1 and insert 1 messages_fts rows") {
		t.Fatalf("dry run output:\n%s", out)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE message_id = 'gone'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("dry run changed messages_fts: %d, %v", n, err)
	}

	maintenanceDryRun = false
	out = captureMaintenanceStdout(t, func() error { return runSpecificMaintenanceTask(nil, []string{"fts_rebuild"}) })
	if !strings.Contains(out, "Repaired messages_fts: deleted 1 stale rows, indexed 1 messages") {
		t.Fatalf("run output:\n%s", out)
	}
	out = captureMaintenanceStdout(t, func() error { return showMaintenanceStatus(nil, nil) })
	if !strings.Contains(out, "Search index (messages_fts): in sync (4 rows)") {
		t.Fatalf("status after repair:\n%s", out)
	}
}

// conduit-16f0: maintenance.keep_backups rotates <db>.backup.<ts> files after
// a run's backup (never other files), dry run and status show the rotation,
// and keep_backups 0 keeps everything.
func TestMaintenanceRun_BackupRotation(t *testing.T) {
	keep := 2
	f := newMaintenanceFixture(t, config.MaintenanceConfig{RetentionDays: 30, KeepBackups: &keep})
	var old []string
	for i := 0; i < 3; i++ {
		p := f.dbPath + ".backup." + time.Date(2026, 1, 1, i, 0, 0, 0, time.UTC).Format("20060102T150405.000Z")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		old = append(old, p)
	}
	manual := f.dbPath + ".bak-manual"
	if err := os.WriteFile(manual, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	useMaintenanceFlags(t, f.cfgPath, "")

	out := captureMaintenanceStdout(t, func() error { return showMaintenanceStatus(nil, nil) })
	if !strings.Contains(out, "Backups in ") || !strings.Contains(out, "keep newest 2") || strings.Count(out, "remove\n") != 2 {
		t.Fatalf("status lacks the backups:\n%s", out)
	}
	cfgOut := captureMaintenanceStdout(t, func() error { return showMaintenanceConfig(nil, nil) })
	if !strings.Contains(cfgOut, "Keep Backups: newest 2") {
		t.Fatalf("config output:\n%s", cfgOut)
	}

	maintenanceDryRun = true
	out = captureMaintenanceStdout(t, func() error { return runSpecificMaintenanceTask(nil, []string{"session_cleanup"}) })
	if !strings.Contains(out, "would remove 2 old backup(s)") {
		t.Fatalf("dry run output:\n%s", out)
	}
	maintenanceDryRun = false
	out = captureMaintenanceStdout(t, func() error { return runSpecificMaintenanceTask(nil, []string{"session_cleanup"}) })
	if !strings.Contains(out, "removed 2 old backup(s)") {
		t.Fatalf("run output:\n%s", out)
	}
	left, _ := filepath.Glob(f.dbPath + ".backup.*")
	if len(left) != 2 {
		t.Fatalf("backups left = %v", left)
	}
	for _, p := range []string{old[2], manual} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed", p)
		}
	}

	// keep_backups 0: the config loads and rotation is off.
	zero := 0
	f2 := newMaintenanceFixture(t, config.MaintenanceConfig{KeepBackups: &zero})
	cfgFile = f2.cfgPath
	m, _, err := loadMaintenanceConfig(mustLoadConfig(t, f2.cfgPath))
	if err != nil || m.Sessions.KeepBackups != 0 || m.Database.KeepBackups != 0 {
		t.Fatalf("keep_backups 0 = %+v, %v", m, err)
	}
	m, _, err = loadMaintenanceConfig(nil)
	if err != nil || m.Sessions.KeepBackups != 3 {
		t.Fatalf("default keep = %d, %v", m.Sessions.KeepBackups, err)
	}
}

func mustLoadConfig(t *testing.T, path string) *config.Config {
	t.Helper()
	cfg, err := loadExistingConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}
