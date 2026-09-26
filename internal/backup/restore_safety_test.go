package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"conduit/internal/database"
)

// writeArchive builds a .tar.gz with a manifest and the given entries.
func writeArchive(t *testing.T, path string, m *BackupManifest, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	md, err := MarshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("manifest.json", md)
	for name, data := range entries {
		add(name, []byte(data))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
}

func testManifest(root string) *BackupManifest {
	return NewManifest(ComponentDatabase|ComponentConfig|ComponentWorkspace, OriginalPaths{
		Config:       filepath.Join(root, "config.json"),
		Database:     filepath.Join(root, "data", "gateway.db"),
		WorkspaceDir: filepath.Join(root, "workspace"),
		SkillsPaths:  []string{filepath.Join(root, "skills", "x")},
	}, DatabaseInfo{})
}

// conduit-31jg.9 (3): a workspace/ entry containing ../ must not escape the
// workspace directory (it previously wrote one level up, e.g. over the
// config file). Same for database/ entries.
func TestRestore_RejectsDotDotEscape(t *testing.T) {
	root := t.TempDir()
	m := testManifest(root)
	archive := filepath.Join(root, "evil.tar.gz")
	writeArchive(t, archive, m, map[string]string{
		"workspace/../config.json":     "PWNED",
		"workspace/../../outside.txt":  "PWNED",
		"database/../escaped.db":       "PWNED",
		"skills/x/../../../skills.txt": "PWNED",
		"workspace/fine.md":            "fine",
	})
	cfgPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	res, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(cfgPath); string(got) != "original" {
		t.Fatalf("config.json overwritten via workspace/../: %q", got)
	}
	for _, p := range []string{
		filepath.Join(filepath.Dir(root), "outside.txt"),
		filepath.Join(root, "data", "..", "escaped.db"),
		filepath.Join(root, "escaped.db"),
		filepath.Join(filepath.Dir(root), "skills.txt"),
		filepath.Join(root, "skills.txt"),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("escaped write landed at %s", p)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "workspace", "fine.md")); string(got) != "fine" {
		t.Errorf("legit workspace file not restored")
	}
	rejected := 0
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, "rejected ") {
			rejected++
		}
	}
	if rejected < 4 {
		t.Errorf("expected >=4 rejections, got %d: %v", rejected, res.Warnings)
	}
}

// conduit-31jg.9 (3): a symlinked directory inside the workspace pointing
// outside must not be followed.
func TestRestore_RejectsSymlinkedParentEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	m := testManifest(root)
	ws := m.OriginalPaths.WorkspaceDir
	if err := os.MkdirAll(ws, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "link")); err != nil {
		t.Skip("symlinks unsupported")
	}
	archive := filepath.Join(root, "a.tar.gz")
	writeArchive(t, archive, m, map[string]string{"workspace/link/pwn.txt": "PWNED"})
	if _, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); err == nil {
		t.Fatal("write followed a symlinked directory out of the workspace")
	}
}

// conduit-31jg.9 (1): restoring a database must remove stale -wal/-shm so
// old WAL frames cannot replay onto the restored file.
func TestRestore_RemovesStaleWAL(t *testing.T) {
	root := t.TempDir()
	m := testManifest(root)
	dbDest := m.OriginalPaths.Database

	// Existing live-ish DB at the destination with un-checkpointed WAL
	// frames (rows only in the WAL).
	if err := os.MkdirAll(filepath.Dir(dbDest), 0755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", database.BuildDSN(dbDest)+"&_pragma=wal_autocheckpoint%3D0")
	if err != nil {
		t.Fatal(err)
	}
	old.SetMaxOpenConns(1)
	if _, err := old.Exec(`CREATE TABLE test_data (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO test_data(value) VALUES ('stale1'),('stale2'),('stale3');`); err != nil {
		t.Fatal(err)
	}
	// Snapshot the -wal/-shm while the connection holds them, then close
	// (which checkpoints) and put the stale WAL back.
	walData, err := os.ReadFile(dbDest + "-wal")
	if err != nil || len(walData) == 0 {
		t.Fatalf("expected WAL data: %v", err)
	}
	old.Close()
	if err := os.WriteFile(dbDest+"-wal", walData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbDest+"-shm", make([]byte, 32768), 0600); err != nil {
		t.Fatal(err)
	}

	// Archive DB: a fresh DB with 2 rows.
	src := filepath.Join(t.TempDir(), "src.db")
	createTestDB(t, src)
	srcData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "a.tar.gz")
	writeArchive(t, archive, m, map[string]string{"database/gateway.db": string(srcData)})

	if _, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true}); err != nil {
		t.Fatal(err)
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(dbDest + sfx); err == nil {
			t.Errorf("stale %s left behind", sfx)
		}
	}
	db, err := sql.Open("sqlite", dbDest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var ic string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity_check=%q err=%v", ic, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM test_data`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("restored DB has %d rows, want 2 (from archive)", n)
	}
	// No temp files left.
	ents, _ := os.ReadDir(filepath.Dir(dbDest))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".restore-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

// conduit-31jg.9 (1): restore refuses while the gateway is live, even with
// --force (Force only skips the confirmation prompt).
func TestRestore_RefusesWhenGatewayLive(t *testing.T) {
	root := t.TempDir()
	m := testManifest(root)
	archive := filepath.Join(root, "a.tar.gz")
	writeArchive(t, archive, m, map[string]string{"workspace/a.md": "x"})

	// Live pidfile (our own PID is certainly alive).
	pidfile := filepath.Join(root, "conduit.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true, PidfilePath: pidfile})
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("expected refusal for live pidfile, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "workspace", "a.md")); statErr == nil {
		t.Fatal("files written despite live gateway")
	}

	// Port in use.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, err = RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true, GatewayAddr: ln.Addr().String()})
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("expected refusal for listening port, got %v", err)
	}

	// Stale pidfile (dead PID) + closed port: allowed.
	if err := os.WriteFile(pidfile, []byte("999999999"), 0600); err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if _, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true, PidfilePath: pidfile, GatewayAddr: addr}); err != nil {
		t.Fatalf("stale pidfile/closed port should not block restore: %v", err)
	}

	// Dry-run never refuses.
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackup(RestoreOptions{BackupPath: archive, DryRun: true, PidfilePath: pidfile}); err != nil {
		t.Fatalf("dry-run refused: %v", err)
	}
}

// conduit-31jg.9 (2): backup honours brain.path from config instead of only
// the path derived from database.path, and restore puts it back there.
func TestBackup_HonoursBrainPath(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	os.MkdirAll(srcDir, 0755)
	dbPath := filepath.Join(srcDir, "gateway.db")
	createTestDB(t, dbPath)
	brainPath := filepath.Join(srcDir, "custom", "my-brain.db")
	os.MkdirAll(filepath.Dir(brainPath), 0755)
	createTestDB(t, brainPath)
	wsDir := filepath.Join(srcDir, "workspace")
	os.MkdirAll(wsDir, 0755)

	cfgPath := writeTestConfig(t, srcDir, dbPath, wsDir)
	raw, _ := os.ReadFile(cfgPath)
	var cfgMap map[string]interface{}
	if err := json.Unmarshal(raw, &cfgMap); err != nil {
		t.Fatal(err)
	}
	cfgMap["brain"] = map[string]interface{}{"enabled": true, "path": brainPath}
	raw, _ = json.Marshal(cfgMap)
	os.WriteFile(cfgPath, raw, 0600)

	archive := filepath.Join(tmp, "b.tar.gz")
	res, err := CreateBackup(context.Background(), BackupOptions{ConfigPath: cfgPath, OutputPath: archive})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Components.Has(ComponentBrainDB) {
		t.Fatal("brain DB at brain.path was not backed up")
	}
	m, err := readManifestFromArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if m.OriginalPaths.BrainDatabase != brainPath {
		t.Fatalf("manifest brain path = %q, want %q", m.OriginalPaths.BrainDatabase, brainPath)
	}

	// Restore in place: brain goes back to brain.path, not <db>.brain.db.
	os.Remove(brainPath)
	if _, err := RestoreBackup(RestoreOptions{BackupPath: archive, Force: true, SkipConfig: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(brainPath); err != nil {
		t.Fatalf("brain not restored to brain.path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srcDir, "gateway.brain.db")); err == nil {
		t.Fatal("brain restored to derived path instead of brain.path")
	}
}

// conduit-31jg.9 (4): the snapshot must include rows that are only in the
// WAL, and must never fall back to a raw copy of the main file (which would
// silently drop WAL content).
func TestSnapshot_IncludesWALAndNoRawCopyFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "live.db")
	live, err := sql.Open("sqlite", database.BuildDSN(src)+"&_pragma=wal_autocheckpoint%3D0")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	live.SetMaxOpenConns(1)
	if _, err := live.Exec(`CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('a'),('b'),('c');`); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(src + "-wal"); err != nil || st.Size() == 0 {
		t.Fatalf("expected un-checkpointed WAL: %v", err)
	}

	dst := filepath.Join(dir, "snap.db")
	if _, err := snapshotDatabase(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	snap, err := sql.Open("sqlite", dst)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("snapshot rows=%d err=%v, want 3", n, err)
	}
	snap.Close()

	// Force VACUUM INTO to fail (destination exists and is non-empty): must
	// return an error rather than a WAL-less raw copy.
	dst2 := filepath.Join(dir, "snap2.db")
	os.WriteFile(dst2, []byte("not empty"), 0600)
	if _, err := snapshotDatabase(context.Background(), src, dst2); err == nil {
		t.Fatal("expected error when VACUUM INTO fails; raw-copy fallback would drop WAL rows")
	}
}
