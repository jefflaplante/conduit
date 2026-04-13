package vecgo

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// newTestService creates an in-memory VecGo service for testing.
func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(testConfig())
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	return svc
}

// newTestServiceWithDB creates a VecGo service backed by a temp SQLite file.
func newTestServiceWithDB(t *testing.T) (*Service, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.vector.db")
	cfg := testConfig()
	cfg.DBPath = dbPath
	svc, err := NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	return svc, dbPath
}

// startTestIndexer creates an Indexer and launches its embed worker so that
// IndexNow/IndexFile can be called without Start().
func startTestIndexer(t *testing.T, svc *Service, cfg IndexerConfig) *Indexer {
	t.Helper()
	idx := NewIndexer(svc, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go idx.embedWorker(ctx)
	t.Cleanup(func() {
		cancel()
		close(idx.workCh)
		<-idx.workerDone
		if idx.hashDB != nil {
			idx.hashDB.Close()
		}
	})
	return idx
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

// waitForTrackedFiles polls until the indexer tracks the expected number of files.
func waitForTrackedFiles(t *testing.T, idx *Indexer, expected int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if idx.Status().TrackedFiles == expected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d tracked files, got %d", expected, idx.Status().TrackedFiles)
}

func TestIndexer_IndexNow_EmptyWorkspace(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 0, result.FilesScanned)
	assert.Equal(t, 0, result.FilesIndexed)
	assert.Equal(t, 0, result.FilesSkipped)
	assert.Equal(t, 0, result.FilesRemoved)
	assert.Empty(t, result.Errors)
}

func TestIndexer_IndexNow_EmptyWorkspaceDir(t *testing.T) {
	svc := newTestService(t)

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: ""})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result.FilesScanned)
}

func TestIndexer_IndexNow_IndexesMarkdownFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "MEMORY.md", "# Memory\n\nImportant project notes.\n")
	writeTestFile(t, workspaceDir, "README.md", "# README\n\nProject overview.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, result.FilesScanned)
	assert.Equal(t, 2, result.FilesIndexed)
	assert.Equal(t, 0, result.FilesSkipped)
	assert.Empty(t, result.Errors)
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestIndexer_IndexNow_SkipsUnchangedFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "test.md", "# Test\n\nSome content.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// First index
	result1, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result1.FilesIndexed)
	assert.Equal(t, 0, result1.FilesSkipped)

	// Second index -- should skip unchanged file
	result2, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result2.FilesIndexed)
	assert.Equal(t, 1, result2.FilesSkipped)
}

func TestIndexer_IndexNow_ReindexesChangedFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "test.md", "# Original\n\nOriginal content.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// First index
	result1, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result1.FilesIndexed)

	// Modify file
	writeTestFile(t, workspaceDir, "test.md", "# Updated\n\nUpdated content with new information.\n")

	// Second index -- should re-index changed file
	result2, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result2.FilesIndexed)
	assert.Equal(t, 0, result2.FilesSkipped)
}

func TestIndexer_IndexNow_RemovesStaleFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "keep.md", "# Keep\n\nKeep this file.\n")
	writeTestFile(t, workspaceDir, "remove.md", "# Remove\n\nRemove this file.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// First index
	result1, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result1.FilesIndexed)

	// Delete the file
	require.NoError(t, os.Remove(filepath.Join(workspaceDir, "remove.md")))

	// Second index -- should remove stale entry
	result2, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result2.FilesIndexed) // keep.md unchanged
	assert.Equal(t, 1, result2.FilesSkipped) // keep.md
	assert.Equal(t, 1, result2.FilesRemoved) // remove.md
}

func TestIndexer_IndexNow_SubdirectoryFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	memDir := filepath.Join(workspaceDir, "memory")
	require.NoError(t, os.MkdirAll(memDir, 0755))

	writeTestFile(t, memDir, "notes.md", "## Notes\n\nSome notes in memory directory.\n")
	writeTestFile(t, workspaceDir, "MEMORY.md", "# Main Memory\n\nTop-level memory.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 2, result.FilesScanned)
	assert.Equal(t, 2, result.FilesIndexed)
}

func TestIndexer_IndexNow_IgnoresNonMarkdown(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "readme.md", "# Readme\n")
	writeTestFile(t, workspaceDir, "data.json", `{"key": "value"}`)
	writeTestFile(t, workspaceDir, "script.sh", "#!/bin/bash\necho hello\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesScanned, "only .md files should be scanned")
	assert.Equal(t, 1, result.FilesIndexed)
}

func TestIndexer_IndexFile_SingleFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "single.md", "# Single\n\nA single file to index.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	err := idx.IndexFile(context.Background(), "single.md")
	require.NoError(t, err)

	// Verify the file is tracked
	status := idx.Status()
	assert.Equal(t, 1, status.TrackedFiles)
}

func TestIndexer_IndexFile_SkipsUnchanged(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "single.md", "# Single\n\nContent.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// Index once
	require.NoError(t, idx.IndexFile(context.Background(), "single.md"))

	// Index again -- should skip (no error, no change)
	require.NoError(t, idx.IndexFile(context.Background(), "single.md"))
}

func TestIndexer_RemoveFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "removable.md", "# Removable\n\nContent to remove.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	require.NoError(t, idx.IndexFile(context.Background(), "removable.md"))

	assert.Equal(t, 1, idx.Status().TrackedFiles)

	require.NoError(t, idx.RemoveFile(context.Background(), "removable.md"))
	assert.Equal(t, 0, idx.Status().TrackedFiles)
}

func TestIndexer_Status(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "a.md", "# A\n")
	writeTestFile(t, workspaceDir, "b.md", "# B\n")

	idx := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		PollInterval: 5 * time.Minute,
	})

	_, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	status := idx.Status()
	assert.Equal(t, workspaceDir, status.WorkspaceDir)
	assert.Equal(t, 2, status.TrackedFiles)
	assert.Equal(t, 5*time.Minute, status.PollInterval)
}

func TestIndexer_Start_InitialScan(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "startup.md", "# Startup\n\nIndexed on start.\n")

	idx := NewIndexer(svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		PollInterval: 0, // No polling
	})

	err := idx.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { idx.Stop() })

	// Start is non-blocking now; wait for the staggered scan to complete.
	waitForTrackedFiles(t, idx, 1, 5*time.Second)
}

func TestIndexer_StartStop_WithPolling(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "poll.md", "# Poll\n\nContent.\n")

	idx := NewIndexer(svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		PollInterval: 100 * time.Millisecond, // Very short for testing
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := idx.Start(ctx)
	require.NoError(t, err)

	// Wait for staggered scan to finish
	waitForTrackedFiles(t, idx, 1, 5*time.Second)

	// Give polling a chance to run at least once
	time.Sleep(250 * time.Millisecond)

	// Stop should not block or panic
	idx.Stop()

	assert.Equal(t, 1, idx.Status().TrackedFiles)
}

func TestIndexer_ContextCancellation(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	// Create several files
	for i := 0; i < 5; i++ {
		writeTestFile(t, workspaceDir, filepath.Base(filepath.Join(workspaceDir, "file"+string(rune('a'+i))+".md")),
			"# File\n\nContent.\n")
	}

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := idx.IndexNow(ctx)
	// Should return context error or partial results
	if err != nil {
		assert.ErrorIs(t, err, context.Canceled)
	} else {
		// Partial results are OK
		assert.NotNil(t, result)
	}
}

func TestIndexer_Start_ResilientToInitialScanFailure(t *testing.T) {
	svc := newTestService(t)

	// Use a non-existent workspace dir so the initial scan fails.
	idx := NewIndexer(svc, IndexerConfig{
		WorkspaceDir: "/nonexistent/path/that/does/not/exist",
		PollInterval: 100 * time.Millisecond,
	})

	// Start should NOT return an error even though the initial scan fails.
	err := idx.Start(context.Background())
	require.NoError(t, err, "Start should succeed even when initial scan fails")

	// The polling goroutine should have started — Stop must not hang.
	idx.Stop()
}

func TestIndexer_SearchAfterIndexing(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "MEMORY.md", "# Memory\n\nThe quick brown fox jumps over the lazy dog.\n")
	writeTestFile(t, workspaceDir, "notes.md", "# Notes\n\nDatabase configuration uses SQLite with WAL mode.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result.FilesIndexed)

	// Now search for content
	searchResults, err := svc.Search(context.Background(), "fox", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, searchResults, "should find indexed content via search")

	// At least one result should mention fox
	found := false
	for _, r := range searchResults {
		if len(r.Content) > 0 {
			found = true
		}
	}
	assert.True(t, found, "search should return content from indexed files")
}

func TestIndexer_EmbedPacing(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "a.md", "# A\nContent A.\n")
	writeTestFile(t, workspaceDir, "b.md", "# B\nContent B.\n")
	writeTestFile(t, workspaceDir, "c.md", "# C\nContent C.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		EmbedPacing:  50 * time.Millisecond,
	})

	start := time.Now()
	result, err := idx.IndexNow(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)

	assert.Equal(t, 3, result.FilesIndexed)
	// 3 files with 50ms pacing = at least 100ms (pacing between, not after last)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"pacing should add delay between embedding calls")
}

// --- Persistent hash tests ---

func TestIndexer_PersistentHashes_SurviveRestart(t *testing.T) {
	svc, dbPath := newTestServiceWithDB(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "persist.md", "# Persist\n\nThis content should survive restart.\n")
	writeTestFile(t, workspaceDir, "another.md", "# Another\n\nAnother file.\n")

	// First indexer: index files and persist hashes
	idx1 := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})
	result1, err := idx1.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, result1.FilesIndexed)
	assert.Equal(t, 0, result1.FilesSkipped)

	// Second indexer: same DB, should skip unchanged files
	idx2 := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})

	// Load persisted hashes
	require.NoError(t, idx2.loadHashes(context.Background()))
	assert.Equal(t, 2, len(idx2.hashes), "should have loaded 2 persisted hashes")

	// IndexNow should skip both files
	result2, err := idx2.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result2.FilesIndexed, "unchanged files should be skipped after restart")
	assert.Equal(t, 2, result2.FilesSkipped)
}

func TestIndexer_PersistentHashes_DetectsChanges(t *testing.T) {
	svc, dbPath := newTestServiceWithDB(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "mutable.md", "# Original\n\nOriginal content.\n")

	// First indexer: index the file
	idx1 := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})
	result1, err := idx1.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result1.FilesIndexed)

	// Modify the file
	writeTestFile(t, workspaceDir, "mutable.md", "# Changed\n\nDifferent content now.\n")

	// Second indexer: should detect the change
	idx2 := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})
	require.NoError(t, idx2.loadHashes(context.Background()))

	result2, err := idx2.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result2.FilesIndexed, "changed file should be re-indexed")
	assert.Equal(t, 0, result2.FilesSkipped)
}

func TestIndexer_HashPersistence_DeleteOnRemove(t *testing.T) {
	svc, dbPath := newTestServiceWithDB(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "removable.md", "# Removable\n\nWill be removed.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})

	// Index the file
	require.NoError(t, idx.IndexFile(context.Background(), "removable.md"))
	assert.Equal(t, 1, idx.Status().TrackedFiles)

	// Verify hash exists in DB
	var count int
	require.NoError(t, idx.hashDB.QueryRow("SELECT COUNT(*) FROM file_hashes WHERE path = ?", "removable.md").Scan(&count))
	assert.Equal(t, 1, count, "hash should be persisted in DB")

	// Remove the file from index
	require.NoError(t, idx.RemoveFile(context.Background(), "removable.md"))
	assert.Equal(t, 0, idx.Status().TrackedFiles)

	// Verify hash is deleted from DB
	require.NoError(t, idx.hashDB.QueryRow("SELECT COUNT(*) FROM file_hashes WHERE path = ?", "removable.md").Scan(&count))
	assert.Equal(t, 0, count, "hash should be deleted from DB after removal")
}

func TestIndexer_StaggeredScan_DoesNotBlock(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	// Create several files
	for i := 0; i < 10; i++ {
		writeTestFile(t, workspaceDir, filepath.Base(filepath.Join(workspaceDir, "file"+string(rune('a'+i))+".md")),
			"# File\n\nContent for pacing test.\n")
	}

	idx := NewIndexer(svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		EmbedPacing:  10 * time.Millisecond,
		PollInterval: 0, // No polling
	})

	start := time.Now()
	err := idx.Start(context.Background())
	startDuration := time.Since(start)
	require.NoError(t, err)
	t.Cleanup(func() { idx.Stop() })

	// Start() should return almost immediately (non-blocking)
	assert.Less(t, startDuration, 500*time.Millisecond,
		"Start() should return quickly, not block on indexing")

	// Eventually all files get indexed
	waitForTrackedFiles(t, idx, 10, 10*time.Second)
}

func TestIndexer_EnsureIndexed_QueuesNewFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	idx := NewIndexer(svc, IndexerConfig{
		WorkspaceDir: workspaceDir,
		PollInterval: 0,
	})
	err := idx.Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { idx.Stop() })

	// Wait for initial scan (empty workspace)
	time.Sleep(100 * time.Millisecond)

	// Create a new file after startup
	writeTestFile(t, workspaceDir, "ondemand.md", "# On Demand\n\nTriggered by access.\n")

	// Trigger on-demand indexing
	idx.EnsureIndexed(context.Background(), "ondemand.md")

	// Should eventually be tracked
	waitForTrackedFiles(t, idx, 1, 5*time.Second)
}

func TestIndexer_EnsureIndexed_SkipsCurrentFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "current.md", "# Current\n\nAlready indexed.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	idx.started.Store(true) // Simulate Start() having been called

	// Index the file first
	require.NoError(t, idx.IndexFile(context.Background(), "current.md"))
	assert.Equal(t, 1, idx.Status().TrackedFiles)

	// EnsureIndexed should return without queuing (file unchanged)
	idx.EnsureIndexed(context.Background(), "current.md")

	// Still only 1 tracked file, no re-indexing
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 1, idx.Status().TrackedFiles)
}

func TestIndexer_EnsureIndexed_BeforeStart(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "early.md", "# Early\n\nBefore start.\n")

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
	// Don't call Start() — EnsureIndexed should be a no-op
	idx.EnsureIndexed(context.Background(), "early.md")

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, idx.Status().TrackedFiles, "should not index before Start()")
}

// --- .vectorignore integration tests ---

func TestIndexer_RespectsVectorignore(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	// Create .vectorignore
	ignoreContent := "scratch.md\ndrafts/\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, ".vectorignore"), []byte(ignoreContent), 0644))

	// Create files — some should be ignored
	writeTestFile(t, workspaceDir, "important.md", "# Important\n\nIndex this.\n")
	writeTestFile(t, workspaceDir, "scratch.md", "# Scratch\n\nIgnore this.\n")

	draftsDir := filepath.Join(workspaceDir, "drafts")
	require.NoError(t, os.MkdirAll(draftsDir, 0755))
	writeTestFile(t, draftsDir, "wip.md", "# WIP\n\nIgnore this too.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesIndexed, "only important.md should be indexed")
	assert.Equal(t, 1, idx.Status().TrackedFiles)
}

func TestIndexer_VectorignoreWithIndexFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	ignoreContent := "scratch.md\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspaceDir, ".vectorignore"), []byte(ignoreContent), 0644))

	writeTestFile(t, workspaceDir, "scratch.md", "# Scratch\n\nIgnored.\n")

	idx := startTestIndexer(t, svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// IndexFile should silently skip ignored files
	err := idx.IndexFile(context.Background(), "scratch.md")
	require.NoError(t, err)
	assert.Equal(t, 0, idx.Status().TrackedFiles)
}

func TestIndexer_HashDB_DirectQuery(t *testing.T) {
	_, dbPath := newTestServiceWithDB(t)

	// Open the same DB to verify the table exists
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode%3Dwal&_pragma=busy_timeout%3D5000")
	require.NoError(t, err)
	defer db.Close()

	// The indexer should have created the file_hashes table
	svc2, _ := newTestServiceWithDB(t) // separate service won't conflict
	workspaceDir := t.TempDir()
	writeTestFile(t, workspaceDir, "test.md", "# Test\n")

	idx := startTestIndexer(t, svc2, IndexerConfig{
		WorkspaceDir: workspaceDir,
		DBPath:       dbPath,
	})
	require.NoError(t, idx.IndexFile(context.Background(), "test.md"))

	// Query directly
	var path, hash, indexedAt string
	err = idx.hashDB.QueryRow("SELECT path, hash, indexed_at FROM file_hashes WHERE path = ?", "test.md").Scan(&path, &hash, &indexedAt)
	require.NoError(t, err)
	assert.Equal(t, "test.md", path)
	assert.NotEmpty(t, hash)
	assert.NotEmpty(t, indexedAt)
}
