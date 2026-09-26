package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.31: Recall's LTM LIKE terms must treat % and _ literally.
// Before the fix "100%" was the pattern %100%% and matched any value
// containing "100".
func TestRecallLIKEEscapesWildcards(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "disk.nas", "usage at 100% today", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "inventory.count", "1000 items in stock", TierLongTerm, "tool"))
	require.NoError(t, b.Store(ctx, "path.win", `C:\temp\100 files`, TierLongTerm, "tool"))

	res, err := b.Recall(ctx, "100%", 10)
	require.NoError(t, err)
	var keys []string
	for _, e := range res {
		keys = append(keys, e.Key)
	}
	assert.Equal(t, []string{"disk.nas"}, keys)

	// Backslash (the ESCAPE char) in a query is literal too.
	res, err = b.Recall(ctx, `temp\100`, 10)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "path.win", res[0].Key)
}

// conduit-179p multi-word OR recall still works end to end.
func TestRecallMultiWordOR(t *testing.T) {
	b := newTestBrain(t, WithSpreadingEnabled(false))
	ctx := testCtx("u")
	require.NoError(t, b.Store(ctx, "jeff.drink", "Jeff drinks Blanton's bourbon", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "jeff.work", "Jeff works at Acme", TierLongTerm, "user"))
	require.NoError(t, b.Store(ctx, "garden.plan", "tomatoes", TierLongTerm, "user"))

	res, err := b.Recall(ctx, "what bourbon does Jeff drink", 10)
	require.NoError(t, err)
	require.NotEmpty(t, res)
	assert.Equal(t, "jeff.drink", res[0].Key, "entry matching most terms ranks first")
	var keys []string
	for _, e := range res {
		keys = append(keys, e.Key)
	}
	assert.Contains(t, keys, "jeff.work", "OR logic: partial matches are returned")
	assert.NotContains(t, keys, "garden.plan")
}
