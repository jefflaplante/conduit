package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-6gx5: pin warmth as an eviction discriminator. ltmEvictionScoreSQL
// includes COALESCE(warmth,0)*warmthWeight, but no other test controls warmth
// at eviction time — a refactor dropping the term would have kept the suite
// green while silently changing victim selection.

// setupWarmthParity builds a full LTM (capacity 5, all rows outside the grace
// window) where old.1 and old.2 are identical on salience, access count and
// accessed_at and differ only in warmth; the other rows keep fresher access
// so the pair sits at the bottom of the ranking.
func setupWarmthParity(t *testing.T, opts ...Option) *Brain {
	t.Helper()
	b := newTestBrain(t, append([]Option{WithMaxLTMEntries(5), WithSpreadingEnabled(false)}, opts...)...)
	fillAndAgeLTM(t, b, 5, 2*time.Hour)
	_, err := b.db.Exec(`UPDATE brain_ltm SET salience = 0.5, warmth = 0, created_at = ?`, sqlTime(30*day))
	require.NoError(t, err)
	setAccess(t, b, "old.1", 3, 10*day)
	setAccess(t, b, "old.2", 3, 10*day)
	_, err = b.db.Exec(`UPDATE brain_ltm SET warmth = 0.8 WHERE key = 'old.1'`)
	require.NoError(t, err)
	return b
}

// Of two rows identical except for warmth, the cold one is evicted and Recall
// ranks it below the warm one; with warmth weighted out, the pair is an exact
// tie and eviction must still remove exactly one of them.
func TestWarmthEviction_RecallEvictionParity(t *testing.T) {
	// Recall side (separate brain: Recall bumps accessed_at into the grace window).
	rb := setupWarmthParity(t)
	keys := recallKeys(t, rb, "value")
	require.Contains(t, keys, "old.1")
	require.Contains(t, keys, "old.2")
	idx := func(k string) int {
		for i, key := range keys {
			if key == k {
				return i
			}
		}
		return -1
	}
	assert.Less(t, idx("old.1"), idx("old.2"), "recall should rank the warm row above the identical cold row")

	// Eviction side.
	eb := setupWarmthParity(t)
	require.NoError(t, eb.Store(testCtx("user1"), "new.fact", "v", TierLongTerm, ""))
	assert.True(t, ltmHas(t, eb, "new.fact"))
	assert.True(t, ltmHas(t, eb, "old.1"), "warm row must survive")
	assert.False(t, ltmHas(t, eb, "old.2"), "cold row with equal salience/access should be evicted")
	assert.Equal(t, "capacity", archiveReason(t, eb, "old.2"))
	assert.Equal(t, 5, ltmCount(t, eb))

	// With warmth weighted out, the pair ties on eviction score: either may
	// go, but eviction still fires and removes exactly one.
	zb := setupWarmthParity(t, WithWarmthWeight(0))
	require.NoError(t, zb.Store(testCtx("user1"), "new.fact", "v", TierLongTerm, ""))
	assert.NotEqual(t, ltmHas(t, zb, "old.1"), ltmHas(t, zb, "old.2"),
		"with warmth weighted out the tied rows must be indistinguishable to eviction")
}
