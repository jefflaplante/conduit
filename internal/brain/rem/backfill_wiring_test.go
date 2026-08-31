package rem

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"conduit/internal/brain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConsolidate_WithBackfill tests that consolidate triggers bounded backfill
func TestConsolidate_WithBackfill(t *testing.T) {
	// Setup: enable spreading so backfill can work
	dbPath := t.TempDir() + "/brain.db"
	b, err := brain.New(dbPath, brain.WithSpreadingEnabled(true))
	require.NoError(t, err)
	defer b.Close()

	// Open DB directly for queries and manual edge manipulation
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	rem := NewREMCycle(b, db, REMConfig{
		ConsolidateThreshold: 0.6,
		MaxLTMEntries:        10000,
		SalienceDecayRate:    0.1,
	})
	ctx := context.Background()

	// Create 20 LTM keys across different namespaces
	// Store does not auto-create edges - edges are only created via spreadActivation on Get
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("solar.node%02d", i)
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))
	}

	// Verify no edges exist initially (Store doesn't create edges)
	var edgeCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&edgeCount))
	require.Equal(t, 0, edgeCount, "should start with no edges")

	// Create a few edges manually to simulate some connected nodes
	_, err = db.Exec(`
		INSERT INTO brain_relationships (key_a, key_b, relationship, confidence)
		VALUES ('solar.node00', 'solar.node01', 'manual', 0.9),
		       ('solar.node02', 'solar.node03', 'manual', 0.9)
	`)
	require.NoError(t, err)

	// Verify some edges exist now
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&edgeCount))
	require.Equal(t, 2, edgeCount)

	// Verify many nodes are isolated
	var isolatedCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM brain_ltm l
		WHERE NOT EXISTS (
			SELECT 1 FROM brain_relationships r
			WHERE r.key_a = l.key OR r.key_b = l.key
		)
	`).Scan(&isolatedCount))
	require.Greater(t, isolatedCount, 0, "should have some isolated nodes")

	// First consolidate: should trigger bounded backfill for isolated nodes
	result, err := rem.Consolidate(ctx, false)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify backfill metrics are set
	assert.Greater(t, result.BackfilledNodes, 0, "backfill should process some nodes")
	assert.Greater(t, result.BackfilledEdges, 0, "backfill should create some edges")

	// Verify edges were created
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&edgeCount))
	assert.Greater(t, edgeCount, 2, "consolidate should backfill edges for isolated nodes")
}

// TestConsolidate_BackfillRespectsCap tests that per-node cap is enforced
func TestConsolidate_BackfillRespectsCap(t *testing.T) {
	dbPath := t.TempDir() + "/brain.db"
	b, err := brain.New(dbPath, brain.WithSpreadingEnabled(true))
	require.NoError(t, err)
	defer b.Close()

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	rem := NewREMCycle(b, db, REMConfig{
		ConsolidateThreshold: 0.6,
		MaxLTMEntries:        10000,
		SalienceDecayRate:    0.1,
	})
	ctx := context.Background()

	// Create 20 keys in the SAME namespace with spreading enabled
	for i := 0; i < 20; i++ {
		key := "solar.deep.namespace.key" + string(rune('a'+i))
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))
	}

	// Manually delete all edges to simulate historical nodes
	_, err = db.Exec("DELETE FROM brain_relationships")
	require.NoError(t, err)

	// First consolidate: backfill with per-node cap=5
	_, err = rem.Consolidate(ctx, false)
	require.NoError(t, err)

	// Check that no node exceeds the per-node cap
	var overCapCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM (
			SELECT key_a, COUNT(*) as edge_count
			FROM brain_relationships
			WHERE key_a LIKE 'solar.deep%'
			GROUP BY key_a
		)
		WHERE edge_count > 5
	`).Scan(&overCapCount))
	require.Equal(t, 0, overCapCount, "no node should exceed per-node cap")
}

// TestConsolidate_BackfillDoesNotDuplicateExistingEdges tests that backfill skips existing edges
func TestConsolidate_BackfillDoesNotDuplicateExistingEdges(t *testing.T) {
	dbPath := t.TempDir() + "/brain.db"
	b, err := brain.New(dbPath, brain.WithSpreadingEnabled(true))
	require.NoError(t, err)
	defer b.Close()

	// Open DB in read-write mode for manual edge insertion
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer db.Close()

	rem := NewREMCycle(b, db, REMConfig{
		ConsolidateThreshold: 0.6,
		MaxLTMEntries:        10000,
		SalienceDecayRate:    0.1,
	})
	ctx := context.Background()

	// Create 3 keys with spreading enabled to create some edges
	keys := []string{"solar.a", "solar.b", "solar.c"}
	for _, key := range keys {
		require.NoError(t, b.Store(ctx, key, "value", brain.TierLongTerm, "test"))
	}

	// Insert a manual edge between solar.a and solar.b
	_, err = db.Exec(`
		INSERT OR IGNORE INTO brain_relationships (key_a, key_b, relationship, confidence)
		VALUES ('solar.a', 'solar.b', 'manual', 0.9)
	`)
	require.NoError(t, err)

	// Verify at least one edge exists (the manual one)
	var count int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&count))
	require.GreaterOrEqual(t, count, 1)

	// Delete all auto-created edges, keep only the manual one
	_, err = db.Exec("DELETE FROM brain_relationships WHERE relationship != 'manual'")
	require.NoError(t, err)

	// Verify only the manual edge exists
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&count))
	require.Equal(t, 1, count)

	// Run consolidate (backfill should skip the existing edge)
	_, err = rem.Consolidate(ctx, false)
	require.NoError(t, err)

	// Edge count should increase but NOT duplicate the manual edge
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM brain_relationships").Scan(&count))
	assert.Greater(t, count, 1, "should have created new edges")

	// Verify no duplicates for the specific edge
	var duplicateCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*)
		FROM brain_relationships
		WHERE key_a = 'solar.a' AND key_b = 'solar.b'
	`).Scan(&duplicateCount))
	assert.Equal(t, 1, duplicateCount, "should not duplicate existing edge")
}