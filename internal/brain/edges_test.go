package brain

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceEdgeConfidence(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"two shared segments", "solar.battery.config", "solar.battery.plan", 0.9},
		{"one shared segment", "solar.battery.config", "solar.inverter", 0.6},
		{"no shared segments", "solar.foo", "house.foo", 0.3},
		{"three shared segments", "a.b.c.d", "a.b.c.e", 0.9},
		{"identical keys", "solar.x", "solar.x", 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, namespaceEdgeConfidence(tt.a, tt.b))
		})
	}
}

func TestFlushPendingEdges_CreatesNamespaceEdges(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Three solar.* LTM entries — stores queue all three into pendingEdgeKeys.
	require.NoError(t, b.Store(ctx, "solar.battery.config", "2x 14.6 kWh", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "solar.battery.plan", "charge overnight", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "solar.inverter_mode", "EG4", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "jeff.birthday", "Oct 5", TierLongTerm, "user"))

	require.NoError(t, b.flushPendingEdges())

	// After flush, the three solar.* keys should be fully-connected via
	// namespace edges; jeff.birthday shares no qualifying prefix with any.
	var count int
	err := b.db.QueryRow(
		`SELECT COUNT(*) FROM brain_relationships WHERE relationship = 'namespace'`,
	).Scan(&count)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, 3, "three solar.* keys should form at least 3 pairwise edges")

	// jeff.birthday shares "jeff" (4 chars, passes MinPrefixLength) with nothing,
	// so no edges should involve it.
	var jeffCount int
	err = b.db.QueryRow(
		`SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?`,
		"jeff.birthday", "jeff.birthday",
	).Scan(&jeffCount)
	require.NoError(t, err)
	assert.Equal(t, 0, jeffCount, "jeff.birthday should have no namespace edges")
}

func TestFlushPendingEdges_ClearsPending(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "learned.memory.a", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.memory.b", "v", TierLongTerm, "tool"))

	require.NoError(t, b.flushPendingEdges())

	b.mu.RLock()
	remaining := len(b.pendingEdgeKeys)
	b.mu.RUnlock()
	assert.Equal(t, 0, remaining, "pending queue should be empty after flush")

	// Second flush with empty queue is a no-op and should not error.
	require.NoError(t, b.flushPendingEdges())
}

func TestFlushPendingEdges_UpsertOnRepeat(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "learned.memory.a", "v1", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.memory.b", "v1", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	var before int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&before))

	// Store again — edges should upsert, not duplicate.
	require.NoError(t, b.Store(ctx, "learned.memory.a", "v2", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	var after int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&after))
	assert.Equal(t, before, after, "repeat flush should upsert, not duplicate edges")
}

func TestFlushPendingEdges_DisabledIsNoOp(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.a", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "solar.b", "v", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	var count int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count))
	assert.Equal(t, 0, count, "no edges should be created when spreading is disabled")
}

func TestFlushPendingEdges_SkipsShortPrefixes(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// "a.x" and "a.y" share prefix "a" which is shorter than MinPrefixLength=4
	// and should be filtered out.
	require.NoError(t, b.Store(ctx, "a.x", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "a.y", "v", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	var count int
	require.NoError(t, b.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count))
	assert.Equal(t, 0, count, "short prefix 'a' should be filtered")
}

func TestStoreLTM_QueuesEdgeKey(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.battery.config", "v", TierLongTerm, "tool"))

	b.mu.RLock()
	_, ok := b.pendingEdgeKeys["solar.battery.config"]
	b.mu.RUnlock()
	assert.True(t, ok, "LTM store should queue the key for edge creation")
}

func TestStoreBulk_QueuesEdgeKeys(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.StoreBulk(ctx, []BulkEntry{
		{Key: "learned.memory.a", Value: "v", Tier: TierLongTerm, Source: "tool"},
		{Key: "learned.memory.b", Value: "v", Tier: TierLongTerm, Source: "tool"},
		{Key: "session.x", Value: "v", Tier: TierWorking, Source: "tool"}, // WM should not queue
	}))

	b.mu.RLock()
	_, hasA := b.pendingEdgeKeys["learned.memory.a"]
	_, hasB := b.pendingEdgeKeys["learned.memory.b"]
	_, hasWM := b.pendingEdgeKeys["session.x"]
	b.mu.RUnlock()
	assert.True(t, hasA)
	assert.True(t, hasB)
	assert.False(t, hasWM, "working-memory stores should not queue edge keys")
}

func TestEffectiveEdgeConfidence(t *testing.T) {
	const alpha = 0.1

	tests := []struct {
		name        string
		confidence  float64
		accessCount int
		wantApprox  float64 // approximate expected value
	}{
		{"zero accesses — no boost", 0.9, 0, 0.9},
		{"1 access — small boost", 0.9, 1, 0.9 * (1 + 0.1*math.Log1p(1))},
		{"10 accesses — noticeable boost", 0.9, 10, 0.9 * (1 + 0.1*math.Log1p(10))},
		{"100 accesses — significant boost", 0.6, 100, 0.6 * (1 + 0.1*math.Log1p(100))},
		{"1000 accesses — diminishing returns", 0.5, 1000, 0.5 * (1 + 0.1*math.Log1p(1000))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveEdgeConfidence(tt.confidence, tt.accessCount, alpha)
			assert.InDelta(t, tt.wantApprox, got, 0.001)
		})
	}
}

func TestSpreadActivation_IncrementsEdgeAccessCount(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store two related LTM keys.
	require.NoError(t, b.Store(ctx, "solar.battery.config", "2x 14.6kWh", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "solar.battery.mode", "self-use", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	// Verify edge exists with access_count=0.
	var accessCount int
	require.NoError(t, b.db.QueryRow(
		`SELECT access_count FROM brain_relationships WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%' LIMIT 1`,
	).Scan(&accessCount))
	assert.Equal(t, 0, accessCount, "edge should start with access_count=0")

	// Access one key to trigger spreading activation.
	_, err := b.Get(ctx, "solar.battery.config")
	require.NoError(t, err)

	// The edge should now have access_count > 0.
	require.NoError(t, b.db.QueryRow(
		`SELECT access_count FROM brain_relationships WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%' LIMIT 1`,
	).Scan(&accessCount))
	assert.Equal(t, 1, accessCount, "edge access_count should be incremented after spreading activation")

	// Access again — should increment to 2.
	_, err = b.Get(ctx, "solar.battery.config")
	require.NoError(t, err)
	require.NoError(t, b.db.QueryRow(
		`SELECT access_count FROM brain_relationships WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%' LIMIT 1`,
	).Scan(&accessCount))
	assert.Equal(t, 2, accessCount, "edge access_count should increment on each traversal")
}

func TestDecayEdgeConfidence_DecaysAccessCount(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Seed an edge with a high access_count via direct DB insert.
	require.NoError(t, b.Store(ctx, "solar.battery.config", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "solar.battery.mode", "v", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	// Manually set access_count to a known value.
	_, err := b.db.Exec(`UPDATE brain_relationships SET access_count = 100 WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%'`)
	require.NoError(t, err)

	// Decay with a factor of 0.5, idleWindow=0 (all edges decay), pruneThreshold=0.1.
	require.NoError(t, b.DecayEdgeConfidence(0.85, 0.1, 0))

	// access_count should be halved: 100 * 0.95 = 95 (using default edgeAccessDecay=0.95).
	var accessCount int
	require.NoError(t, b.db.QueryRow(
		`SELECT access_count FROM brain_relationships WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%' LIMIT 1`,
	).Scan(&accessCount))
	assert.Equal(t, 95, accessCount, "access_count should decay by edgeAccessDecay factor")

	// Repeated decay should eventually zero out. Use decayFactor=1.0 so the
	// edge isn't pruned (confidence stays put); this test isolates the
	// access_count decay path from confidence-based pruning.
	for i := 0; i < 100; i++ {
		require.NoError(t, b.DecayEdgeConfidence(1.0, 0.1, 0))
	}
	require.NoError(t, b.db.QueryRow(
		`SELECT access_count FROM brain_relationships WHERE key_a LIKE 'solar%' AND key_b LIKE 'solar%' LIMIT 1`,
	).Scan(&accessCount))
	assert.Equal(t, 0, accessCount, "access_count should reach zero after many decays")
}

func TestUsageWeightedBoost_StrongerThanUnweighted(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Set up two independent pairs of related keys with same confidence.
	require.NoError(t, b.Store(ctx, "learned.test.alpha", "v1", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.test.beta", "v2", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.demo.gamma", "v3", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.demo.delta", "v4", TierLongTerm, "tool"))
	require.NoError(t, b.flushPendingEdges())

	// Give the demo pair a high access_count manually.
	_, err := b.db.Exec(`UPDATE brain_relationships SET access_count = 50 WHERE key_a LIKE 'learned.demo%' AND key_b LIKE 'learned.demo%'`)
	require.NoError(t, err)

	// Set equal salience for all entries.
	_, err = b.db.Exec(`UPDATE brain_ltm SET salience = 0.8`)
	require.NoError(t, err)

	// Trigger spreading from a key that has edges to both pairs.
	// Use "learned.test.alpha" which is only connected to "learned.test.beta".
	_, err = b.Get(ctx, "learned.test.alpha")
	require.NoError(t, err)

	// The test pair should have lower warmth (no usage boost) vs what demo would have.
	// But they're independent pairs, so we check that demo pair's warmth is higher
	// when activated from its own side.
	warmthTestBeta, _ := b.GetWarmth("learned.test.beta")
	assert.Greater(t, warmthTestBeta, 0.0, "neighbour should have warmth from spreading")

	// Now activate demo pair and compare — it should get a stronger boost.
	_, err = b.Get(ctx, "learned.demo.gamma")
	require.NoError(t, err)
	warmthDemoDelta, _ := b.GetWarmth("learned.demo.delta")
	assert.Greater(t, warmthDemoDelta, warmthTestBeta,
		"edge with higher access_count should produce stronger warmth boost")
}
