package fts

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_IndexesNewMarkdownFile(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	idx := NewIndexer(db, workspaceDir)

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Create a new .md file
	writeFile(t, workspaceDir, "new.md", "# New\n\nNew content.\n")

	// Wait for the watcher to pick it up
	waitForChunks(t, db, "new.md", 1, 3*time.Second)
}

func TestWatcher_ReindexesChangedFile(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	// Pre-create and index a file
	writeFile(t, workspaceDir, "doc.md", "# Original\n\nOriginal content.\n")
	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Modify the file
	writeFile(t, workspaceDir, "doc.md", "# Updated\n\nUpdated content.\n")

	// Wait for re-index
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var content string
		db.QueryRow("SELECT content FROM document_chunks WHERE file_path = 'doc.md' LIMIT 1").Scan(&content)
		if content == "Updated content." {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("file was not re-indexed after modification")
}

func TestWatcher_RemovesDeletedFile(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	// Pre-create and index a file
	writeFile(t, workspaceDir, "delete-me.md", "# Delete\n\nDelete this.\n")
	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Delete the file
	os.Remove(filepath.Join(workspaceDir, "delete-me.md"))

	// Wait for removal
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM document_chunks WHERE file_path = 'delete-me.md'").Scan(&count)
		if count == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("chunks were not removed after file deletion")
}

func TestWatcher_IgnoresNonMarkdownFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	idx := NewIndexer(db, workspaceDir)

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Create a non-markdown file
	writeFile(t, workspaceDir, "notes.txt", "Some text content.\n")

	time.Sleep(500 * time.Millisecond)

	var count int
	db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 chunks for non-markdown file, got %d", count)
	}
}

func TestWatcher_SubdirectoryChanges(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	// Pre-create subdirectory so watcher can watch it
	subDir := filepath.Join(workspaceDir, "docs")
	os.MkdirAll(subDir, 0755)

	idx := NewIndexer(db, workspaceDir)

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(100 * time.Millisecond)

	// Create a file in the subdirectory
	writeFile(t, subDir, "sub.md", "# Sub\n\nSubdirectory content.\n")

	waitForChunks(t, db, filepath.Join("docs", "sub.md"), 1, 3*time.Second)
}

func TestWatcher_StopsCleanly(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	idx := NewIndexer(db, workspaceDir)

	w, err := NewWatcher(idx, workspaceDir)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Clean shutdown
	case <-time.After(2 * time.Second):
		t.Error("watcher did not stop within 2 seconds")
	}
}

// waitForChunks polls until at least minCount chunks exist for the given path.
func waitForChunks(t *testing.T, db *sql.DB, filePath string, minCount int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var count int
		db.QueryRow("SELECT COUNT(*) FROM document_chunks WHERE file_path = ?", filePath).Scan(&count)
		if count >= minCount {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %d chunks for %s", minCount, filePath)
}
