package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// writeTestConfig writes a minimal valid config JSON to disk.
func writeTestConfig(t *testing.T, dir, dbPath, wsDir string) string {
	t.Helper()
	cfg := map[string]interface{}{
		"port": 18789,
		"database": map[string]interface{}{
			"path": dbPath,
		},
		"ai": map[string]interface{}{
			"default_provider": "anthropic",
			"providers": []map[string]interface{}{
				{"name": "anthropic", "type": "anthropic", "api_key": "test-key", "model": "claude-3-5-sonnet-20241022"},
			},
		},
		"tools": map[string]interface{}{
			"enabled_tools":   []string{"read"},
			"max_tool_chains": 25,
			"sandbox": map[string]interface{}{
				"workspace_dir": wsDir,
				"allowed_paths": []string{wsDir},
			},
		},
		"workspace": map[string]interface{}{
			"context_dir": wsDir,
		},
		"channels": []interface{}{},
		"heartbeat": map[string]interface{}{
			"enabled":          true,
			"interval_seconds": 30,
			"enable_metrics":   true,
			"enable_events":    true,
			"log_level":        "info",
			"max_queue_depth":  1000,
		},
		"agent_heartbeat": map[string]interface{}{
			"enabled":          true,
			"interval_minutes": 5,
			"timezone":         "UTC",
			"quiet_enabled":    true,
			"quiet_hours": map[string]interface{}{
				"start_time": "23:00",
				"end_time":   "08:00",
			},
			"alert_queue_path": "memory/alerts/pending.json",
			"alert_targets":    []interface{}{},
			"alert_retry_policy": map[string]interface{}{
				"max_retries":    3,
				"base_delay_ms":  1000,
				"max_delay_ms":   30000,
				"backoff_factor": 2.0,
			},
			"severity_routing": map[string]interface{}{},
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal test config: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, data, 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return cfgPath
}

// createTestDB creates a minimal SQLite database with a table and some data.
func createTestDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE test_data (id INTEGER PRIMARY KEY, value TEXT);
		INSERT INTO test_data (value) VALUES ('hello'), ('world');
	`)
	if err != nil {
		t.Fatalf("create test table: %v", err)
	}
}

func TestCreateBackup(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "gateway.db")
	createTestDB(t, dbPath)

	wsDir := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul\nTest soul file"), 0644)
	os.WriteFile(filepath.Join(wsDir, "USER.md"), []byte("# User\nTest user file"), 0644)

	memDir := filepath.Join(wsDir, "memory")
	os.MkdirAll(memDir, 0755)
	os.WriteFile(filepath.Join(memDir, "2024-01-01.md"), []byte("memory entry"), 0644)

	cfgPath := writeTestConfig(t, tmpDir, dbPath, wsDir)

	outPath := filepath.Join(tmpDir, "test-backup.tar.gz")

	result, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: outPath,
		Verbose:    true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if result.FileCount < 4 {
		t.Errorf("expected at least 4 files, got %d", result.FileCount)
	}
	if result.TotalSize == 0 {
		t.Error("expected non-zero total size")
	}
	if !result.Components.Has(ComponentDatabase) {
		t.Error("expected database component")
	}
	if !result.Components.Has(ComponentConfig) {
		t.Error("expected config component")
	}
	if !result.Components.Has(ComponentWorkspace) {
		t.Error("expected workspace component")
	}

	// Verify archive is readable.
	listResult, err := ListBackup(ListOptions{BackupPath: outPath})
	if err != nil {
		t.Fatalf("ListBackup: %v", err)
	}
	if listResult.Manifest.Version != ManifestVersion {
		t.Errorf("expected manifest version %q, got %q", ManifestVersion, listResult.Manifest.Version)
	}

	// Check that expected files exist in archive.
	fileNames := make(map[string]bool)
	for _, f := range listResult.Files {
		fileNames[f.Path] = true
	}
	for _, expected := range []string{"manifest.json", "database/gateway.db", "config/config.json"} {
		if !fileNames[expected] {
			t.Errorf("expected file %q in archive", expected)
		}
	}
	if !fileNames["workspace/SOUL.md"] {
		t.Error("expected workspace/SOUL.md in archive")
	}
	if !fileNames["workspace/memory/2024-01-01.md"] {
		t.Error("expected workspace/memory/2024-01-01.md in archive")
	}
}

func TestDatabaseSnapshot(t *testing.T) {
	tmpDir := t.TempDir()

	srcPath := filepath.Join(tmpDir, "source.db")
	createTestDB(t, srcPath)

	dstPath := filepath.Join(tmpDir, "snapshot.db")

	info, err := snapshotDatabase(context.Background(), srcPath, dstPath)
	if err != nil {
		t.Fatalf("snapshotDatabase: %v", err)
	}

	if info.Size == 0 {
		t.Error("expected non-zero database size")
	}
	if info.TableCount == 0 {
		t.Error("expected at least one table")
	}

	// Verify snapshot is usable.
	db, err := sql.Open("sqlite", dstPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_data").Scan(&count)
	if err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}
}

func TestRestoreBackup(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source data.
	srcDir := filepath.Join(tmpDir, "src")
	os.MkdirAll(srcDir, 0755)

	dbPath := filepath.Join(srcDir, "gateway.db")
	createTestDB(t, dbPath)

	wsDir := filepath.Join(srcDir, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("restore test"), 0644)

	cfgPath := writeTestConfig(t, srcDir, dbPath, wsDir)
	archivePath := filepath.Join(tmpDir, "restore-test.tar.gz")

	_, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: archivePath,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Restore to new location.
	restoreDir := filepath.Join(tmpDir, "restored")
	os.MkdirAll(restoreDir, 0755)

	result, err := RestoreBackup(RestoreOptions{
		BackupPath:    archivePath,
		Force:         true,
		ConfigPath:    filepath.Join(restoreDir, "config.json"),
		DatabasePath:  filepath.Join(restoreDir, "gateway.db"),
		WorkspacePath: filepath.Join(restoreDir, "workspace"),
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	if result.FilesRestored < 3 {
		t.Errorf("expected at least 3 restored files, got %d", result.FilesRestored)
	}

	// Verify restored files.
	if _, err := os.Stat(filepath.Join(restoreDir, "config.json")); err != nil {
		t.Error("restored config not found")
	}
	if _, err := os.Stat(filepath.Join(restoreDir, "gateway.db")); err != nil {
		t.Error("restored database not found")
	}

	data, err := os.ReadFile(filepath.Join(restoreDir, "workspace", "SOUL.md"))
	if err != nil {
		t.Fatal("restored SOUL.md not found")
	}
	if string(data) != "restore test" {
		t.Errorf("SOUL.md content mismatch: got %q", string(data))
	}

	// Verify restored database has data.
	db, err := sql.Open("sqlite", filepath.Join(restoreDir, "gateway.db"))
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_data").Scan(&count)
	if err != nil {
		t.Fatalf("query restored db: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows in restored db, got %d", count)
	}
}

func TestRestoreDryRun(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "gateway.db")
	createTestDB(t, dbPath)

	wsDir := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("dry run test"), 0644)

	cfgPath := writeTestConfig(t, tmpDir, dbPath, wsDir)
	archivePath := filepath.Join(tmpDir, "dryrun-test.tar.gz")

	_, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: archivePath,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	restoreDir := filepath.Join(tmpDir, "should-not-exist")

	result, err := RestoreBackup(RestoreOptions{
		BackupPath:    archivePath,
		DryRun:        true,
		WorkspacePath: restoreDir,
	})
	if err != nil {
		t.Fatalf("RestoreBackup dry-run: %v", err)
	}

	if result.FilesRestored == 0 {
		t.Error("dry-run should report files that would be restored")
	}

	// Verify nothing was actually written.
	if _, err := os.Stat(restoreDir); !os.IsNotExist(err) {
		t.Error("dry-run should not create files")
	}
}

func TestListBackup(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "gateway.db")
	createTestDB(t, dbPath)

	wsDir := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("list test"), 0644)

	cfgPath := writeTestConfig(t, tmpDir, dbPath, wsDir)
	archivePath := filepath.Join(tmpDir, "list-test.tar.gz")

	_, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: archivePath,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	result, err := ListBackup(ListOptions{BackupPath: archivePath, Verbose: true})
	if err != nil {
		t.Fatalf("ListBackup: %v", err)
	}

	if result.Manifest.Version != ManifestVersion {
		t.Errorf("expected version %q, got %q", ManifestVersion, result.Manifest.Version)
	}
	if result.Manifest.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if len(result.Files) < 4 {
		t.Errorf("expected at least 4 files, got %d", len(result.Files))
	}
	if result.Manifest.DatabaseInfo.TableCount == 0 {
		t.Error("expected non-zero table count")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := NewManifest(
		ComponentDatabase|ComponentConfig|ComponentWorkspace,
		OriginalPaths{
			Config:       "/etc/conduit/config.json",
			Database:     "/var/lib/conduit/gateway.db",
			WorkspaceDir: "/var/lib/conduit/workspace",
		},
		DatabaseInfo{Size: 1024 * 1024, TableCount: 5},
	)

	data, err := MarshalManifest(m)
	if err != nil {
		t.Fatalf("MarshalManifest: %v", err)
	}

	m2, err := UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("UnmarshalManifest: %v", err)
	}

	if m2.Version != m.Version {
		t.Errorf("version mismatch: %q vs %q", m.Version, m2.Version)
	}
	if m2.Components != m.Components {
		t.Errorf("components mismatch: %d vs %d", m.Components, m2.Components)
	}
	if m2.OriginalPaths.Database != m.OriginalPaths.Database {
		t.Errorf("database path mismatch")
	}
	if m2.DatabaseInfo.Size != m.DatabaseInfo.Size {
		t.Errorf("database size mismatch")
	}

	if err := ValidateManifest(m2); err != nil {
		t.Errorf("ValidateManifest: %v", err)
	}
}

func TestValidateTarEntry_PathTraversal(t *testing.T) {
	tests := []struct {
		name      string
		hdr       *tar.Header
		targetDir string
		dest      string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "normal path is allowed",
			hdr: &tar.Header{
				Name:     "workspace/SOUL.md",
				Typeflag: tar.TypeReg,
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/workspace/SOUL.md",
			wantErr:   false,
		},
		{
			name: "path traversal with ../ rejected",
			hdr: &tar.Header{
				Name:     "../../etc/passwd",
				Typeflag: tar.TypeReg,
			},
			targetDir: "/tmp/restore",
			dest:      "/etc/passwd",
			wantErr:   true,
			errSubstr: "path traversal",
		},
		{
			name: "absolute path rejected",
			hdr: &tar.Header{
				Name:     "/etc/passwd",
				Typeflag: tar.TypeReg,
			},
			targetDir: "/tmp/restore",
			dest:      "/etc/passwd",
			wantErr:   true,
			errSubstr: "absolute path",
		},
		{
			name: "embedded .. that escapes target rejected",
			hdr: &tar.Header{
				Name:     "foo/../../bar",
				Typeflag: tar.TypeReg,
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/bar",
			wantErr:   true,
			errSubstr: "path traversal",
		},
		{
			name: "embedded .. that stays inside target allowed",
			hdr: &tar.Header{
				Name:     "foo/../foo/bar",
				Typeflag: tar.TypeReg,
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/foo/bar",
			wantErr:   false,
		},
		{
			name: "symlink rejected",
			hdr: &tar.Header{
				Name:     "workspace/link",
				Typeflag: tar.TypeSymlink,
				Linkname: "/etc/passwd",
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/workspace/link",
			wantErr:   true,
			errSubstr: "symlink entry not allowed",
		},
		{
			name: "hard link rejected",
			hdr: &tar.Header{
				Name:     "workspace/hardlink",
				Typeflag: tar.TypeLink,
				Linkname: "/etc/passwd",
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/workspace/hardlink",
			wantErr:   true,
			errSubstr: "hard link entry not allowed",
		},
		{
			name: "directory entry allowed",
			hdr: &tar.Header{
				Name:     "workspace/subdir/",
				Typeflag: tar.TypeDir,
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/workspace/subdir",
			wantErr:   false,
		},
		{
			name: "unsupported entry type rejected",
			hdr: &tar.Header{
				Name:     "workspace/device",
				Typeflag: tar.TypeChar,
			},
			targetDir: "/tmp/restore",
			dest:      "/tmp/restore/workspace/device",
			wantErr:   true,
			errSubstr: "unsupported tar entry type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarEntry(tt.hdr, tt.targetDir, tt.dest)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errSubstr)
				} else if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestExtractFile_RejectsSymlinksInArchive(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a tar.gz archive with a symlink entry
	archivePath := filepath.Join(tmpDir, "malicious.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// Add a manifest.json
	manifest := NewManifest(
		ComponentWorkspace,
		OriginalPaths{
			WorkspaceDir: filepath.Join(tmpDir, "workspace"),
		},
		DatabaseInfo{},
	)
	manifestData, _ := MarshalManifest(manifest)
	tw.WriteHeader(&tar.Header{
		Name: "manifest.json",
		Size: int64(len(manifestData)),
		Mode: 0644,
	})
	tw.Write(manifestData)

	// Add a regular file
	content := []byte("safe content")
	tw.WriteHeader(&tar.Header{
		Name:     "workspace/safe.txt",
		Size:     int64(len(content)),
		Mode:     0644,
		Typeflag: tar.TypeReg,
	})
	tw.Write(content)

	// Add a symlink entry (should be rejected)
	tw.WriteHeader(&tar.Header{
		Name:     "workspace/evil",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	})

	tw.Close()
	gw.Close()
	f.Close()

	// Create workspace target dir
	wsDir := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(wsDir, 0755)

	// Restore should succeed but skip the symlink
	result, err := RestoreBackup(RestoreOptions{
		BackupPath:    archivePath,
		Force:         true,
		WorkspacePath: wsDir,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// The safe file should be restored
	if result.FilesRestored < 1 {
		t.Errorf("expected at least 1 restored file, got %d", result.FilesRestored)
	}

	// The symlink should be skipped with a warning
	if result.FilesSkipped < 1 {
		t.Errorf("expected at least 1 skipped file (the symlink), got %d", result.FilesSkipped)
	}

	hasSymlinkWarning := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "symlink") {
			hasSymlinkWarning = true
			break
		}
	}
	if !hasSymlinkWarning {
		t.Errorf("expected warning about symlink, got warnings: %v", result.Warnings)
	}

	// Verify the symlink was NOT created
	if _, err := os.Lstat(filepath.Join(wsDir, "evil")); !os.IsNotExist(err) {
		t.Error("symlink entry should not have been created")
	}

	// Verify the safe file WAS created
	data, err := os.ReadFile(filepath.Join(wsDir, "safe.txt"))
	if err != nil {
		t.Fatalf("safe file should have been created: %v", err)
	}
	if string(data) != "safe content" {
		t.Errorf("safe file content mismatch: got %q", string(data))
	}
}

func TestBackupFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "gateway.db")
	createTestDB(t, dbPath)

	wsDir := filepath.Join(tmpDir, "workspace")
	os.MkdirAll(wsDir, 0755)
	os.WriteFile(filepath.Join(wsDir, "SOUL.md"), []byte("# Soul\npermission test"), 0644)

	cfgPath := writeTestConfig(t, tmpDir, dbPath, wsDir)
	outPath := filepath.Join(tmpDir, "perms-test.tar.gz")

	_, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: outPath,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Verify the backup archive has restrictive permissions (0600).
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("backup archive permissions: got %o, want 0600", perm)
	}
}

func TestSnapshotDatabasePermissions(t *testing.T) {
	tmpDir := t.TempDir()

	srcPath := filepath.Join(tmpDir, "source.db")
	createTestDB(t, srcPath)

	dstPath := filepath.Join(tmpDir, "snapshot.db")

	_, err := snapshotDatabase(context.Background(), srcPath, dstPath)
	if err != nil {
		t.Fatalf("snapshotDatabase: %v", err)
	}

	// Verify the snapshot file has restrictive permissions (0600).
	info, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("snapshot permissions: got %o, want 0600", perm)
	}
}

func TestCopyFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()

	src := filepath.Join(tmpDir, "source.txt")
	if err := os.WriteFile(src, []byte("test data"), 0644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmpDir, "copy.txt")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	// Verify the copied file has restrictive permissions (0600).
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat copy: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("copy permissions: got %o, want 0600", perm)
	}
}

func TestMissingWorkspaceFiles(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "gateway.db")
	createTestDB(t, dbPath)

	// Use a non-existent workspace dir.
	wsDir := filepath.Join(tmpDir, "nonexistent-workspace")

	cfgPath := writeTestConfig(t, tmpDir, dbPath, wsDir)
	archivePath := filepath.Join(tmpDir, "missing-ws-test.tar.gz")

	result, err := CreateBackup(context.Background(), BackupOptions{
		ConfigPath: cfgPath,
		OutputPath: archivePath,
	})
	if err != nil {
		t.Fatalf("CreateBackup should succeed with missing workspace: %v", err)
	}

	if len(result.Warnings) == 0 {
		t.Error("expected warnings about missing workspace dir")
	}

	// Should still have manifest, database, and config.
	if result.FileCount < 3 {
		t.Errorf("expected at least 3 files (manifest, db, config), got %d", result.FileCount)
	}
}
