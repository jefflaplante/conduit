package rem

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.28: cold-LTM sweep tests. The sweep previously matched
// access_count = 0, which Store-written rows never have (they start at 1).

func insertLTMRow(t *testing.T, rem *REMCycle, key string, accessCount int, created, accessed time.Time) {
	t.Helper()
	_, err := rem.db.Exec(`
		INSERT INTO brain_ltm (key, value, source, created_at, accessed_at, access_count, salience)
		VALUES (?, ?, 'test', ?, ?, ?, 0.5)
	`, key, "value of "+key,
		created.UTC().Format("2006-01-02 15:04:05"),
		accessed.UTC().Format("2006-01-02 15:04:05"),
		accessCount)
	require.NoError(t, err)
}

func ltmRowExists(t *testing.T, rem *REMCycle, key string) bool {
	t.Helper()
	var n int
	require.NoError(t, rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, key).Scan(&n))
	return n == 1
}

// TestPrune_ColdLTMEvicted verifies that, at capacity, write-once entries
// (access_count <= 1) older than 30 days and untouched since are archived and
// counted in ColdEvicted, while used or recently-touched entries are kept.
func TestPrune_ColdLTMEvicted(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	rem.config.MaxLTMEntries = 1 // at capacity

	ctx := brain.WithUserID(context.Background(), "testuser")
	now := time.Now()
	old := now.Add(-31 * 24 * time.Hour)
	oldStr := old.UTC().Format("2006-01-02 15:04:05")

	// Stored once 31 days ago, never used since: access_count = 1 (Store's initial value).
	insertLTMRow(t, rem, "cold.aged", 1, old, old)
	// Legacy row with access_count = 0.
	insertLTMRow(t, rem, "cold.legacy", 0, old, old)
	// Recent store (too young).
	require.NoError(t, b.Store(ctx, "fresh.key", "recent", brain.TierLongTerm, "test"))
	// Old but used repeatedly.
	insertLTMRow(t, rem, "aged.used", 5, old, old)
	// Old, single access, but touched 2 days ago.
	insertLTMRow(t, rem, "aged.recent", 1, old, now.Add(-48*time.Hour))
	// Stored, read back via Get (access_count -> 2), then aged.
	require.NoError(t, b.Store(ctx, "aged.read", "v", brain.TierLongTerm, "test"))
	e, err := b.Get(ctx, "aged.read")
	require.NoError(t, err)
	require.NotNil(t, e)
	_, err = rem.db.Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ? WHERE key = 'aged.read'`, oldStr, oldStr)
	require.NoError(t, err)

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 2, result.ColdEvicted, "cold.aged and cold.legacy should be evicted")
	assert.False(t, ltmRowExists(t, rem, "cold.aged"))
	assert.False(t, ltmRowExists(t, rem, "cold.legacy"))
	for _, k := range []string{"fresh.key", "aged.used", "aged.recent", "aged.read"} {
		assert.True(t, ltmRowExists(t, rem, k), "%s should not be evicted", k)
	}

	// Cold evictions are archived, not hard-deleted.
	var reason string
	require.NoError(t, rem.db.QueryRow(`SELECT reason FROM brain_archive WHERE key = 'cold.aged'`).Scan(&reason))
	assert.Equal(t, "cold", reason)
}

// TestPrune_ColdLTMUnderCapacityNoop verifies the cold sweep never touches a
// table under MaxLTMEntries (the common case for a live brain DB).
func TestPrune_ColdLTMUnderCapacityNoop(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t) // MaxLTMEntries = 10000
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	old := time.Now().Add(-90 * 24 * time.Hour)
	insertLTMRow(t, rem, "cold.but.safe", 1, old, old)

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result.ColdEvicted)
	assert.True(t, ltmRowExists(t, rem, "cold.but.safe"))
}

// TestPrune_ColdLTMBatchLimit verifies a large cold backlog is drained at most
// coldEvictBatchLimit rows per run.
func TestPrune_ColdLTMBatchLimit(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	rem.config.MaxLTMEntries = 1

	ctx := brain.WithUserID(context.Background(), "testuser")
	old := time.Now().Add(-60 * 24 * time.Hour)
	total := coldEvictBatchLimit + 25
	for i := 0; i < total; i++ {
		insertLTMRow(t, rem, fmt.Sprintf("cold.%03d", i), 1, old, old)
	}

	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, coldEvictBatchLimit, result.ColdEvicted)

	var remaining int
	require.NoError(t, rem.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm`).Scan(&remaining))
	assert.Equal(t, total-coldEvictBatchLimit, remaining)
}

// TestPrune_ColdLTMDryRun verifies that dry-run mode reports but doesn't delete.
func TestPrune_ColdLTMDryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	rem.config.MaxLTMEntries = 1

	ctx := brain.WithUserID(context.Background(), "testuser")
	old := time.Now().Add(-31 * 24 * time.Hour)
	insertLTMRow(t, rem, "cold.dryrun", 1, old, old)

	result, err := rem.Prune(ctx, true)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 1, result.ColdEvicted, "dry-run should count cold-aged entries")
	assert.True(t, ltmRowExists(t, rem, "cold.dryrun"), "dry-run must not delete the entry")
}
