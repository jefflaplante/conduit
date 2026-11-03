package searchdb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bd-lfrd: the brain FTS index must cover the new context column so
// MemorySearch finds entries by their situational context.
func TestBrainIndexer_IndexesContext(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	ctx := context.Background()
	_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source, context) VALUES ('svc.recovery', 'cache hit rate recovered', 'tool', 'billing spike investigation')`)
	require.NoError(t, err)
	_, err = brainDB.Exec(`INSERT INTO brain_ltm (key, value, source, context) VALUES ('svc.other', 'plain value', 'tool', '')`)
	require.NoError(t, err)

	require.NoError(t, indexer.IndexBrain(ctx))

	// Context token must hit through the FTS index.
	results, err := indexer.SearchBrain(ctx, "billing", 10)
	require.NoError(t, err)
	require.Len(t, results, 1, "a context-only token must be searchable")
	assert.Equal(t, "svc.recovery", results[0].Key)

	// Value tokens keep working.
	results, err = indexer.SearchBrain(ctx, "recovered", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "svc.recovery", results[0].Key)

	// Unrelated tokens return nothing.
	results, err = indexer.SearchBrain(ctx, "zebra", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// A context-only change must be picked up by the incremental hash.
func TestBrainIndexer_ContextChangeReindexes(t *testing.T) {
	indexer, brainDB, cleanup := setupBrainIndexerTest(t)
	defer cleanup()

	ctx := context.Background()
	_, err := brainDB.Exec(`INSERT INTO brain_ltm (key, value, source) VALUES ('k', 'v', 'tool')`)
	require.NoError(t, err)
	require.NoError(t, indexer.IndexBrain(ctx))

	results, err := indexer.SearchBrain(ctx, "postmortem", 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	_, err = brainDB.Exec(`UPDATE brain_ltm SET context = 'postmortem follow-up' WHERE key = 'k'`)
	require.NoError(t, err)
	require.NoError(t, indexer.IndexBrain(ctx))

	results, err = indexer.SearchBrain(ctx, "postmortem", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
}
