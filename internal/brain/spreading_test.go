package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpreadActivationWithNamespaceEdges verifies that spreading activation
// propagates warmth to neighbouring keys via brain_relationships edges.
func TestSpreadActivationWithNamespaceEdges(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Store keys in LTM with known salience.
	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.battery_soc", "85", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "house.address", "123 Main St", TierLongTerm, "user"))

	// Verify warmth is zero before spreading.
	w0, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, w0, 0.001)

	wHouse, err := b.GetWarmth("house.address")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, wHouse, 0.001)

	// Create edges: solar.panel_count <-> solar.battery_soc (namespace), solar.panel_count <-> house.address (manual).
	require.NoError(t, b.StoreRelationship("solar.panel_count", "solar.battery_soc", "namespace", 0.7))
	require.NoError(t, b.StoreRelationship("solar.panel_count", "house.address", "manual", 0.5))

	// Trigger spreading activation from solar.panel_count.
	err = b.spreadActivation([]string{"solar.panel_count"})
	require.NoError(t, err)

	// Neighbouring key should have received warmth boost.
	// Expected: decay(0.5) * salience(~0.5 for fresh) * confidence(0.7) ≈ 0.175
	w1, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	assert.Greater(t, w1, 0.0, "solar.battery_soc should have received warmth from spreading")

	// house.address should also have warmth from the manual edge.
	// Expected: decay(0.5) * salience(~0.5) * confidence(0.5) ≈ 0.125
	wH, err := b.GetWarmth("house.address")
	require.NoError(t, err)
	assert.Greater(t, wH, 0.0, "house.address should have received warmth from spreading")
}

// TestSpreadActivationNoEdges verifies that spreading activation is a clean
// no-op when there are no relationships in the graph.
func TestSpreadActivationNoEdges(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "config"))

	// No edges exist — should return no error.
	err := b.spreadActivation([]string{"solar.panel_count"})
	require.NoError(t, err)

	// Warmth should remain zero.
	w, err := b.GetWarmth("solar.panel_count")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, w, 0.001)
}

// TestSpreadActivationDisabled verifies that spreading is skipped when
// spreadingEnabled is false.
func TestSpreadActivationDisabled(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.battery_soc", "85", TierLongTerm, "config"))
	require.NoError(t, b.StoreRelationship("solar.panel_count", "solar.battery_soc", "namespace", 0.9))

	err := b.spreadActivation([]string{"solar.panel_count"})
	require.NoError(t, err)

	// Should be zero — spreading is disabled.
	w, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, w, 0.001)
}

// TestSpreadActivationEmptyKeys verifies that an empty accessedKeys slice
// results in a no-op.
func TestSpreadActivationEmptyKeys(t *testing.T) {
	b := newTestBrain(t)

	err := b.spreadActivation([]string{})
	require.NoError(t, err)

	err = b.spreadActivation(nil)
	require.NoError(t, err)
}

// TestDecayWarmthThreshold verifies that DecayWarmth zeroes out entries that
// fall below the 0.01 threshold and preserves entries above it.
func TestDecayWarmthThreshold(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.battery_soc", "85", TierLongTerm, "config"))

	// Set warmth directly via spread activation with an edge.
	require.NoError(t, b.StoreRelationship("solar.panel_count", "solar.battery_soc", "namespace", 1.0))
	_ = b.spreadActivation([]string{"solar.panel_count"})

	wBefore, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	require.Greater(t, wBefore, 0.0, "should have warmth before decay")

	// Apply decay with factor 0.5 — repeat until below threshold.
	for i := 0; i < 20; i++ {
		err := b.DecayWarmth(0.5)
		require.NoError(t, err)
	}

	// After 20 halvings, any starting value should be well below 0.01 and zeroed.
	w, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, w, 0.001, "warmth should be zeroed after falling below threshold")
}

// TestDecayWarmthPreservesAboveThreshold verifies that warmth above the
// threshold survives a single decay pass.
func TestDecayWarmthPreservesAboveThreshold(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.panel_count", "30", TierLongTerm, "config"))
	require.NoError(t, b.Store(ctx, "solar.battery_soc", "85", TierLongTerm, "config"))

	require.NoError(t, b.StoreRelationship("solar.panel_count", "solar.battery_soc", "namespace", 1.0))
	_ = b.spreadActivation([]string{"solar.panel_count"})

	wBefore, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	require.Greater(t, wBefore, 0.0)

	// Single decay with factor 0.95 — should still be above threshold.
	err = b.DecayWarmth(0.95)
	require.NoError(t, err)

	wAfter, err := b.GetWarmth("solar.battery_soc")
	require.NoError(t, err)
	assert.Greater(t, wAfter, 0.0, "warmth should survive one decay cycle")
	assert.Less(t, wAfter, wBefore, "warmth should decrease after decay")
}

// TestGetWarmthMissingKey verifies that GetWarmth returns 0 for keys that
// don't exist in LTM.
func TestGetWarmthMissingKey(t *testing.T) {
	b := newTestBrain(t)

	w, err := b.GetWarmth("nonexistent.key")
	require.NoError(t, err)
	assert.InDelta(t, 0.0, w, 0.001)
}

// TestStoreRelationshipCanonicalOrdering verifies that StoreRelationship
// stores edges with lexicographic ordering (key_a < key_b) regardless of
// input order.
func TestStoreRelationshipCanonicalOrdering(t *testing.T) {
	b := newTestBrain(t)

	// Insert with reverse order: zebra comes before alpha lexically should be
	// stored as (alpha, zebra).
	require.NoError(t, b.StoreRelationship("zebra.key", "alpha.key", "test", 0.5))

	// Verify the edge exists with canonical ordering.
	row := b.db.QueryRow(
		"SELECT key_a, key_b FROM brain_relationships WHERE key_a = ? AND key_b = ?",
		"alpha.key", "zebra.key",
	)
	var keyA, keyB string
	err := row.Scan(&keyA, &keyB)
	require.NoError(t, err, "should find edge with canonical ordering (alpha.key, zebra.key)")
	assert.Equal(t, "alpha.key", keyA)
	assert.Equal(t, "zebra.key", keyB)

	// Verify the reverse ordering does NOT exist.
	row2 := b.db.QueryRow(
		"SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? AND key_b = ?",
		"zebra.key", "alpha.key",
	)
	var count int
	require.NoError(t, row2.Scan(&count))
	assert.Equal(t, 0, count, "reverse ordering should not exist")
}

// TestStoreRelationshipSameKey verifies that StoreRelationship handles the
// edge case where both keys are equal (no reordering needed).
func TestStoreRelationshipSameKey(t *testing.T) {
	b := newTestBrain(t)

	// Same key — no reordering needed, but it's a degenerate case.
	require.NoError(t, b.StoreRelationship("same.key", "same.key", "self", 0.1))
}

// TestSpreadActivationMultipleSources verifies that spreading from multiple
// source keys produces the maximum boost for each neighbour.
func TestSpreadActivationMultipleSources(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.a", "1", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.b", "2", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.c", "3", TierLongTerm, "test"))

	// Both solar.a and solar.b connect to solar.c with different confidence.
	require.NoError(t, b.StoreRelationship("solar.a", "solar.c", "namespace", 0.5))
	require.NoError(t, b.StoreRelationship("solar.b", "solar.c", "namespace", 0.9))

	err := b.spreadActivation([]string{"solar.a", "solar.b"})
	require.NoError(t, err)

	w, err := b.GetWarmth("solar.c")
	require.NoError(t, err)
	assert.Greater(t, w, 0.0, "solar.c should have warmth from multiple sources")
}

// TestAutoFlushNoReentrantLockDeadlock is a regression test for a deadlock
// where autoFlush held b.mu across a call to flushPendingEdges, which itself
// re-acquires b.mu. The symptom in production was the gateway hanging on
// every Brain.Store/Get/Recall while slash commands (which don't hit Brain)
// kept working. We invoke autoFlush with pending edge work queued and require
// it to finish in well under the autoFlush period.
func TestAutoFlushNoReentrantLockDeadlock(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "solar.battery.config", "1", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.battery.plan", "2", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "solar.inverter", "3", TierLongTerm, "test"))
	require.NotEmpty(t, b.pendingEdgeKeys, "expected pending edges queued after Store")

	done := make(chan struct{})
	go func() {
		b.autoFlush()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("autoFlush deadlocked — flushPendingEdges tried to re-acquire b.mu")
	}

	// Post-flush: a concurrent Store must still be able to grab the lock.
	require.NoError(t, b.Store(ctx, "solar.battery.state", "4", TierLongTerm, "test"))
}

// TestSpreadActivationWarmthCappedAtOne verifies that warmth is capped at 1.0
// even with multiple high-confidence edges.
func TestSpreadActivationWarmthCappedAtOne(t *testing.T) {
	b := newTestBrain(t, WithSpreadingDecay(1.0)) // decay=1.0 means no decay
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "src.a", "1", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "src.b", "2", TierLongTerm, "test"))
	require.NoError(t, b.Store(ctx, "target", "3", TierLongTerm, "test"))

	require.NoError(t, b.StoreRelationship("src.a", "target", "strong", 1.0))
	require.NoError(t, b.StoreRelationship("src.b", "target", "strong", 1.0))

	err := b.spreadActivation([]string{"src.a", "src.b"})
	require.NoError(t, err)

	w, err := b.GetWarmth("target")
	require.NoError(t, err)
	assert.LessOrEqual(t, w, 1.0, "warmth should be capped at 1.0")
}
