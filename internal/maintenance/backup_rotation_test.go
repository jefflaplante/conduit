package maintenance

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var rotBase = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// writeBackupFiles creates n "<db>.backup.<ts>" files one hour apart,
// oldest first, and returns their paths.
func writeBackupFiles(t *testing.T, dbPath string, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		p := dbPath + ".backup." + rotBase.Add(time.Duration(i)*time.Hour).Format(backupTimestampLayout)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// writeDecoys creates files next to dbPath that look like backups but were
// not written by BackupDatabase; rotation must never touch them.
func writeDecoys(t *testing.T, dbPath string) []string {
	t.Helper()
	dir := filepath.Dir(dbPath)
	ts := rotBase.Add(-24 * time.Hour).Format(backupTimestampLayout)
	names := []string{
		filepath.Base(dbPath) + ".bak-20260101",
		filepath.Base(dbPath) + ".pre-migrate",
		filepath.Base(dbPath) + ".backup.old",
		filepath.Base(dbPath) + ".backup." + ts + ".gz",
		filepath.Base(dbPath) + ".backup." + ts + "-wal",
		filepath.Base(dbPath) + ".backup.20260101T120000Z", // no millis
		filepath.Base(dbPath) + ".backup.20260101T120000.000+0000",
		"other.db.backup." + ts,
		"x" + filepath.Base(dbPath) + ".backup." + ts,
	}
	var out []string
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	// A directory and a symlink with a backup name are not regular files.
	d := filepath.Join(dir, filepath.Base(dbPath)+".backup."+rotBase.Add(-48*time.Hour).Format(backupTimestampLayout))
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	out = append(out, d)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := filepath.Join(dir, filepath.Base(dbPath)+".backup."+rotBase.Add(-72*time.Hour).Format(backupTimestampLayout))
	if err := os.Symlink(target, l); err != nil {
		t.Fatal(err)
	}
	return append(out, l, target)
}

func assertExist(t *testing.T, paths []string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
}

func paths(bs []BackupFile) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Path)
	}
	return out
}

// conduit-16f0: only exact "<db>.backup.<timestamp>" regular files are
// listed, newest first.
func TestListBackups_ExactPatternOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	backups := writeBackupFiles(t, dbPath, 3)
	writeDecoys(t, dbPath)

	got, err := ListBackups(dbPath, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{backups[2], backups[1], backups[0]}
	if strings.Join(paths(got), ",") != strings.Join(want, ",") {
		t.Fatalf("ListBackups = %v, want %v", paths(got), want)
	}
	if !got[0].Time.Equal(rotBase.Add(2*time.Hour)) || got[0].Size != 1 {
		t.Fatalf("newest = %+v", got[0])
	}

	// A missing backup dir is empty, not an error.
	if got, err := ListBackups(dbPath, filepath.Join(t.TempDir(), "nope")); err != nil || len(got) != 0 {
		t.Fatalf("missing dir = %v, %v", got, err)
	}
}

func TestRotateBackups(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "gateway.db")
	backups := writeBackupFiles(t, dbPath, 5)
	decoys := writeDecoys(t, dbPath)

	// keep 0 keeps all.
	rot, err := RotateBackups(dbPath, "", 0, "")
	if err != nil || len(rot.Removed) != 0 || len(rot.Kept) != 5 {
		t.Fatalf("keep 0 = %+v, %v", rot, err)
	}

	// Plan with one pending backup: keep 3 means 2 existing survive.
	plan, err := PlanBackupRotation(dbPath, "", 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(plan.Removed); strings.Join(got, ",") != strings.Join([]string{backups[2], backups[1], backups[0]}, ",") {
		t.Fatalf("plan removes %v", got)
	}
	if !strings.Contains(plan.Summary(true), "would remove 3") {
		t.Fatalf("summary = %q", plan.Summary(true))
	}
	assertExist(t, backups)

	rot, err = RotateBackups(dbPath, "", 3, backups[4])
	if err != nil {
		t.Fatal(err)
	}
	if len(rot.Removed) != 2 || len(rot.Errors) != 0 {
		t.Fatalf("rotation = %+v", rot)
	}
	assertExist(t, backups[2:])
	assertExist(t, decoys)
	for _, p := range backups[:2] {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s not rotated out", p)
		}
	}

	// The backup just written is never removed, even if it sorts oldest.
	rot, err = RotateBackups(dbPath, "", 1, backups[2])
	if err != nil {
		t.Fatal(err)
	}
	assertExist(t, []string{backups[2], backups[4]})
	if _, err := os.Stat(backups[3]); err == nil {
		t.Error("backups[3] not rotated out")
	}
	if len(rot.Removed) != 1 {
		t.Fatalf("removed %v", paths(rot.Removed))
	}
	assertExist(t, decoys)
}

// conduit-16f0: the pre-VACUUM backup rotates old ones (keep_backups), in
// backup_dir when one is set, and a dry run reports the rotation.
func TestDatabaseMaintenanceTask_RotatesBackups(t *testing.T) {
	db, path := openGatewayDB(t)
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := writeBackupFiles(t, filepath.Join(dir, filepath.Base(path)), 3)
	decoy := filepath.Join(dir, filepath.Base(path)+".bak-manual")
	if err := os.WriteFile(decoy, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := DatabaseConfig{VacuumEnabled: true, VacuumThreshold: -1, BackupBeforeVacuum: true, BackupDir: dir, KeepBackups: 2, DryRun: true}
	res := NewDatabaseMaintenanceTask(db, path, cfg, log.New(io.Discard, "", 0)).Execute(context.Background())
	if !res.Success || !strings.Contains(res.Message, "backup rotation (keep 2): would remove 2 old backup(s)") {
		t.Fatalf("dry run = %+v", res)
	}
	assertExist(t, old)

	cfg.DryRun = false
	res = NewDatabaseMaintenanceTask(db, path, cfg, log.New(io.Discard, "", 0)).Execute(context.Background())
	if !res.Success || !strings.Contains(res.Message, "removed 2 old backup(s)") {
		t.Fatalf("run = %+v", res)
	}
	left, err := ListBackups(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 || left[1].Path != old[2] {
		t.Fatalf("left = %v", paths(left))
	}
	assertExist(t, []string{decoy})
}

// conduit-16f0: session cleanup rotates after its pre-prune backup; keep 0
// keeps everything.
func TestSessionCleanup_RotatesBackups(t *testing.T) {
	for _, keep := range []int{1, 0} {
		db, path := openGatewayDB(t)
		old := writeBackupFiles(t, path, 2)
		if _, err := db.Exec(`INSERT INTO sessions (key, user_id, channel_id, updated_at) VALUES ('cron_a_1', 'u', 'c', '2020-01-01 00:00:00')`); err != nil {
			t.Fatal(err)
		}
		cfg := SessionConfig{RetentionDays: 30, PrunablePrefixes: []string{"cron_"}, BatchSize: 10, CleanupEnabled: true, BackupBeforePrune: true, KeepBackups: keep}
		task := NewSessionCleanupTask(db, path, cfg, log.New(io.Discard, "", 0))
		res := task.Execute(context.Background())
		rep := res.Details.(*PruneReport)
		if !res.Success || rep.BackupPath == "" || rep.BackupRotation == nil {
			t.Fatalf("keep %d: %+v", keep, res)
		}
		left, _ := ListBackups(path, "")
		got := paths(left)
		sort.Strings(got)
		switch keep {
		case 1:
			if len(got) != 1 || got[0] != rep.BackupPath || len(rep.BackupRotation.Removed) != 2 {
				t.Fatalf("keep 1: left %v, rotation %+v", got, rep.BackupRotation)
			}
		case 0:
			if len(got) != 3 {
				t.Fatalf("keep 0: left %v", got)
			}
			assertExist(t, old)
		}
	}
}
