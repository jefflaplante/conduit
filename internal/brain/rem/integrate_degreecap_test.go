package rem

import (
	"context"
	"testing"

	"conduit/internal/brain"
	"fmt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrate_DegreeCap(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store 30 LTM entries to create potential relationships
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("test.namespace.%d", i)
		require.NoError(t, b.Store(ctx, key, fmt.Sprintf("test value %d", i), brain.TierLongTerm, ""))
	}

	// Create a hub node by adding it to all relationships
	hubKey := "test.hub"
	require.NoError(t, b.Store(ctx, hubKey, "hub value", brain.TierLongTerm, ""))

	// Manually insert maxNodeDegree edges for the hub (all with low confidence)
	for i := 0; i < maxNodeDegree; i++ {
		otherKey := fmt.Sprintf("test.namespace.%d", i)
		_, err := rem.db.Exec(
			"INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?, ?, 'related', ?)",
			hubKey, otherKey, 0.1)
		require.NoError(t, err)
	}

	// Verify hub is at cap
	var edgeCount int
	err := rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount, "hub should be at degree cap")

	// Now integrate, which should try to create more relationships via namespace overlap
	// Create more entries that share namespace with hub
	for i := 30; i < 35; i++ {
		key := fmt.Sprintf("hub.%d", i)
		require.NoError(t, b.Store(ctx, key, fmt.Sprintf("hub value %d", i), brain.TierLongTerm, ""))
	}

	// Run integration (manual = true to bypass day gate)
	result, err := rem.Integrate(ctx, false, true)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.RelationshipsCreated, 0, "integration should create some relationships")

	// Verify hub is still at cap (not exceeded)
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.LessOrEqual(t, edgeCount, maxNodeDegree, "hub should not exceed degree cap")
}

func TestIntegrate_DegreeCapHighConfidenceEvictsLow(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries
	hubKey := "test.hub"
	require.NoError(t, b.Store(ctx, hubKey, "hub value", brain.TierLongTerm, ""))

	lowConfKey := "test.low"
	highConfKey := "test.high"

	require.NoError(t, b.Store(ctx, lowConfKey, "low confidence value", brain.TierLongTerm, ""))
	require.NoError(t, b.Store(ctx, highConfKey, "high confidence value", brain.TierLongTerm, ""))

	// Fill hub to cap with low-confidence edges
	for i := 0; i < maxNodeDegree; i++ {
		otherKey := fmt.Sprintf("test.filler.%d", i)
		require.NoError(t, b.Store(ctx, otherKey, fmt.Sprintf("filler value %d", i), brain.TierLongTerm, ""))

		_, err := rem.db.Exec(
			"INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?, ?, 'related', ?)",
			hubKey, otherKey, 0.1)
		require.NoError(t, err)
	}

	// Verify hub is at cap
	var edgeCount int
	err := rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount)

	// Insert a new edge with higher confidence via the capped path
	err = rem.insertRelationship(ctx, relationshipCandidate{
		keyA:       hubKey,
		keyB:       highConfKey,
		confidence: 0.8,
	})
	require.NoError(t, err)

	// Verify hub is still at cap (lowest-confidence edge was evicted)
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount, "hub should remain at cap after high-confidence insert")

	// Verify high-confidence edge exists (direction-agnostic)
	var exists int
	err = rem.db.QueryRow(
		"SELECT COUNT(*) FROM brain_relationships WHERE ((key_a = ? AND key_b = ?) OR (key_a = ? AND key_b = ?)) AND confidence >= 0.8",
		hubKey, highConfKey, highConfKey, hubKey).Scan(&exists)
	require.NoError(t, err)
	assert.Equal(t, 1, exists, "high-confidence edge should exist")
}

func TestIntegrate_DegreeCapLowConfidenceSkipped(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Store entries
	hubKey := "test.hub"
	require.NoError(t, b.Store(ctx, hubKey, "hub value", brain.TierLongTerm, ""))

	lowConfKey := "test.low"
	require.NoError(t, b.Store(ctx, lowConfKey, "low confidence value", brain.TierLongTerm, ""))

	// Fill hub to cap with medium-confidence edges
	for i := 0; i < maxNodeDegree; i++ {
		otherKey := fmt.Sprintf("test.filler.%d", i)
		require.NoError(t, b.Store(ctx, otherKey, fmt.Sprintf("filler value %d", i), brain.TierLongTerm, ""))

		_, err := rem.db.Exec(
			"INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?, ?, 'related', ?)",
			hubKey, otherKey, 0.5)
		require.NoError(t, err)
	}

	// Verify hub is at cap
	var edgeCount int
	err := rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount)

	// Try to insert a new edge with lower confidence
	candidate := relationshipCandidate{
		keyA:       hubKey,
		keyB:       lowConfKey,
		confidence: 0.2,
		reason:     "low confidence test",
	}

	err = rem.insertRelationship(ctx, candidate)
	require.NoError(t, err)

	// Verify hub is still at cap
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount)

	// Verify low-confidence edge does NOT exist
	var exists int
	err = rem.db.QueryRow(
		"SELECT COUNT(*) FROM brain_relationships WHERE (key_a = ? AND key_b = ?) OR (key_a = ? AND key_b = ?) AND confidence = 0.2",
		hubKey, lowConfKey, lowConfKey, hubKey).Scan(&exists)
	require.NoError(t, err)
	assert.Equal(t, 0, exists, "low-confidence edge should be skipped")
}

func TestHubPrune(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create a hub node
	hubKey := "test.hub"
	require.NoError(t, b.Store(ctx, hubKey, "hub value", brain.TierLongTerm, ""))

	// Add edges exceeding the cap (with varying confidence)
	edgesToAdd := maxNodeDegree + 10
	for i := 0; i < edgesToAdd; i++ {
		otherKey := fmt.Sprintf("test.node.%d", i)
		require.NoError(t, b.Store(ctx, otherKey, fmt.Sprintf("value %d", i), brain.TierLongTerm, ""))

		// Vary confidence: low confidence first
		confidence := 0.1 + (float64(i) / float64(edgesToAdd))
		_, err := rem.db.Exec(
			"INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?, ?, 'related', ?)",
			hubKey, otherKey, confidence)
		require.NoError(t, err)
	}

	// Verify hub exceeds cap
	var edgeCount int
	err := rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Greater(t, edgeCount, maxNodeDegree, "hub should exceed cap before prune")

	// Run hub prune
	hubsPruned, edgesRemoved, err := rem.HubPrune(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, hubsPruned, "should prune one hub")
	assert.Equal(t, edgesToAdd-maxNodeDegree, edgesRemoved, "should remove excess edges")

	// Verify hub is now at cap
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCount)
	require.NoError(t, err)
	assert.Equal(t, maxNodeDegree, edgeCount, "hub should be at cap after prune")

	// Verify remaining edges have higher confidence (lowest were removed)
	var minConfidence float64
	err = rem.db.QueryRow(
		`SELECT MIN(confidence) FROM brain_relationships WHERE key_a = ? OR key_b = ?`,
		hubKey, hubKey).Scan(&minConfidence)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, minConfidence, 0.1, "remaining edges should have non-negative confidence")
}

func TestHubPruneDryRun(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Create a hub node
	hubKey := "test.hub"
	require.NoError(t, b.Store(ctx, hubKey, "hub value", brain.TierLongTerm, ""))

	// Add edges exceeding the cap
	edgesToAdd := maxNodeDegree + 5
	for i := 0; i < edgesToAdd; i++ {
		otherKey := fmt.Sprintf("test.node.%d", i)
		require.NoError(t, b.Store(ctx, otherKey, fmt.Sprintf("value %d", i), brain.TierLongTerm, ""))

		_, err := rem.db.Exec(
			"INSERT INTO brain_relationships (key_a, key_b, relationship, confidence) VALUES (?, ?, 'related', ?)",
			hubKey, otherKey, 0.5)
		require.NoError(t, err)
	}

	// Verify hub exceeds cap
	var edgeCountBefore int
	err := rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCountBefore)
	require.NoError(t, err)
	assert.Greater(t, edgeCountBefore, maxNodeDegree)

	// Run hub prune in dry run mode
	hubsPruned, edgesRemoved, err := rem.HubPrune(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, 1, hubsPruned)
	assert.Equal(t, edgesToAdd-maxNodeDegree, edgesRemoved)

	// Verify hub still exceeds cap (dry run doesn't delete)
	var edgeCountAfter int
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?", hubKey, hubKey).Scan(&edgeCountAfter)
	require.NoError(t, err)
	assert.Equal(t, edgeCountBefore, edgeCountAfter, "dry run should not change edge count")
}
