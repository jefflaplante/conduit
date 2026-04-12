package rem

import (
	"context"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/reflection"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedReflectionEntries inserts test reflection data into brain_reflections.
func seedReflectionEntries(t *testing.T, rem *REMCycle, entries []*reflection.ReflectionEntry) {
	t.Helper()
	store := reflection.NewStore(rem.db)
	require.NoError(t, store.InsertBatch(context.Background(), entries))
}

func TestReflect_ClustersWrittenToBrain(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// Seed 4 WebFetch failures across 2 sessions — should form a cluster (>= 3)
	now := time.Now()
	entries := []*reflection.ReflectionEntry{
		{
			ID: "r1", SessionKey: "sess-1", Timestamp: now.Add(-3 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 200 * time.Millisecond,
			Insight: "connection timeout",
		},
		{
			ID: "r2", SessionKey: "sess-1", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 300 * time.Millisecond,
			Insight: "connection refused",
		},
		{
			ID: "r3", SessionKey: "sess-2", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 250 * time.Millisecond,
			Insight: "DNS resolution failed",
		},
		{
			ID: "r4", SessionKey: "sess-2", Timestamp: now.Add(-30 * time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 150 * time.Millisecond,
			Insight: "connection timeout",
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 4, result.EntriesProcessed)
	assert.Equal(t, 1, result.ClustersFound)

	// Verify cluster written to Brain LTM
	entry, err := b.Get(ctx, "reflect.clusters.WebFetch.failure")
	require.NoError(t, err)
	require.NotNil(t, entry, "cluster entry should exist in Brain LTM")
	assert.Contains(t, entry.Value, "Tool WebFetch has 4 failures")
	assert.Contains(t, entry.Value, "Avg duration:")
}

func TestReflect_EntriesMarkedProcessed(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	entries := []*reflection.ReflectionEntry{
		{
			ID: "p1", SessionKey: "sess-1", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 50 * time.Millisecond,
		},
		{
			ID: "p2", SessionKey: "sess-1", Timestamp: now.Add(-30 * time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 40 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, entries)

	_, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	// Verify entries are now marked as processed
	store := reflection.NewStore(rem.db)
	unprocessed, err := store.QueryUnprocessed(ctx)
	require.NoError(t, err)
	assert.Empty(t, unprocessed, "all entries should be marked as processed after reflect")
}

func TestReflect_HeuristicScoring(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()

	// Session with no failures: should get score=4
	// Session with some failures: should get score=2
	// Session with all failures: should get score=1
	entries := []*reflection.ReflectionEntry{
		// Session A: 3 successes, 0 failures → score 4
		{
			ID: "a1", SessionKey: "sess-a", Timestamp: now.Add(-3 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 50 * time.Millisecond,
		},
		{
			ID: "a2", SessionKey: "sess-a", Timestamp: now.Add(-2*time.Hour - 50*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WriteFile",
			Outcome: reflection.OutcomeSuccess, Duration: 60 * time.Millisecond,
		},
		{
			ID: "a3", SessionKey: "sess-a", Timestamp: now.Add(-2*time.Hour - 40*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "Bash",
			Outcome: reflection.OutcomeSuccess, Duration: 100 * time.Millisecond,
		},
		{
			ID: "a-summary", SessionKey: "sess-a", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeSessionSummary,
			Outcome: reflection.OutcomeSuccess, Score: 0, // unscored
		},
		// Session B: 2 successes, 1 failure → score 2
		{
			ID: "b1", SessionKey: "sess-b", Timestamp: now.Add(-1*time.Hour - 30*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 200 * time.Millisecond,
		},
		{
			ID: "b2", SessionKey: "sess-b", Timestamp: now.Add(-1*time.Hour - 20*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 30 * time.Millisecond,
		},
		{
			ID: "b3", SessionKey: "sess-b", Timestamp: now.Add(-1*time.Hour - 10*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WriteFile",
			Outcome: reflection.OutcomeSuccess, Duration: 40 * time.Millisecond,
		},
		{
			ID: "b-summary", SessionKey: "sess-b", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeSessionSummary,
			Outcome: reflection.OutcomeSuccess, Score: 0, // unscored
		},
		// Session C: 2 failures, 0 successes → score 1
		{
			ID: "c1", SessionKey: "sess-c", Timestamp: now.Add(-40 * time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 300 * time.Millisecond,
		},
		{
			ID: "c2", SessionKey: "sess-c", Timestamp: now.Add(-35 * time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeTimeout, Duration: 5000 * time.Millisecond,
		},
		{
			ID: "c-summary", SessionKey: "sess-c", Timestamp: now.Add(-30 * time.Minute),
			Source: "system", Type: reflection.TypeSessionSummary,
			Outcome: reflection.OutcomeSuccess, Score: 0, // unscored
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 3, result.ScoresBackfilled)

	// Verify scores in DB
	var scoreA, scoreB, scoreC int
	err = rem.db.QueryRow("SELECT score FROM brain_reflections WHERE id = ?", "a-summary").Scan(&scoreA)
	require.NoError(t, err)
	assert.Equal(t, 4, scoreA, "session with no failures should get score 4")

	err = rem.db.QueryRow("SELECT score FROM brain_reflections WHERE id = ?", "b-summary").Scan(&scoreB)
	require.NoError(t, err)
	assert.Equal(t, 2, scoreB, "session with some failures should get score 2")

	err = rem.db.QueryRow("SELECT score FROM brain_reflections WHERE id = ?", "c-summary").Scan(&scoreC)
	require.NoError(t, err)
	assert.Equal(t, 1, scoreC, "session with all failures should get score 1")
}

func TestReflect_NoUnprocessedEntries(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	// No entries seeded — should be a clean no-op
	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 0, result.EntriesProcessed)
	assert.Equal(t, 0, result.ClustersFound)
	assert.Equal(t, 0, result.ScoresBackfilled)
}

func TestReflect_DryRunNoSideEffects(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	entries := []*reflection.ReflectionEntry{
		{
			ID: "d1", SessionKey: "sess-1", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 200 * time.Millisecond,
		},
		{
			ID: "d2", SessionKey: "sess-1", Timestamp: now.Add(-1*time.Hour - 30*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 300 * time.Millisecond,
		},
		{
			ID: "d3", SessionKey: "sess-2", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 250 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, true) // dry run
	require.NoError(t, err)

	assert.Equal(t, 3, result.EntriesProcessed)
	assert.Equal(t, 1, result.ClustersFound)

	// Entries should NOT be marked as processed
	store := reflection.NewStore(rem.db)
	unprocessed, err := store.QueryUnprocessed(ctx)
	require.NoError(t, err)
	assert.Len(t, unprocessed, 3, "dry run should not mark entries as processed")

	// No cluster should be written to Brain
	entry, err := b.Get(ctx, "reflect.clusters.WebFetch.failure")
	require.NoError(t, err)
	assert.Nil(t, entry, "dry run should not write clusters to Brain")
}

func TestReflect_ClusterThreshold(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	// Only 2 failures — below the threshold of 3, should not create a cluster
	entries := []*reflection.ReflectionEntry{
		{
			ID: "t1", SessionKey: "sess-1", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "Bash",
			Outcome: reflection.OutcomeFailure, Duration: 100 * time.Millisecond,
		},
		{
			ID: "t2", SessionKey: "sess-2", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "Bash",
			Outcome: reflection.OutcomeFailure, Duration: 150 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 2, result.EntriesProcessed)
	assert.Equal(t, 0, result.ClustersFound, "should not form cluster with fewer than 3 entries")
}

func TestReflect_MultipleClusters(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	entries := []*reflection.ReflectionEntry{
		// 3 WebFetch failures
		{
			ID: "wf1", SessionKey: "sess-1", Timestamp: now.Add(-3 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 200 * time.Millisecond,
		},
		{
			ID: "wf2", SessionKey: "sess-1", Timestamp: now.Add(-2*time.Hour - 30*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 300 * time.Millisecond,
		},
		{
			ID: "wf3", SessionKey: "sess-2", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WebFetch",
			Outcome: reflection.OutcomeFailure, Duration: 250 * time.Millisecond,
		},
		// 4 ReadFile successes
		{
			ID: "rf1", SessionKey: "sess-1", Timestamp: now.Add(-1*time.Hour - 30*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 30 * time.Millisecond,
		},
		{
			ID: "rf2", SessionKey: "sess-1", Timestamp: now.Add(-1*time.Hour - 20*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 25 * time.Millisecond,
		},
		{
			ID: "rf3", SessionKey: "sess-2", Timestamp: now.Add(-1*time.Hour - 10*time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 35 * time.Millisecond,
		},
		{
			ID: "rf4", SessionKey: "sess-2", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 28 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 7, result.EntriesProcessed)
	assert.Equal(t, 2, result.ClustersFound, "should find 2 clusters: WebFetch failures and ReadFile successes")

	// Both clusters should be in Brain
	wfEntry, err := b.Get(ctx, "reflect.clusters.WebFetch.failure")
	require.NoError(t, err)
	require.NotNil(t, wfEntry)
	assert.Contains(t, wfEntry.Value, "Tool WebFetch has 3 failures")

	rfEntry, err := b.Get(ctx, "reflect.clusters.ReadFile.success")
	require.NoError(t, err)
	require.NotNil(t, rfEntry)
	assert.Contains(t, rfEntry.Value, "Tool ReadFile has 4 successes")
}

func TestReflect_ScoreSkipsSummaryWithNoToolOutcomes(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	// Session with only a summary, no tool outcomes — should not be scored
	entries := []*reflection.ReflectionEntry{
		{
			ID: "s-only", SessionKey: "sess-empty", Timestamp: now.Add(-1 * time.Hour),
			Source: "system", Type: reflection.TypeSessionSummary,
			Outcome: reflection.OutcomeSuccess, Score: 0,
		},
	}
	seedReflectionEntries(t, rem, entries)

	result, err := rem.Reflect(ctx, false)
	require.NoError(t, err)

	assert.Equal(t, 0, result.ScoresBackfilled, "should not score summary with no tool outcomes")

	// Score should remain 0
	var score int
	err = rem.db.QueryRow("SELECT score FROM brain_reflections WHERE id = ?", "s-only").Scan(&score)
	require.NoError(t, err)
	assert.Equal(t, 0, score)
}

func TestPrune_GroomsReflectionTable(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")

	now := time.Now()
	store := reflection.NewStore(rem.db)

	// Insert old processed entries (should be groomed)
	oldEntries := []*reflection.ReflectionEntry{
		{
			ID: "old1", SessionKey: "sess-old", Timestamp: now.Add(-45 * 24 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 50 * time.Millisecond,
		},
		{
			ID: "old2", SessionKey: "sess-old", Timestamp: now.Add(-40 * 24 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WriteFile",
			Outcome: reflection.OutcomeSuccess, Duration: 60 * time.Millisecond,
		},
	}
	require.NoError(t, store.InsertBatch(ctx, oldEntries))
	// Mark them as processed
	require.NoError(t, store.MarkProcessed(ctx, []string{"old1", "old2"}))

	// Insert recent processed entry (should be retained)
	recentProcessed := []*reflection.ReflectionEntry{
		{
			ID: "recent1", SessionKey: "sess-recent", Timestamp: now.Add(-5 * 24 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "Bash",
			Outcome: reflection.OutcomeSuccess, Duration: 100 * time.Millisecond,
		},
	}
	require.NoError(t, store.InsertBatch(ctx, recentProcessed))
	require.NoError(t, store.MarkProcessed(ctx, []string{"recent1"}))

	// Insert old unprocessed entry (should be retained — not yet processed by REM)
	unprocessed := []*reflection.ReflectionEntry{
		{
			ID: "unproc1", SessionKey: "sess-old-unproc", Timestamp: now.Add(-50 * 24 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "Bash",
			Outcome: reflection.OutcomeFailure, Duration: 200 * time.Millisecond,
		},
	}
	require.NoError(t, store.InsertBatch(ctx, unprocessed))

	// Run pruning (PruneAgeDays=30 by default)
	result, err := rem.Prune(ctx, false)
	require.NoError(t, err)

	// Old processed entries should be groomed
	assert.Equal(t, 2, result.ReflectionsGroomed, "old processed entries should be groomed")

	// Verify remaining entries
	var count int
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_reflections").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "recent processed + old unprocessed should remain")

	// Verify specific entries
	var exists int
	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_reflections WHERE id = ?", "recent1").Scan(&exists)
	require.NoError(t, err)
	assert.Equal(t, 1, exists, "recent processed entry should be retained")

	err = rem.db.QueryRow("SELECT COUNT(*) FROM brain_reflections WHERE id = ?", "unproc1").Scan(&exists)
	require.NoError(t, err)
	assert.Equal(t, 1, exists, "old unprocessed entry should be retained")
}

func TestReflect_SecondRunProcessesOnlyNew(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()

	ctx := brain.WithUserID(context.Background(), "testuser")
	now := time.Now()

	// First batch
	batch1 := []*reflection.ReflectionEntry{
		{
			ID: "batch1-1", SessionKey: "sess-1", Timestamp: now.Add(-2 * time.Hour),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "ReadFile",
			Outcome: reflection.OutcomeSuccess, Duration: 30 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, batch1)

	result1, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.EntriesProcessed)

	// Second batch
	batch2 := []*reflection.ReflectionEntry{
		{
			ID: "batch2-1", SessionKey: "sess-2", Timestamp: now.Add(-30 * time.Minute),
			Source: "system", Type: reflection.TypeToolOutcome, Tool: "WriteFile",
			Outcome: reflection.OutcomeSuccess, Duration: 40 * time.Millisecond,
		},
	}
	seedReflectionEntries(t, rem, batch2)

	result2, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result2.EntriesProcessed, "second run should only process new entries")
}
