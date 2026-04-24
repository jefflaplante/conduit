package brain

import (
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
