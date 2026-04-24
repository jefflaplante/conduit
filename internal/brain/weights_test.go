package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecallWeights_DefaultsUnchanged(t *testing.T) {
	b := newTestBrain(t)
	assert.InDelta(t, 0.5, b.matchWeight, 1e-9)
	assert.InDelta(t, 0.3, b.salienceWeight, 1e-9)
	assert.InDelta(t, 0.2, b.warmthWeight, 1e-9)
}

func TestRecallWeights_Configurable(t *testing.T) {
	b := newTestBrain(t,
		WithMatchWeight(0.1),
		WithSalienceWeight(0.2),
		WithWarmthWeight(0.7),
	)
	assert.InDelta(t, 0.1, b.matchWeight, 1e-9)
	assert.InDelta(t, 0.2, b.salienceWeight, 1e-9)
	assert.InDelta(t, 0.7, b.warmthWeight, 1e-9)
}

func TestRecallWeights_WarmthHeavyReorders(t *testing.T) {
	// With warmth dominating, an entry with high warmth but weaker match
	// should rank ahead of a stronger match with no warmth.
	b := newTestBrain(t,
		WithMatchWeight(0.0),
		WithSalienceWeight(0.0),
		WithWarmthWeight(1.0),
	)
	ctx := testCtx("user1")

	require.NoError(t, b.Store(ctx, "cold.foo", "foo foo foo", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "warm.foo", "foo", TierLongTerm, "tool"))

	// Prime warmth on warm.foo.
	_, err := b.db.Exec(`UPDATE brain_ltm SET warmth = 0.9 WHERE key = 'warm.foo'`)
	require.NoError(t, err)

	results, err := b.RecallWithContext(ctx, "foo", 20, "")
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// With warmthWeight=1.0, warm.foo should be ranked first.
	assert.Equal(t, "warm.foo", results[0].Key, "warmth-heavy weighting should rank warm entry first")
}
