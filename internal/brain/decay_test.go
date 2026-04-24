package brain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecayEdgeConfidence_DecaysIdleEdges(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.x", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "a.y", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("a.x", "a.y", "namespace", 0.8))

	// last_traversed_at is NULL for a freshly-inserted edge, so any positive
	// idle window should qualify it for decay.
	require.NoError(t, b.DecayEdgeConfidence(0.5, 0.05, 0))

	var conf float64
	err := b.db.QueryRow(
		`SELECT confidence FROM brain_relationships WHERE key_a = 'a.x' AND key_b = 'a.y'`,
	).Scan(&conf)
	require.NoError(t, err)
	assert.InDelta(t, 0.4, conf, 1e-9, "NULL last_traversed_at should decay")
}

func TestDecayEdgeConfidence_PrunesBelowThreshold(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.x", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "a.y", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("a.x", "a.y", "namespace", 0.15))

	// Decay 0.15 * 0.5 = 0.075, below 0.1 → prune.
	require.NoError(t, b.DecayEdgeConfidence(0.5, 0.1, 0))

	var count int
	err := b.db.QueryRow(`SELECT COUNT(*) FROM brain_relationships`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "edge should be pruned once confidence drops below threshold")
}

func TestDecayEdgeConfidence_SkipsRecentlyTraversed(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.x", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "a.y", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("a.x", "a.y", "namespace", 0.8))

	// Stamp last_traversed_at to "now".
	_, err := b.db.Exec(
		`UPDATE brain_relationships SET last_traversed_at = datetime('now') WHERE key_a = 'a.x'`,
	)
	require.NoError(t, err)

	// Generous idle window (1 hour) — edge is within it, so decay should skip.
	require.NoError(t, b.DecayEdgeConfidence(0.5, 0.05, time.Hour))

	var conf float64
	err = b.db.QueryRow(
		`SELECT confidence FROM brain_relationships WHERE key_a = 'a.x' AND key_b = 'a.y'`,
	).Scan(&conf)
	require.NoError(t, err)
	assert.InDelta(t, 0.8, conf, 1e-9, "recently-traversed edge should not decay")
}

func TestDecayEdgeConfidence_DisabledIsNoOp(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "a.x", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "a.y", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("a.x", "a.y", "namespace", 0.8))

	require.NoError(t, b.DecayEdgeConfidence(0.1, 0.5, 0))

	var conf float64
	err := b.db.QueryRow(
		`SELECT confidence FROM brain_relationships WHERE key_a = 'a.x' AND key_b = 'a.y'`,
	).Scan(&conf)
	require.NoError(t, err)
	assert.InDelta(t, 0.8, conf, 1e-9, "disabled spreading → no decay")
}

func TestSpreadActivation_StampsLastTraversed(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "src", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "dst", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("src", "dst", "test", 0.8))

	// Before spread: last_traversed_at is NULL.
	var before *time.Time
	err := b.db.QueryRow(
		`SELECT last_traversed_at FROM brain_relationships WHERE key_a = 'dst' AND key_b = 'src'`,
	).Scan(&before)
	require.NoError(t, err)
	assert.Nil(t, before, "pre-spread should be NULL")

	require.NoError(t, b.spreadActivation([]string{"src"}))

	// After spread: last_traversed_at is populated.
	var after time.Time
	err = b.db.QueryRow(
		`SELECT last_traversed_at FROM brain_relationships WHERE key_a = 'dst' AND key_b = 'src'`,
	).Scan(&after)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), after, 5*time.Second, "traversal timestamp should be recent")
}
