package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWarmthInject_HappyPath: a high-warmth LTM entry that does NOT match
// the query keywords is appended (tail) to recall results with WarmthHit=true.
func TestWarmthInject_HappyPath(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Keyword-matching entry.
	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "tool"))
	// High-warmth entry in an unrelated namespace — no keyword overlap with "solar".
	require.NoError(t, b.Store(ctx, "travel.paris_2026", "trip details", TierLongTerm, "tool"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = 0.95 WHERE key = 'travel.paris_2026'`)
	require.NoError(t, err)

	results, err := b.RecallWithContext(ctx, "solar", 10, "")
	require.NoError(t, err)
	require.NotEmpty(t, results)

	assert.Equal(t, "solar.panel_count", results[0].Key, "keyword match stays first")
	var injected []*Entry
	for _, r := range results {
		if r.WarmthHit {
			injected = append(injected, r)
		}
	}
	require.Len(t, injected, 1, "expected exactly one warmth-injected entry")
	assert.Equal(t, "travel.paris_2026", injected[0].Key)
	assert.True(t, injected[0].WarmthHit)
}

// TestWarmthInject_NoInjectionOnEmptyResults: recall that matches nothing
// must not inject warm entries (guard against polluting miss-driven queries).
func TestWarmthInject_NoInjectionOnEmptyResults(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "travel.paris_2026", "trip details", TierLongTerm, "tool"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = 0.95 WHERE key = 'travel.paris_2026'`)
	require.NoError(t, err)

	results, err := b.RecallWithContext(ctx, "quantum_entanglement", 10, "")
	require.NoError(t, err)
	assert.Empty(t, results, "no keyword hits -> no warmth injection")
}

// TestWarmthInject_RespectsLimitAndSeen: limit headroom bounds injection,
// and entries already seen (keyword/cluster hits) are not duplicated.
func TestWarmthInject_RespectsLimitAndSeen_Real(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "tool"))
	// Three warm entries, no keyword overlap with "solar".
	for _, k := range []string{"a.alpha_note", "b.beta_note", "c.gamma_note"} {
		require.NoError(t, b.Store(ctx, k, "note "+k, TierLongTerm, "tool"))
		_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = 0.9 WHERE key = ?`, k)
		require.NoError(t, err)
	}

	// limit=2: one keyword hit + at most one injected (headroom = 2-1 = 1).
	results, err := b.RecallWithContext(ctx, "solar", 2, "")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(results), 2)
	warmthHits := 0
	for _, r := range results {
		if r.WarmthHit {
			warmthHits++
		}
	}
	assert.LessOrEqual(t, warmthHits, 1)

	// Warm entry that IS a keyword match must not be duplicated.
	require.NoError(t, b.Store(ctx, "solar.warm_match", "solar thermal data", TierLongTerm, "tool"))
	_, err = b.db.Exec(`UPDATE brain_ltm SET warmth = 0.9 WHERE key = 'solar.warm_match'`)
	require.NoError(t, err)
	results, err = b.RecallWithContext(ctx, "solar", 10, "")
	require.NoError(t, err)
	seen := map[string]int{}
	for _, r := range results {
		seen[r.Key]++
	}
	assert.Equal(t, 1, seen["solar.warm_match"], "warm keyword-matching entry appears exactly once")
}

// TestWarmthInject_DisabledByZero: warmthInjectLimit=0 is the kill-switch.
func TestWarmthInject_DisabledByZero(t *testing.T) {
	b := newTestBrain(t, WithWarmthInjectLimit(0))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "travel.paris_2026", "trip details", TierLongTerm, "tool"))
	_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = 0.95 WHERE key = 'travel.paris_2026'`)
	require.NoError(t, err)

	results, err := b.RecallWithContext(ctx, "solar", 10, "")
	require.NoError(t, err)
	for _, r := range results {
		assert.False(t, r.WarmthHit, "kill-switch must suppress all injection")
	}
}
