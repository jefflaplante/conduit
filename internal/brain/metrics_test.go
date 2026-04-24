package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatus_SpreadingMetricsPopulate(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Seed two namespace-sibling entries and an edge between them.
	require.NoError(t, b.Store(ctx, "learned.memory.a", "v", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.memory.b", "v", TierLongTerm, "tool"))
	require.NoError(t, b.StoreRelationship("learned.memory.a", "learned.memory.b", "namespace", 0.9))

	// Trigger spread + cluster expansion.
	require.NoError(t, b.spreadActivation([]string{"learned.memory.a"}))
	_, err := b.RecallWithContext(ctx, "learned", 20, "")
	require.NoError(t, err)

	status, err := b.Status(ctx)
	require.NoError(t, err)

	assert.Greater(t, status.SpreadEvents, int64(0), "should record at least one spread event")
	assert.Greater(t, status.AvgWarmthBoost, 0.0, "should record average warmth boost")
	assert.NotNil(t, status.EdgeCountByType)
	assert.Equal(t, 1, status.EdgeCountByType["namespace"], "namespace edge should be counted")
}

func TestStatus_ClusterHitRateReflectsExpansion(t *testing.T) {
	b := newTestBrain(t)
	ctx := testCtx("user1")

	// Three learned.memory.* keys; only one matches "spreading" as a keyword.
	require.NoError(t, b.Store(ctx, "learned.memory.spreading_activation", "Collins Loftus", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.memory.bi_temporal", "zep graphiti", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "learned.memory.encoding", "Tulving", TierLongTerm, "tool"))

	// Recall only the direct match triggers cluster expansion to bring in the siblings.
	_, err := b.RecallWithContext(ctx, "spreading", 20, "")
	require.NoError(t, err)

	status, err := b.Status(ctx)
	require.NoError(t, err)
	assert.Greater(t, status.ClusterHitRate, 0.0, "cluster expansion should contribute to hit rate")
	assert.LessOrEqual(t, status.ClusterHitRate, 1.0, "cluster hit rate is a ratio")
}
