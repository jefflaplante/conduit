package brain

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ageWM backdates a WM entry's AccessedAt (test-only; same package).
func ageWM(b *Brain, userID, key string, idle time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.working[userID][key]; ok {
		e.AccessedAt = time.Now().Add(-idle)
	}
}

func wmHas(b *Brain, userID, key string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.working[userID][key]
	return ok
}

func wmTestLTMHas(t *testing.T, b *Brain, key string) bool {
	t.Helper()
	var n int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE key = ?`, key).Scan(&n))
	return n > 0
}

// conduit-31jg.29: with the default weights and evict threshold, a WM entry
// touched once and idle for hours must be evicted by autoFlush. Before the
// fix the constant tier term (0.5*0.2 = 0.1) kept every WM salience >= 0.1,
// so `salience < evictThreshold` could never be true.
func TestWMEvictionFires_AutoFlush(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("u1")
	require.NoError(t, b.Store(ctx, "cold.fact", "v", TierWorking, "tool"))
	require.NoError(t, b.Store(ctx, "fresh.fact", "v", TierWorking, "tool"))
	ageWM(b, "u1", "cold.fact", 6*time.Hour)

	b.autoFlush()

	assert.False(t, wmHas(b, "u1", "cold.fact"), "idle, rarely-used WM entry should be evicted")
	assert.True(t, wmHas(b, "u1", "fresh.fact"), "recently used WM entry must survive")
	assert.False(t, wmTestLTMHas(t, b, "cold.fact"), "a cold entry is dropped, not promoted")
}

func TestWMEvictionFires_Consolidate(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("u1")
	require.NoError(t, b.Store(ctx, "cold.fact", "v", TierWorking, "tool"))
	ageWM(b, "u1", "cold.fact", 6*time.Hour)

	report, err := b.Consolidate(ctx, true)
	require.NoError(t, err)
	assert.Contains(t, report.EvictedKeys, "cold.fact")
	assert.False(t, wmHas(b, "u1", "cold.fact"))
}

// Frequently-used ("hot") WM entries are rescued into LTM instead of being
// dropped when they go idle — REM runs nightly, far later than the idle
// eviction window, so without this they'd be lost before REM could promote.
func TestWMEviction_HotEntryPromotedNotDropped(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false), WithHeatPromotionThreshold(3))
	ctx := testCtx("u1")
	require.NoError(t, b.Store(ctx, "hot.fact", "keep me", TierWorking, "tool"))
	for i := 0; i < 3; i++ {
		_, err := b.Get(ctx, "hot.fact")
		require.NoError(t, err)
	}
	ageWM(b, "u1", "hot.fact", 6*time.Hour)

	b.autoFlush()

	assert.False(t, wmHas(b, "u1", "hot.fact"))
	assert.True(t, wmTestLTMHas(t, b, "hot.fact"), "hot entry must be promoted to LTM before leaving WM")
}

func TestWMEviction_HotEntryKeptWhenAutoPromoteOff(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false), WithHeatPromotionThreshold(3), WithAutoPromote(false))
	ctx := testCtx("u1")
	require.NoError(t, b.Store(ctx, "hot.fact", "keep me", TierWorking, "tool"))
	for i := 0; i < 3; i++ {
		_, _ = b.Get(ctx, "hot.fact")
	}
	ageWM(b, "u1", "hot.fact", 6*time.Hour)

	b.autoFlush()

	assert.True(t, wmHas(b, "u1", "hot.fact"), "without auto-promote a hot entry is kept, not lost")
	assert.False(t, wmTestLTMHas(t, b, "hot.fact"))
}

// The per-user cap bounds WM growth even when nothing is idle yet.
func TestWMPerUserCap(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false), WithMaxWMEntriesPerUser(5))
	ctx := testCtx("u1")
	for i := 0; i < 5; i++ {
		require.NoError(t, b.Store(ctx, fmt.Sprintf("k%d", i), "v", TierWorking, "tool"))
	}
	// k0 is the least recently used.
	ageWM(b, "u1", "k0", 30*time.Minute)
	require.NoError(t, b.Store(ctx, "k5", "v", TierWorking, "tool"))

	assert.Len(t, b.WorkingMemoryEntries(ctx), 5)
	assert.False(t, wmHas(b, "u1", "k0"), "least valuable entry is evicted at the cap")
	assert.True(t, wmHas(b, "u1", "k5"), "the entry just written is never the cap victim")

	// Another user's bucket is unaffected by u1's cap pressure.
	other := testCtx("u2")
	require.NoError(t, b.Store(other, "x", "v", TierWorking, "tool"))
	assert.Len(t, b.WorkingMemoryEntries(other), 1)

	// StoreBulk respects the cap too.
	var bulk []BulkEntry
	for i := 0; i < 10; i++ {
		bulk = append(bulk, BulkEntry{Key: fmt.Sprintf("bulk%d", i), Value: "v"})
	}
	require.NoError(t, b.StoreBulk(ctx, bulk))
	assert.LessOrEqual(t, len(b.WorkingMemoryEntries(ctx)), 5)
}
