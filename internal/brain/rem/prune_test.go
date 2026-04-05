package rem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrune_LowSalienceEntries(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Insert low-salience old entries
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "low.key1", "value1", "test", oldTime, oldTime, 1, 0.05)
	require.NoError(t, err)

	_, err = rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "low.key2", "value2", "test", oldTime, oldTime, 1, 0.08)
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should have archived low-salience entries
	assert.GreaterOrEqual(t, len(result.Archived), 2)

	// Verify entries were moved to archive
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_archive WHERE reason = 'low_salience'`).Scan(&count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 2)

	// Verify entries were deleted from LTM
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key IN ('low.key1', 'low.key2')`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestPrune_OrphanedEntries(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create a temporary file that we'll delete
	testFile := filepath.Join(tmpDir, "test.md")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))

	// Store an entry with this file as source
	require.NoError(t, b.Store(ctx, "orphan.key", "value", brain.TierLongTerm, testFile))

	// Delete the file to make the entry orphaned
	require.NoError(t, os.Remove(testFile))

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should detect orphaned entries
	assert.GreaterOrEqual(t, len(result.Orphaned), 1)
	assert.Contains(t, result.Orphaned, "orphan.key")

	// Verify entry was archived
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_archive WHERE key = ? AND reason = 'orphaned'`, "orphan.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	// Verify entry was deleted from LTM
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "orphan.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestPrune_DryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Insert low-salience old entry
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "prune.key", "value", "test", oldTime, oldTime, 1, 0.05)
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30

	// Run in dry-run mode
	result, err := rem.Prune(ctx, true)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should report what would be archived
	assert.GreaterOrEqual(t, len(result.Archived), 1)

	// Verify entry was NOT archived (dry run)
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_archive`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)

	// Verify entry still exists in LTM
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "prune.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPrune_PreservesRecentEntries(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store recent entry even with low salience
	// Note: source is empty, so it won't be checked for orphans
	require.NoError(t, b.Store(ctx, "recent.key", "value", brain.TierLongTerm, ""))

	// Update salience to be low (but accessed_at is recent)
	_, err := rem.db.Exec(`UPDATE brain_ltm SET salience = 0.05 WHERE key = ?`, "recent.key")
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Recent entry should not be archived (accessed_at is within PruneAgeDays)
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "recent.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "recent entry should be preserved")
}

func TestPrune_PreservesHighSalienceEntries(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Insert old entry with high salience and empty source (so it won't be orphan-checked)
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "important.key", "value", "", oldTime, oldTime, 1, 0.8)
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// High salience entry should not be archived even if old
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "important.key").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPrune_EmptyLTM(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Should complete without errors
	assert.Empty(t, result.Archived)
	assert.Empty(t, result.Orphaned)
}

func TestPrune_NonFileSource(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with non-file sources that will fail os.Stat
	require.NoError(t, b.Store(ctx, "user.key", "value1", brain.TierLongTerm, "user:manual"))
	require.NoError(t, b.Store(ctx, "llm.key", "value2", brain.TierLongTerm, "llm:generated"))

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Non-file sources will be treated as orphaned (os.Stat fails)
	// This is expected behavior - prune checks all non-empty sources
	assert.GreaterOrEqual(t, len(result.Orphaned), 2)
	assert.Contains(t, result.Orphaned, "user.key")
	assert.Contains(t, result.Orphaned, "llm.key")

	// Entries should be archived and removed from LTM
	var count int
	err = rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key IN ('user.key', 'llm.key')`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestPrune_ArchivePreservesMetadata(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Insert low-salience old entry with specific metadata
	oldTime := time.Now().Add(-40 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "archive.key", "archive value", "test:source", oldTime, oldTime, 5, 0.05)
	require.NoError(t, err)

	rem.config.PruneAgeDays = 30

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify archive preserves key, value, source, salience
	var key, value, source string
	var salience float64
	err = rem.db.QueryRow(`
		SELECT key, value, source, salience
		FROM brain_archive
		WHERE key = ?
	`, "archive.key").Scan(&key, &value, &source, &salience)
	require.NoError(t, err)
	assert.Equal(t, "archive.key", key)
	assert.Equal(t, "archive value", value)
	assert.Equal(t, "test:source", source)
	assert.Equal(t, 0.05, salience)
}
