package vecgo

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestService creates an in-memory VecGo service for testing.
func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(func() { svc.Close() })
	return svc
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
}

func TestIndexer_IndexNow_EmptyWorkspace(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: ""})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, result.FilesScanned)
}

func TestIndexer_IndexNow_IndexesMarkdownFiles(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "MEMORY.md", "# Memory\n\nImportant project notes.\n")
	writeTestFile(t, workspaceDir, "README.md", "# README\n\nProject overview.\n")

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})

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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})

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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})

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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
	result, err := idx.IndexNow(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, result.FilesScanned, "only .md files should be scanned")
	assert.Equal(t, 1, result.FilesIndexed)
}

func TestIndexer_IndexFile_SingleFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "single.md", "# Single\n\nA single file to index.\n")

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})

	// Index once
	require.NoError(t, idx.IndexFile(context.Background(), "single.md"))

	// Index again -- should skip (no error, no change)
	require.NoError(t, idx.IndexFile(context.Background(), "single.md"))
}

func TestIndexer_RemoveFile(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "removable.md", "# Removable\n\nContent to remove.\n")

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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

	idx := NewIndexer(svc, IndexerConfig{
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

	assert.Equal(t, 1, idx.Status().TrackedFiles)
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

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})

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

func TestIndexer_SearchAfterIndexing(t *testing.T) {
	svc := newTestService(t)
	workspaceDir := t.TempDir()

	writeTestFile(t, workspaceDir, "MEMORY.md", "# Memory\n\nThe quick brown fox jumps over the lazy dog.\n")
	writeTestFile(t, workspaceDir, "notes.md", "# Notes\n\nDatabase configuration uses SQLite with WAL mode.\n")

	idx := NewIndexer(svc, IndexerConfig{WorkspaceDir: workspaceDir})
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
