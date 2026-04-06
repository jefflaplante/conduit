package rem

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		PruneAgeDays:         30,
		SalienceDecayRate:    0.1,
		ConsolidateThreshold: 0.6,
		IntegrationDay:       0,
		GroomWithLLM:         false,
		LogPath:              "",
		WorkspaceDir:         tmpDir,
		MaxLTMEntries:        10000,
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

func TestGroom_AgeBasedStaleness(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := context.Background()

	// Store an old skill-sourced entry (>30 days old)
	oldTime := time.Now().Add(-35 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 1, 0.5)
	`, "skill.fact", "value", "skill:profile", oldTime, oldTime)
	require.NoError(t, err)

	// Store a recent skill-sourced entry
	require.NoError(t, b.Store(ctx, "skill.recent", "value", brain.TierLongTerm, "skill:profile"))

	result, err := rem.Groom(ctx, false)
	require.NoError(t, err)

	// Old entry should be marked stale
	assert.Equal(t, 1, result.EntriesMarkedStale)

	// Verify stale flag in DB
	var stale int
	err = rem.db.QueryRow(`SELECT stale FROM brain_ltm WHERE key = ?`, "skill.fact").Scan(&stale)
	require.NoError(t, err)
	assert.Equal(t, 1, stale)

	// Recent entry should NOT be stale
	err = rem.db.QueryRow(`SELECT stale FROM brain_ltm WHERE key = ?`, "skill.recent").Scan(&stale)
	require.NoError(t, err)
	assert.Equal(t, 0, stale)
}

func TestGroom_UserSourceNeverStale(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	// Store an old user-sourced entry
	oldTime := time.Now().Add(-365 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 1, 0.5)
	`, "user.old", "value", "user:manual", oldTime, oldTime)
	require.NoError(t, err)

	result, err := rem.Groom(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, 0, result.EntriesMarkedStale)
}

func TestGroom_LLMSourceStaleFaster(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	// Store an LLM-sourced entry 20 days old (> 14 day threshold)
	oldTime := time.Now().Add(-20 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 1, 0.5)
	`, "llm.inference", "value", "llm:generated", oldTime, oldTime)
	require.NoError(t, err)

	result, err := rem.Groom(context.Background(), false)
	require.NoError(t, err)

	assert.Equal(t, 1, result.EntriesMarkedStale)
}
