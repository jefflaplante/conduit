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

	// Store a low-salience entry with old access time
	err := b.Store(ctx, "old.key", "old value", brain.TierLongTerm, "")
	require.NoError(t, err)

	// Manually set low salience and old accessed_at
	oldTime := time.Now().Add(-60 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err = rem.db.Exec("UPDATE brain_ltm SET salience = 0.01, accessed_at = ?", oldTime)
	require.NoError(t, err)

	// With default MaxLTMEntries=10000, table has 1 entry → under threshold → skip eviction
	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Empty(t, result.Archived, "should NOT evict when under MaxLTMEntries threshold")
}

func TestPrune_LowSalienceEntries_OverThreshold(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	// Set threshold very low so our small test data triggers eviction
	rem.config.MaxLTMEntries = 1

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store two entries (over threshold of 1)
	err := b.Store(ctx, "keep.key", "keep value", brain.TierLongTerm, "")
	require.NoError(t, err)
	err = b.Store(ctx, "evict.key", "evict value", brain.TierLongTerm, "")
	require.NoError(t, err)

	// Make evict.key low-salience and old
	oldTime := time.Now().Add(-60 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err = rem.db.Exec("UPDATE brain_ltm SET salience = 0.01, accessed_at = ? WHERE key = 'evict.key'", oldTime)
	require.NoError(t, err)

	// keep.key stays recent — won't match the salience+age filter
	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	// evict.key should be archived
	found := false
	for _, a := range result.Archived {
		if a.Key == "evict.key" && a.Reason == "low_salience" {
			found = true
		}
	}
	assert.True(t, found, "evict.key should be archived when over threshold")
}

func TestPrune_OrphanedFileSource(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create and then delete a file to make a genuine orphan
	testFile := filepath.Join(tmpDir, "temp.md")
	require.NoError(t, os.WriteFile(testFile, []byte("test content"), 0644))
	require.NoError(t, b.Store(ctx, "orphan.key", "value", brain.TierLongTerm, testFile))
	require.NoError(t, os.Remove(testFile))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	// The entry with a deleted file should be orphaned
	assert.Contains(t, result.Orphaned, "orphan.key")

	// Verify it was archived
	var count int
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_archive WHERE key = 'orphan.key'").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestPrune_NonPathSourcesNotOrphaned(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries with non-path sources — these should NEVER be treated as orphaned
	require.NoError(t, b.Store(ctx, "tool.key", "value", brain.TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "user.key", "value1", brain.TierLongTerm, "user:manual"))
	require.NoError(t, b.Store(ctx, "llm.key", "value2", brain.TierLongTerm, "llm:generated"))
	require.NoError(t, b.Store(ctx, "skill.key", "value3", brain.TierLongTerm, "skill:profile"))
	require.NoError(t, b.Store(ctx, "sub.key", "value4", brain.TierLongTerm, "sub-agent:abc"))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	// None of these should be orphaned
	assert.Empty(t, result.Orphaned, "non-path sources should not be treated as orphaned")
	assert.Empty(t, result.Archived, "nothing should be archived when under threshold and no real orphans")
}

func TestPrune_DryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	// Set threshold low to trigger eviction logic
	rem.config.MaxLTMEntries = 1

	ctx := brain.WithUserID(context.Background(), "testuser")

	err := b.Store(ctx, "dry.key1", "value", brain.TierLongTerm, "")
	require.NoError(t, err)
	err = b.Store(ctx, "dry.key2", "value", brain.TierLongTerm, "")
	require.NoError(t, err)

	oldTime := time.Now().Add(-60 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err = rem.db.Exec("UPDATE brain_ltm SET salience = 0.01, accessed_at = ? WHERE key = 'dry.key2'", oldTime)
	require.NoError(t, err)

	result, err := rem.Prune(ctx, true) // dry run
	require.NoError(t, err)

	// Should identify candidate but not actually archive
	var ltmCount int
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmCount)
	require.NoError(t, err)
	assert.Equal(t, 2, ltmCount, "dry run should not delete entries")

	// But result should still report what would happen
	assert.NotEmpty(t, result.Archived)
}

func TestPrune_RecentEntriesKept(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	// Set threshold low
	rem.config.MaxLTMEntries = 1

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries — both recent
	require.NoError(t, b.Store(ctx, "recent.key", "value", brain.TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, "recent.key2", "value2", brain.TierLongTerm, ""))

	// Make salience low but keep accessed_at recent (within PruneAgeDays)
	_, err := rem.db.Exec("UPDATE brain_ltm SET salience = 0.01")
	require.NoError(t, err)

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	// Recent entries should NOT be evicted even with low salience
	assert.Empty(t, result.Archived, "recent entries should not be evicted")
}

func TestPrune_FileColonSourceOrphaned(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create file, store with file: prefix, then delete
	testFile := filepath.Join(tmpDir, "gone.md")
	require.NoError(t, os.WriteFile(testFile, []byte("content"), 0644))
	require.NoError(t, b.Store(ctx, "file.orphan", "value", brain.TierLongTerm, "file:"+testFile))
	require.NoError(t, os.Remove(testFile))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	assert.Contains(t, result.Orphaned, "file.orphan")
}

func TestPrune_ExistingFileNotOrphaned(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create file and keep it
	testFile := filepath.Join(tmpDir, "exists.md")
	require.NoError(t, os.WriteFile(testFile, []byte("content"), 0644))
	require.NoError(t, b.Store(ctx, "file.exists", "value", brain.TierLongTerm, testFile))

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	assert.Empty(t, result.Orphaned, "existing file should not be orphaned")
}

func TestPrune_ArchivePreservesMetadata(t *testing.T) {
	rem, b, tmpDir := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create a file, store entry, then delete to trigger orphan archival
	testFile := filepath.Join(tmpDir, "meta.md")
	require.NoError(t, os.WriteFile(testFile, []byte("content"), 0644))
	require.NoError(t, b.Store(ctx, "archive.key", "archive value", brain.TierLongTerm, testFile))

	// Set specific salience
	_, err := rem.db.Exec("UPDATE brain_ltm SET salience = 0.05 WHERE key = 'archive.key'")
	require.NoError(t, err)

	require.NoError(t, os.Remove(testFile))

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
	assert.Equal(t, testFile, source)
	assert.Equal(t, 0.05, salience)
}

// TestPrune_ColdLTMEvicted verifies that LTM entries with access_count = 0 and
// created_at older than 30 days are evicted and counted in ColdEvicted.
func TestPrune_ColdLTMEvicted(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Insert a "cold-aged" entry directly: access_count=0, created_at=31d ago.
	oldTime := time.Now().Add(-31 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 0, 0.5)
	`, "cold.aged", "never-used value", "test", oldTime, oldTime)
	require.NoError(t, err)

	// Insert a recent entry (shouldn't be evicted — too young).
	require.NoError(t, b.Store(ctx, "fresh.key", "recent", brain.TierLongTerm, "test"))

	// Insert an aged but accessed entry (shouldn't be evicted — access_count > 0).
	_, err = rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 5, 0.5)
	`, "aged.used", "still useful", "test", oldTime, oldTime)
	require.NoError(t, err)

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 1, result.ColdEvicted, "exactly one cold-aged entry should be evicted")

	// Verify cold.aged is gone from LTM.
	var count int
	require.NoError(t, rem.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "cold.aged").Scan(&count))
	assert.Equal(t, 0, count, "cold.aged should be deleted from LTM")

	// Verify fresh.key and aged.used are still present.
	require.NoError(t, rem.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "fresh.key").Scan(&count))
	assert.Equal(t, 1, count, "fresh.key should not be evicted")
	require.NoError(t, rem.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "aged.used").Scan(&count))
	assert.Equal(t, 1, count, "aged.used (access_count>0) should not be evicted")
}

// TestPrune_ColdLTMDryRun verifies that dry-run mode reports but doesn't delete.
func TestPrune_ColdLTMDryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	oldTime := time.Now().Add(-31 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, ?, ?, ?, 0, 0.5)
	`, "cold.dryrun", "never-used", "test", oldTime, oldTime)
	require.NoError(t, err)

	result, err := rem.Prune(ctx, true)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 1, result.ColdEvicted, "dry-run should count cold-aged entries")

	// Verify the entry is still in the DB.
	var count int
	require.NoError(t, rem.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, "cold.dryrun").Scan(&count))
	assert.Equal(t, 1, count, "dry-run must not delete the entry")
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		source   string
		expected bool
	}{
		{"", false},
		{"tool", false},
		{"user:manual", false},
		{"llm:generated", false},
		{"skill:profile", false},
		{"sub-agent:abc", false},
		{"/home/jules/file.md", true},
		{"/tmp/test", true},
		{"file:MEMORY.md", true},
		{"file:/home/jules/workspace/test.md", true},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			assert.Equal(t, tt.expected, isFilePath(tt.source))
		})
	}
}
