package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-6gx5: pin the access-bonus term of the recall blend and its SQL mirror
// in LTM capacity eviction.

const day = 24 * time.Hour

// newAccessBonusBrain returns a brain whose recall blend is reduced to the
// access-bonus term alone (match/salience/warmth weights zeroed), with
// spreading disabled so cluster expansion and warm injection can't reorder.
func newAccessBonusBrain(t *testing.T, opts ...Option) *Brain {
	t.Helper()
	base := []Option{
		WithSpreadingEnabled(false),
		WithMatchWeight(0),
		WithSalienceWeight(0),
		WithWarmthWeight(0),
		WithAccessBonusWeight(1),
	}
	return newTestBrain(t, append(base, opts...)...)
}

// setAccess overwrites a row's access profile directly; Store always stamps now.
func setAccess(t *testing.T, b *Brain, key string, count int, ago time.Duration) {
	t.Helper()
	res, err := b.db.Exec(`UPDATE brain_ltm SET access_count = ?, accessed_at = ? WHERE key = ?`,
		count, sqlTime(ago), key)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "row %q not found", key)
}

func recallKeys(t *testing.T, b *Brain, query string) []string {
	t.Helper()
	results, err := b.Recall(testCtx("user1"), query, 20)
	require.NoError(t, err)
	keys := make([]string, len(results))
	for i, e := range results {
		keys[i] = e.Key
	}
	return keys
}

func storeLTM(t *testing.T, b *Brain, keys ...string) {
	t.Helper()
	for _, k := range keys {
		require.NoError(t, b.Store(testCtx("user1"), k, "foo", TierLongTerm, "tool"))
	}
}

// Zero or negative access counts contribute exactly 0, regardless of recency,
// so a huge accessBonusWeight cannot reorder zero-access entries.
func TestAccessBonus_ZeroAccessGuard(t *testing.T) {
	b := newTestBrain(t, WithAccessBonusCap(10))
	now := time.Now()
	for _, c := range []int{0, -1, -500} {
		assert.Equal(t, 0.0, b.computeAccessBonus(&Entry{AccessCount: c, AccessedAt: now}), "count %d", c)
	}

	for _, abw := range []float64{0, 100} {
		b := newTestBrain(t,
			WithSpreadingEnabled(false),
			WithMatchWeight(0), WithSalienceWeight(1), WithWarmthWeight(0),
			WithAccessBonusWeight(abw),
		)
		storeLTM(t, b, "high.foo", "low.foo")
		_, err := b.db.Exec(`UPDATE brain_ltm SET salience = CASE key WHEN 'high.foo' THEN 0.6 ELSE 0.5 END`)
		require.NoError(t, err)
		// A negative count is treated like zero: still no bonus.
		setAccess(t, b, "high.foo", 0, day)
		setAccess(t, b, "low.foo", -5, day)

		assert.Equal(t, []string{"high.foo", "low.foo"}, recallKeys(t, b, "foo"),
			"accessBonusWeight=%v must not affect zero-access ordering", abw)
	}
}

// Same access count: the more recently accessed entry has the larger bonus.
func TestAccessBonus_RecencyOrdering(t *testing.T) {
	b := newAccessBonusBrain(t, WithAccessBonusCap(10))
	storeLTM(t, b, "old.foo", "recent.foo")
	setAccess(t, b, "old.foo", 2, 30*day)
	setAccess(t, b, "recent.foo", 2, 1*day)

	assert.Equal(t, []string{"recent.foo", "old.foo"}, recallKeys(t, b, "foo"))
}

// Same accessed_at: the higher access count wins while below the cap.
func TestAccessBonus_FrequencyOrdering(t *testing.T) {
	tests := []struct {
		name       string
		low, high  int
		storeOrder []string
	}{
		{"1 vs 3", 1, 3, []string{"high.foo", "low.foo"}},
		{"2 vs 20, stored reversed", 2, 20, []string{"low.foo", "high.foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newAccessBonusBrain(t, WithAccessBonusCap(10))
			storeLTM(t, b, tt.storeOrder...)
			setAccess(t, b, "low.foo", tt.low, 2*day)
			setAccess(t, b, "high.foo", tt.high, 2*day)

			assert.Equal(t, []string{"high.foo", "low.foo"}, recallKeys(t, b, "foo"))
		})
	}
}

// With a small cap, counts 50 and 500 both saturate to the same bonus, so a
// small salience edge decides the order; without the cap, count dominates.
func TestAccessBonus_CapFlattensCount(t *testing.T) {
	tests := []struct {
		name  string
		cap   float64
		order []string
	}{
		{"capped: salience decides", 0.05, []string{"few.foo", "many.foo"}},
		{"uncapped: count dominates", 1000, []string{"many.foo", "few.foo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newAccessBonusBrain(t, WithAccessBonusCap(tt.cap), WithSalienceWeight(1))
			storeLTM(t, b, "few.foo", "many.foo")
			_, err := b.db.Exec(`UPDATE brain_ltm SET salience = CASE key WHEN 'few.foo' THEN 0.6 ELSE 0.5 END`)
			require.NoError(t, err)
			setAccess(t, b, "few.foo", 50, day)
			setAccess(t, b, "many.foo", 500, day)

			results, err := b.Recall(testCtx("user1"), "foo", 20)
			require.NoError(t, err)
			require.Len(t, results, 2)
			if tt.cap < 1 {
				for _, e := range results {
					assert.InDelta(t, tt.cap, b.computeAccessBonus(e), 1e-12, "%s should hit the cap", e.Key)
				}
			}
			assert.Equal(t, tt.order, []string{results[0].Key, results[1].Key})
		})
	}
}

// Recency inside the bonus is computed at query time from accessed_at: aging
// a row's accessed_at via SQL alone (no Store) drops its bonus-driven rank.
func TestAccessBonus_RecencyComputedAtQueryTime(t *testing.T) {
	b := newAccessBonusBrain(t, WithAccessBonusCap(10))
	storeLTM(t, b, "a.foo", "b.foo")
	setAccess(t, b, "a.foo", 5, day)
	setAccess(t, b, "b.foo", 3, day)

	assert.Equal(t, []string{"a.foo", "b.foo"}, recallKeys(t, b, "foo"))

	// That recall bumped both rows (a: 6, b: 4, accessed now). Age only a.foo:
	// 6*0.1*0.95^60 ≈ 0.028 < 4*0.1 = 0.4.
	_, err := b.db.Exec(`UPDATE brain_ltm SET accessed_at = ? WHERE key = 'a.foo'`, sqlTime(60*day))
	require.NoError(t, err)

	assert.Equal(t, []string{"b.foo", "a.foo"}, recallKeys(t, b, "foo"))
}

// setupParity builds a full LTM (capacity 5, all rows outside the grace
// window) where old.1 and old.2 have equal salience, warmth and accessed_at
// and differ only in access_count; the other rows are clearly higher value.
func setupParity(t *testing.T, opts ...Option) *Brain {
	t.Helper()
	b := newTestBrain(t, append([]Option{WithMaxLTMEntries(5), WithSpreadingEnabled(false)}, opts...)...)
	fillAndAgeLTM(t, b, 5, 2*time.Hour)
	_, err := b.db.Exec(`UPDATE brain_ltm SET salience = 0.9, warmth = 0, created_at = ?`, sqlTime(30*day))
	require.NoError(t, err)
	_, err = b.db.Exec(`UPDATE brain_ltm SET salience = 0.5 WHERE key IN ('old.1', 'old.2')`)
	require.NoError(t, err)
	setAccess(t, b, "old.1", 30, 10*day) // frequently used
	setAccess(t, b, "old.2", 1, 10*day)  // rarely used
	return b
}

// Eviction ranks with the same access bonus as Recall: of two old rows with
// equal salience, the rarely-accessed one is evicted and Recall ranks it
// below the frequently-accessed one.
func TestAccessBonus_RecallEvictionParity(t *testing.T) {
	// Recall side (separate brain: Recall bumps accessed_at into the grace window).
	rb := setupParity(t)
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
	assert.Less(t, idx("old.1"), idx("old.2"), "recall should rank the frequently-accessed row higher")

	// Eviction side.
	eb := setupParity(t)
	require.NoError(t, eb.Store(testCtx("user1"), "new.fact", "v", TierLongTerm, ""))
	assert.True(t, ltmHas(t, eb, "new.fact"))
	assert.True(t, ltmHas(t, eb, "old.1"), "frequently-accessed old row must survive")
	assert.False(t, ltmHas(t, eb, "old.2"), "rarely-accessed row with equal salience should be evicted")
	assert.Equal(t, "capacity", archiveReason(t, eb, "old.2"))
	assert.Equal(t, 5, ltmCount(t, eb))

	// With the bonus weighted out, access_count no longer protects old.1:
	// the two rows tie on score, so either may go — but exactly one does.
	zb := setupParity(t, WithAccessBonusWeight(0))
	require.NoError(t, zb.Store(testCtx("user1"), "new.fact", "v", TierLongTerm, ""))
	assert.NotEqual(t, ltmHas(t, zb, "old.1"), ltmHas(t, zb, "old.2"),
		"exactly one of the tied rows should be evicted")
}
