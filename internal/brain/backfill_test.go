package brain

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackfillEdges tests the one-shot LTM edge backfill functionality
func TestBackfillEdges(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create 20 LTM keys across different namespaces
	keys := []string{
		"solar.panel_count", "solar.battery_soc", "solar.inverter", "solar.config",
		"house.address", "house.zip", "house.owner",
		"learned.memory.alpha", "learned.memory.beta", "learned.memory.gamma",
		"portfolio.vti", "portfolio.vxus", "portfolio.nvda",
		"projects.converge", "projects.deck", "projects.pipefitter",
		"pets.theo", "pets.claire", "pets.oscar",
		"jeff.birthday", "jeff.email",
	}

	for _, key := range keys {
		require.NoError(t, b.Store(ctx, key, "test_value", TierLongTerm, "test"))
	}

	// Flush to create edges for newly written keys only
	require.NoError(t, b.flushPendingEdges())

	// Count total LTM entries
	var totalLTM int
	require.NoError(t, b.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&totalLTM))
	assert.Equal(t, len(keys), totalLTM)

	// Manually delete edges to simulate historical data with no edges
	_, err := b.db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// Verify all nodes are now isolated
	var isolatedCount int
	require.NoError(t, b.db.QueryRow(`
		SELECT COUNT(*) FROM brain_ltm
		WHERE key NOT IN (SELECT key_a FROM brain_relationships)
		  AND key NOT IN (SELECT key_b FROM brain_relationships)
	`).Scan(&isolatedCount))
	// Initially all should be isolated
	require.Greater(t, isolatedCount, len(keys)/2, "should have mostly isolated nodes before backfill")

	// Run backfill with per-node cap of 3 and global cap of 30
	report, err := b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 3,
		GlobalCap:  30,
	})
	require.NoError(t, err)

	// Verify report
	assert.Greater(t, report.EdgesCreated, 0, "should have created edges")
	assert.Greater(t, report.NodesProcessed, 0, "should have processed nodes")
	assert.Equal(t, len(keys), report.TotalNodes)

	// Verify caps were respected
	assert.LessOrEqual(t, report.EdgesCreated, report.GlobalCap, "should respect global cap")

	// Verify isolated nodes decreased
	var isolatedAfter int
	require.NoError(t, b.db.QueryRow(`
		SELECT COUNT(*) FROM brain_ltm
		WHERE key NOT IN (SELECT key_a FROM brain_relationships)
		  AND key NOT IN (SELECT key_b FROM brain_relationships)
	`).Scan(&isolatedAfter))
	assert.Less(t, isolatedAfter, isolatedCount, "should have reduced isolated nodes")
}

// TestBackfillEdgesRespectsPerNodeCap tests that each node gets at most PerNodeCap edges
func TestBackfillEdgesRespectsPerNodeCap(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create keys with strong namespace overlap (solar.*)
	solarKeys := []string{
		"solar.panel_count", "solar.battery_soc", "solar.inverter", "solar.config",
		"solar.output", "solar.input", "solar.max_power", "solar.min_power",
		"solar.efficiency", "solar.temperature",
	}

	for _, key := range solarKeys {
		require.NoError(t, b.Store(ctx, key, "test", TierLongTerm, "test"))
	}

	// Clear edges to simulate historical data
	require.NoError(t, b.flushPendingEdges())
	_, err := b.db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// Run backfill with per-node cap of 2
	_, err = b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 2,
		GlobalCap:  1000, // High global cap to test per-node cap
	})
	require.NoError(t, err)

	// Verify no node has more than 2 edges
	for _, key := range solarKeys {
		var edgeCount int
		require.NoError(t, b.db.QueryRow(`
			SELECT COUNT(*) FROM brain_relationships
			WHERE key_a = ? OR key_b = ?
		`, key, key).Scan(&edgeCount))
		assert.LessOrEqual(t, edgeCount, 2, "node %s should have at most 2 edges", key)
	}
}

// TestBackfillEdgesRespectsGlobalCap tests that total edges created respects global cap
func TestBackfillEdgesRespectsGlobalCap(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create 50 keys across various namespaces
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("namespace%d.key%d", i%5, i)
		require.NoError(t, b.Store(ctx, key, "test", TierLongTerm, "test"))
	}

	// Clear edges
	require.NoError(t, b.flushPendingEdges())
	_, err := b.db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// Run backfill with global cap of 10
	report, err := b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 5,
		GlobalCap:  10,
	})
	require.NoError(t, err)

	// Verify exactly 10 edges created
	assert.Equal(t, 10, report.EdgesCreated, "should create exactly global cap edges")
}

// TestBackfillEdgesNoDuplicates tests that existing edges are not duplicated
func TestBackfillEdgesNoDuplicates(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create keys
	keys := []string{"solar.panel", "solar.battery", "house.address"}
	for _, key := range keys {
		require.NoError(t, b.Store(ctx, key, "test", TierLongTerm, "test"))
	}

	// Create an edge manually
	require.NoError(t, b.StoreRelationship("solar.panel", "solar.battery", "manual", 0.7))

	// Run backfill
	_, err := b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 5,
		GlobalCap:  1000,
	})
	require.NoError(t, err)

	// Verify we didn't duplicate the existing edge
	var count int
	require.NoError(t, b.db.QueryRow(
		"SELECT COUNT(*) FROM brain_relationships WHERE (key_a = ? AND key_b = ?) OR (key_a = ? AND key_b = ?)",
		"solar.panel", "solar.battery", "solar.battery", "solar.panel",
	).Scan(&count))
	assert.Equal(t, 1, count, "should not duplicate existing edge")
}

// TestBackfillEdgesNoEdgesForNoOverlap tests that keys with no namespace overlap don't get edges
func TestBackfillEdgesNoEdgesForNoOverlap(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create keys with zero namespace overlap
	keys := []string{"a.b.c", "x.y.z", "one.two.three", "four.five.six"}
	for _, key := range keys {
		require.NoError(t, b.Store(ctx, key, "test", TierLongTerm, "test"))
	}

	// Clear edges
	require.NoError(t, b.flushPendingEdges())
	_, err := b.db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// Run backfill
	report, err := b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 5,
		GlobalCap:  1000,
	})
	require.NoError(t, err)

	// With minPrefix=4 in namespacePrefixes, cross-namespace candidates are never
	// fetched, so zero-overlap keys must produce zero edges.
	assert.Equal(t, 0, report.EdgesCreated, "no-overlap keys should get no edges")
	var totalEdges int
	require.NoError(t, b.db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&totalEdges))
	assert.Equal(t, 0, totalEdges, "no-overlap keys should produce empty graph")
}

// TestBackfillEdgesWeightsWithinBounds tests that edge weights are within expected bounds
func TestBackfillEdgesWeightsWithinBounds(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Create keys
	keys := []string{"solar.a", "solar.b", "solar.c", "house.x", "house.y"}
	for _, key := range keys {
		require.NoError(t, b.Store(ctx, key, "test", TierLongTerm, "test"))
	}

	// Clear edges
	require.NoError(t, b.flushPendingEdges())
	_, err := b.db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// Run backfill
	_, err = b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 5,
		GlobalCap:  1000,
	})
	require.NoError(t, err)

	// Verify all confidence values are within bounds [0.3, 0.9]
	rows, err := b.db.Query("SELECT confidence FROM brain_relationships")
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var confidence float64
		require.NoError(t, rows.Scan(&confidence))
		assert.GreaterOrEqual(t, confidence, 0.3, "confidence should be >= 0.3")
		assert.LessOrEqual(t, confidence, 0.9, "confidence should be <= 0.9")
	}
}

// TestBackfillEdgesEmptyDatabase tests that backfill handles empty database gracefully
func TestBackfillEdgesEmptyDatabase(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(true))
	ctx := testCtx("user1")

	// Don't add any keys, just run backfill
	report, err := b.BackfillEdges(ctx, BackfillConfig{
		PerNodeCap: 5,
		GlobalCap:  1000,
	})
	require.NoError(t, err)

	assert.Equal(t, 0, report.EdgesCreated)
	assert.Equal(t, 0, report.NodesProcessed)
	assert.Equal(t, 0, report.TotalNodes)
}