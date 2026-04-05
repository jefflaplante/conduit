package rem

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/database"
)

func setupTestREMCycle(t *testing.T) (*REMCycle, *brain.Brain, string) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "brain.db")

	b, err := brain.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create brain: %v", err)
	}

	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	config := REMConfig{
		PruneAgeDays:      30,
		SalienceDecayRate: 0.1,
		IntegrationDay:    0,
		GroomWithLLM:      false,
		LogPath:           "",
	}

	rem := NewREMCycle(b, db, config)
	return rem, b, tmpDir
}

func TestGroom_NoSources(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	result, err := rem.Groom(ctx, false)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	if result.FilesChecked != 0 {
		t.Errorf("expected 0 files checked, got %d", result.FilesChecked)
	}

	if len(result.FilesChanged) != 0 {
		t.Errorf("expected 0 files changed, got %d", len(result.FilesChanged))
	}
}

func TestGroom_WithSourceFile(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.md")
	content := "# Test File\n\nThis is test content."
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Store an entry with this file as source
	source := fmt.Sprintf("file:%s", testFile)
	err := b.Store(ctx, "test-key", "test value", brain.TierLongTerm, source)
	if err != nil {
		t.Fatalf("failed to store entry: %v", err)
	}

	// First groom - should check the file and update hash
	result, err := rem.Groom(ctx, false)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	if result.FilesChecked != 1 {
		t.Errorf("expected 1 file checked, got %d", result.FilesChecked)
	}

	if len(result.FilesChanged) != 0 {
		t.Errorf("expected 0 files changed on first run, got %d", len(result.FilesChanged))
	}

	if result.KeysUpdated != 1 {
		t.Errorf("expected 1 key updated, got %d", result.KeysUpdated)
	}

	// Modify the file
	modifiedContent := "# Test File\n\nThis is modified content."
	if err := os.WriteFile(testFile, []byte(modifiedContent), 0644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	// Second groom - should detect the change
	result, err = rem.Groom(ctx, false)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	if result.FilesChecked != 1 {
		t.Errorf("expected 1 file checked, got %d", result.FilesChecked)
	}

	if len(result.FilesChanged) != 1 {
		t.Errorf("expected 1 file changed, got %d", len(result.FilesChanged))
	}

	if result.FilesChanged[0] != testFile {
		t.Errorf("expected changed file to be %s, got %s", testFile, result.FilesChanged[0])
	}
}

func TestGroom_DryRun(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.md")
	content := "# Test File\n\nThis is test content."
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Store an entry with this file as source
	source := fmt.Sprintf("file:%s", testFile)
	err := b.Store(ctx, "test-key", "test value", brain.TierLongTerm, source)
	if err != nil {
		t.Fatalf("failed to store entry: %v", err)
	}

	// Run groom in dry run mode
	result, err := rem.Groom(ctx, true)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	if result.FilesChecked != 1 {
		t.Errorf("expected 1 file checked, got %d", result.FilesChecked)
	}

	// In dry run, no keys should be updated
	if result.KeysUpdated != 0 {
		t.Errorf("expected 0 keys updated in dry run, got %d", result.KeysUpdated)
	}
}

func TestGroom_MissingFile(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Store an entry with a non-existent file as source
	source := "file:/nonexistent/path/test.md"
	err := b.Store(ctx, "test-key", "test value", brain.TierLongTerm, source)
	if err != nil {
		t.Fatalf("failed to store entry: %v", err)
	}

	// Groom should handle missing files gracefully
	result, err := rem.Groom(ctx, false)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	// File is checked but can't be hashed, so it's skipped
	if result.FilesChecked != 1 {
		t.Errorf("expected 1 file checked, got %d", result.FilesChecked)
	}

	if len(result.FilesChanged) != 0 {
		t.Errorf("expected 0 files changed, got %d", len(result.FilesChanged))
	}
}

func TestGroom_NonFileSource(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Store an entry with a non-file source
	err := b.Store(ctx, "test-key", "test value", brain.TierLongTerm, "user:manual")
	if err != nil {
		t.Fatalf("failed to store entry: %v", err)
	}

	// Groom should skip non-file sources
	result, err := rem.Groom(ctx, false)
	if err != nil {
		t.Fatalf("Groom failed: %v", err)
	}

	// No files should be checked since source doesn't start with "file:"
	if result.FilesChecked != 0 {
		t.Errorf("expected 0 files checked, got %d", result.FilesChecked)
	}
}
