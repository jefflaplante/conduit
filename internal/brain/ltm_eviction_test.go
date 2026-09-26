package brain

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.28: LTM capacity eviction must never drop the fact that was
// just stored, and must pick the genuinely lowest-value older entry.

func ltmCount(t *testing.T, b *Brain) int {
	t.Helper()
	var n int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm`).Scan(&n))
	return n
}

func ltmHas(t *testing.T, b *Brain, key string) bool {
	t.Helper()
	var n int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, key).Scan(&n))
	return n == 1
}

func archiveReason(t *testing.T, b *Brain, key string) string {
	t.Helper()
	var reason string
	err := b.db.QueryRow(`SELECT reason FROM brain_archive WHERE key = ?`, key).Scan(&reason)
	if err != nil {
		return ""
	}
	return reason
}

func sqlTime(d time.Duration) string {
	return time.Now().Add(-d).UTC().Format("2006-01-02 15:04:05")
}

// fillAndAgeLTM stores n LTM entries, reads each back once (so every row has
// been "accessed" and carries the bumped ~0.568 stored salience), then
// backdates all timestamps by age to simulate time passing.
func fillAndAgeLTM(t *testing.T, b *Brain, n int, age time.Duration) {
	t.Helper()
	ctx := testCtx("user1")
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("old.%d", i)
		require.NoError(t, b.Store(ctx, key, "value", TierLongTerm, ""))
		e, err := b.Get(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, e)
	}
	ts := sqlTime(age)
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ?`, ts, ts)
	require.NoError(t, err)
}

// Reproduction: at capacity, the newly stored fact must survive eviction.
func TestLTMEviction_NewFactSurvivesAtCapacity(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(5), WithSpreadingEnabled(false))
	ctx := testCtx("user1")
	fillAndAgeLTM(t, b, 5, 2*time.Hour)

	require.NoError(t, b.Store(ctx, "new.fact", "remember me", TierLongTerm, ""))

	e, err := b.Get(ctx, "new.fact")
	require.NoError(t, err)
	require.NotNil(t, e, "newly stored fact was evicted by capacity eviction")
	assert.Equal(t, "remember me", e.Value)
	assert.Equal(t, 5, ltmCount(t, b), "table should be trimmed back to capacity")
}

// Eviction removes the genuinely lowest-value older entry, not an old but
// high-value one, and archives it before deleting.
func TestLTMEviction_EvictsLowestValueOldEntry(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(5), WithSpreadingEnabled(false))
	ctx := testCtx("user1")
	fillAndAgeLTM(t, b, 5, 2*time.Hour)

	// old.0: very old but high-value (high salience, heavily used long ago).
	_, err := b.db.Exec(`UPDATE brain_ltm SET salience = 0.95, access_count = 40, accessed_at = ?, created_at = ? WHERE key = 'old.0'`,
		sqlTime(120*24*time.Hour), sqlTime(365*24*time.Hour))
	require.NoError(t, err)
	// old.3: the genuinely low-value one (decayed salience, single access, stale).
	_, err = b.db.Exec(`UPDATE brain_ltm SET salience = 0.05, access_count = 1, accessed_at = ?, created_at = ? WHERE key = 'old.3'`,
		sqlTime(45*24*time.Hour), sqlTime(45*24*time.Hour))
	require.NoError(t, err)

	require.NoError(t, b.Store(ctx, "new.fact", "v", TierLongTerm, ""))

	assert.True(t, ltmHas(t, b, "new.fact"), "new fact must survive")
	assert.True(t, ltmHas(t, b, "old.0"), "old high-value fact must not be evicted for being old")
	assert.False(t, ltmHas(t, b, "old.3"), "lowest-value entry should be evicted")
	assert.Equal(t, "capacity", archiveReason(t, b, "old.3"), "evicted entry should be archived")
	assert.Equal(t, 5, ltmCount(t, b))
}

// Rows touched within the grace window are never evicted, even if that means
// the table temporarily exceeds capacity.
func TestLTMEviction_GraceWindowProtectsRecentRows(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(3), WithSpreadingEnabled(false))
	ctx := testCtx("user1")
	for i := 0; i < 5; i++ {
		require.NoError(t, b.Store(ctx, fmt.Sprintf("k.%d", i), "v", TierLongTerm, ""))
	}
	assert.Equal(t, 5, ltmCount(t, b), "all rows are inside the grace window; none may be evicted")

	// Once they age out of the window, the next store trims back to capacity.
	_, err := b.db.Exec(`UPDATE brain_ltm SET created_at = ?, accessed_at = ?`, sqlTime(2*time.Hour), sqlTime(2*time.Hour))
	require.NoError(t, err)
	require.NoError(t, b.Store(ctx, "k.new", "v", TierLongTerm, ""))
	assert.True(t, ltmHas(t, b, "k.new"))
	assert.Equal(t, 3, ltmCount(t, b))
}

// StoreBulk at capacity keeps every entry from the batch.
func TestLTMEviction_StoreBulkAtCapacity(t *testing.T) {
	b := newTestBrain(t, WithMaxLTMEntries(5), WithSpreadingEnabled(false))
	ctx := testCtx("user1")
	fillAndAgeLTM(t, b, 5, 2*time.Hour)

	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "bulk.a", Value: "a", Tier: TierLongTerm},
		{Key: "bulk.b", Value: "b", Tier: TierLongTerm},
	}))

	for _, k := range []string{"bulk.a", "bulk.b"} {
		e, err := b.Get(ctx, k)
		require.NoError(t, err)
		require.NotNil(t, e, "bulk-stored %s was evicted", k)
	}
	assert.Equal(t, 5, ltmCount(t, b))
}
