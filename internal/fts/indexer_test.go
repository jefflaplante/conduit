package fts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexWorkspace_IndexesMarkdownFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	// Create test markdown files
	writeFile(t, workspaceDir, "README.md", "# README\n\nThis is the readme file.\n")
	writeFile(t, workspaceDir, "MEMORY.md", "## Memory\n\nImportant memory content here.\n")

	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatalf("IndexWorkspace: %v", err)
	}

	// Verify chunks were created
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("expected document_chunks to have rows after indexing")
	}

	// Verify FTS5 index is populated
	var ftsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM document_chunks_fts").Scan(&ftsCount); err != nil {
		t.Fatal(err)
	}
	if ftsCount == 0 {
		t.Error("expected document_chunks_fts to have rows")
	}
}

func TestIndexWorkspace_SkipsUnchangedFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	writeFile(t, workspaceDir, "test.md", "## Test\n\nSome content.\n")

	idx := NewIndexer(db, workspaceDir)

	// Index once
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	var count1 int
	db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&count1)

	// Index again without changes - should be a no-op
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	var count2 int
	db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&count2)

	if count1 != count2 {
		t.Errorf("chunk count changed on re-index without file changes: %d -> %d", count1, count2)
	}
}

func TestIndexWorkspace_ReindexesChangedFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	writeFile(t, workspaceDir, "test.md", "## Original\n\nOriginal content.\n")

	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify original content
	var content string
	db.QueryRow("SELECT content FROM document_chunks LIMIT 1").Scan(&content)
	if content != "Original content." {
		t.Errorf("expected original content, got: %s", content)
	}

	// Modify file
	writeFile(t, workspaceDir, "test.md", "## Updated\n\nUpdated content.\n")

	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify updated content
	db.QueryRow("SELECT content FROM document_chunks LIMIT 1").Scan(&content)
	if content != "Updated content." {
		t.Errorf("expected updated content, got: %s", content)
	}
}

func TestIndexWorkspace_RemovesStaleFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	writeFile(t, workspaceDir, "keep.md", "## Keep\n\nKeep this.\n")
	writeFile(t, workspaceDir, "remove.md", "## Remove\n\nRemove this.\n")

	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Delete the file
	os.Remove(filepath.Join(workspaceDir, "remove.md"))

	// Re-index
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify only keep.md chunks remain
	var count int
	db.QueryRow("SELECT COUNT(*) FROM document_chunks WHERE file_path = 'remove.md'").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 chunks for removed file, got %d", count)
	}

	db.QueryRow("SELECT COUNT(*) FROM document_chunks WHERE file_path = 'keep.md'").Scan(&count)
	if count == 0 {
		t.Error("expected chunks for keep.md to still exist")
	}
}

func TestIndexWorkspace_SubdirectoryFiles(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	// Create a subdirectory with a markdown file
	memDir := filepath.Join(workspaceDir, "memory")
	os.MkdirAll(memDir, 0755)
	writeFile(t, memDir, "notes.md", "## Notes\n\nSome notes in a subdirectory.\n")

	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify the file_path uses relative path
	var filePath string
	db.QueryRow("SELECT file_path FROM document_chunks LIMIT 1").Scan(&filePath)
	if filePath != filepath.Join("memory", "notes.md") {
		t.Errorf("expected relative path 'memory/notes.md', got: %s", filePath)
	}
}

func TestRemoveFile(t *testing.T) {
	db := setupTestDB(t)
	workspaceDir := t.TempDir()

	writeFile(t, workspaceDir, "test.md", "## Test\n\nContent.\n")

	idx := NewIndexer(db, workspaceDir)
	if err := idx.IndexWorkspace(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := idx.RemoveFile(context.Background(), "test.md"); err != nil {
		t.Fatal(err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM document_chunks").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 chunks after RemoveFile, got %d", count)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
