package reflection

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/database"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// createReflectionsSQL is the same DDL as brain migration 4.
const createReflectionsSQL = `
CREATE TABLE IF NOT EXISTS brain_reflections (
	id TEXT PRIMARY KEY,
	session_key TEXT NOT NULL,
	timestamp DATETIME NOT NULL,
	source TEXT NOT NULL,
	type TEXT NOT NULL,
	tool TEXT,
	outcome TEXT NOT NULL,
	retry_count INTEGER DEFAULT 0,
	duration_ms INTEGER DEFAULT 0,
	insight TEXT,
	score INTEGER DEFAULT 0,
	tags TEXT,
	related_keys TEXT,
	rem_processed INTEGER DEFAULT 0
);

CREATE INDEX idx_reflections_session ON brain_reflections (session_key);
CREATE INDEX idx_reflections_timestamp ON brain_reflections (timestamp);
CREATE INDEX idx_reflections_tool ON brain_reflections (tool);
CREATE INDEX idx_reflections_type ON brain_reflections (type);
CREATE INDEX idx_reflections_rem ON brain_reflections (rem_processed);
`

func newTestStore(t *testing.T) *ReflectionStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test_reflect.db")
	db, err := sql.Open("sqlite", database.BuildDSN(dbPath))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(createReflectionsSQL)
	require.NoError(t, err)

	return NewStore(db)
}

func makeEntry(id, session, tool string, outcome Outcome, ts time.Time) *ReflectionEntry {
	return &ReflectionEntry{
		ID:         id,
		SessionKey: session,
		Timestamp:  ts,
		Source:     "system",
		Type:       TypeToolOutcome,
		Tool:       tool,
		Outcome:    outcome,
		RetryCount: 0,
		Duration:   150 * time.Millisecond,
		Insight:    "test insight",
		Score:      0,
		Tags:       []string{"test"},
		RelatedKeys: []string{"brain.key1"},
	}
}

func TestInsertAndQueryBySession(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entry := &ReflectionEntry{
		ID:          "entry-001",
		SessionKey:  "sess-abc",
		Timestamp:   now,
		Source:      "system",
		Type:        TypeToolOutcome,
		Tool:        "BashTool",
		Outcome:     OutcomeSuccess,
		RetryCount:  2,
		Duration:    500 * time.Millisecond,
		Insight:     "command completed quickly",
		Score:       4,
		Tags:        []string{"bash", "fast"},
		RelatedKeys: []string{"reflect.tools.bash.perf"},
	}

	err := store.Insert(ctx, entry)
	require.NoError(t, err)

	results, err := store.QueryBySession(ctx, "sess-abc")
	require.NoError(t, err)
	require.Len(t, results, 1)

	got := results[0]
	assert.Equal(t, "entry-001", got.ID)
	assert.Equal(t, "sess-abc", got.SessionKey)
	assert.Equal(t, now, got.Timestamp)
	assert.Equal(t, "system", got.Source)
	assert.Equal(t, TypeToolOutcome, got.Type)
	assert.Equal(t, "BashTool", got.Tool)
	assert.Equal(t, OutcomeSuccess, got.Outcome)
	assert.Equal(t, 2, got.RetryCount)
	assert.Equal(t, 500*time.Millisecond, got.Duration)
	assert.Equal(t, "command completed quickly", got.Insight)
	assert.Equal(t, 4, got.Score)
	assert.Equal(t, []string{"bash", "fast"}, got.Tags)
	assert.Equal(t, []string{"reflect.tools.bash.perf"}, got.RelatedKeys)
}

func TestInsertAndQueryBySession_NilTagsAndRelatedKeys(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entry := &ReflectionEntry{
		ID:         "entry-nil",
		SessionKey: "sess-nil",
		Timestamp:  now,
		Source:     "model",
		Type:       TypeLearned,
		Outcome:    OutcomeSuccess,
	}

	err := store.Insert(ctx, entry)
	require.NoError(t, err)

	results, err := store.QueryBySession(ctx, "sess-nil")
	require.NoError(t, err)
	require.Len(t, results, 1)

	got := results[0]
	assert.Nil(t, got.Tags)
	assert.Nil(t, got.RelatedKeys)
	assert.Empty(t, got.Tool)
	assert.Empty(t, got.Insight)
}

func TestInsertBatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entries := []*ReflectionEntry{
		makeEntry("batch-1", "sess-b", "ReadFile", OutcomeSuccess, now),
		makeEntry("batch-2", "sess-b", "WriteFile", OutcomeFailure, now.Add(time.Second)),
		makeEntry("batch-3", "sess-b", "BashTool", OutcomeSuccess, now.Add(2*time.Second)),
	}

	err := store.InsertBatch(ctx, entries)
	require.NoError(t, err)

	results, err := store.QueryBySession(ctx, "sess-b")
	require.NoError(t, err)
	assert.Len(t, results, 3)

	// Verify ordering by timestamp
	assert.Equal(t, "batch-1", results[0].ID)
	assert.Equal(t, "batch-2", results[1].ID)
	assert.Equal(t, "batch-3", results[2].ID)
}

func TestInsertBatch_Empty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	err := store.InsertBatch(ctx, nil)
	assert.NoError(t, err)

	err = store.InsertBatch(ctx, []*ReflectionEntry{})
	assert.NoError(t, err)
}

func TestQueryUnprocessed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Insert two entries
	e1 := makeEntry("unp-1", "sess-u", "BashTool", OutcomeSuccess, now)
	e2 := makeEntry("unp-2", "sess-u", "ReadFile", OutcomeFailure, now.Add(time.Second))
	require.NoError(t, store.Insert(ctx, e1))
	require.NoError(t, store.Insert(ctx, e2))

	// Both should be unprocessed
	results, err := store.QueryUnprocessed(ctx)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestMarkProcessed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	e1 := makeEntry("mp-1", "sess-m", "BashTool", OutcomeSuccess, now)
	e2 := makeEntry("mp-2", "sess-m", "ReadFile", OutcomeFailure, now.Add(time.Second))
	require.NoError(t, store.Insert(ctx, e1))
	require.NoError(t, store.Insert(ctx, e2))

	// Mark first as processed
	err := store.MarkProcessed(ctx, []string{"mp-1"})
	require.NoError(t, err)

	// Only mp-2 should remain unprocessed
	results, err := store.QueryUnprocessed(ctx)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "mp-2", results[0].ID)
}

func TestMarkProcessed_AllEntries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	e1 := makeEntry("mpa-1", "sess-ma", "BashTool", OutcomeSuccess, now)
	e2 := makeEntry("mpa-2", "sess-ma", "ReadFile", OutcomeFailure, now.Add(time.Second))
	require.NoError(t, store.Insert(ctx, e1))
	require.NoError(t, store.Insert(ctx, e2))

	// Mark both as processed
	err := store.MarkProcessed(ctx, []string{"mpa-1", "mpa-2"})
	require.NoError(t, err)

	// No unprocessed entries left
	results, err := store.QueryUnprocessed(ctx)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestMarkProcessed_EmptyIDs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Should not error with nil or empty slice
	assert.NoError(t, store.MarkProcessed(ctx, nil))
	assert.NoError(t, store.MarkProcessed(ctx, []string{}))
}

func TestGroom(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// "Old" entry: 60 days ago, processed
	old := makeEntry("groom-old", "sess-g", "BashTool", OutcomeSuccess,
		time.Now().UTC().Add(-60*24*time.Hour).Truncate(time.Second))
	require.NoError(t, store.Insert(ctx, old))
	require.NoError(t, store.MarkProcessed(ctx, []string{"groom-old"}))

	// "Recent" entry: 5 days ago, processed
	recent := makeEntry("groom-recent", "sess-g", "BashTool", OutcomeSuccess,
		time.Now().UTC().Add(-5*24*time.Hour).Truncate(time.Second))
	require.NoError(t, store.Insert(ctx, recent))
	require.NoError(t, store.MarkProcessed(ctx, []string{"groom-recent"}))

	// "Unprocessed" entry: 60 days ago, NOT processed
	unprocessed := makeEntry("groom-unproc", "sess-g", "ReadFile", OutcomeFailure,
		time.Now().UTC().Add(-60*24*time.Hour).Truncate(time.Second))
	require.NoError(t, store.Insert(ctx, unprocessed))

	// Groom with 30-day retention
	deleted, err := store.Groom(ctx, 30)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "should only delete old + processed entry")

	// Verify remaining entries
	results, err := store.QueryBySession(ctx, "sess-g")
	require.NoError(t, err)
	assert.Len(t, results, 2)

	ids := make(map[string]bool)
	for _, r := range results {
		ids[r.ID] = true
	}
	assert.True(t, ids["groom-recent"], "recent processed entry should survive")
	assert.True(t, ids["groom-unproc"], "old unprocessed entry should survive")
	assert.False(t, ids["groom-old"], "old processed entry should be deleted")
}

func TestQueryToolStats(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	entries := []*ReflectionEntry{
		{
			ID: "ts-1", SessionKey: "sess-ts", Timestamp: now,
			Source: "system", Type: TypeToolOutcome, Tool: "BashTool",
			Outcome: OutcomeSuccess, RetryCount: 0, Duration: 100 * time.Millisecond,
		},
		{
			ID: "ts-2", SessionKey: "sess-ts", Timestamp: now.Add(time.Second),
			Source: "system", Type: TypeToolOutcome, Tool: "BashTool",
			Outcome: OutcomeSuccess, RetryCount: 2, Duration: 300 * time.Millisecond,
		},
		{
			ID: "ts-3", SessionKey: "sess-ts", Timestamp: now.Add(2 * time.Second),
			Source: "system", Type: TypeToolOutcome, Tool: "BashTool",
			Outcome: OutcomeFailure, RetryCount: 3, Duration: 500 * time.Millisecond,
		},
		{
			ID: "ts-4", SessionKey: "sess-ts", Timestamp: now.Add(3 * time.Second),
			Source: "system", Type: TypeToolOutcome, Tool: "ReadFile",
			Outcome: OutcomeSuccess, RetryCount: 0, Duration: 50 * time.Millisecond,
		},
		// Entry without tool name — should be excluded from tool stats
		{
			ID: "ts-5", SessionKey: "sess-ts", Timestamp: now.Add(4 * time.Second),
			Source: "model", Type: TypeSessionSummary,
			Outcome: OutcomeSuccess,
		},
	}

	require.NoError(t, store.InsertBatch(ctx, entries))

	stats, err := store.QueryToolStats(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, stats, 3, "BashTool/success, BashTool/failure, ReadFile/success")

	// Build a lookup map for easier assertion
	type key struct {
		Tool    string
		Outcome Outcome
	}
	lookup := make(map[key]ToolStat)
	for _, s := range stats {
		lookup[key{s.Tool, s.Outcome}] = s
	}

	// BashTool success: 2 entries, avg duration (100+300)/2 = 200ms, avg retries (0+2)/2 = 1.0
	bs := lookup[key{"BashTool", OutcomeSuccess}]
	assert.Equal(t, 2, bs.Count)
	assert.Equal(t, 200*time.Millisecond, bs.AvgDuration)
	assert.InDelta(t, 1.0, bs.AvgRetries, 0.01)

	// BashTool failure: 1 entry, 500ms, 3 retries
	bf := lookup[key{"BashTool", OutcomeFailure}]
	assert.Equal(t, 1, bf.Count)
	assert.Equal(t, 500*time.Millisecond, bf.AvgDuration)
	assert.InDelta(t, 3.0, bf.AvgRetries, 0.01)

	// ReadFile success: 1 entry, 50ms, 0 retries
	rs := lookup[key{"ReadFile", OutcomeSuccess}]
	assert.Equal(t, 1, rs.Count)
	assert.Equal(t, 50*time.Millisecond, rs.AvgDuration)
	assert.InDelta(t, 0.0, rs.AvgRetries, 0.01)
}

func TestQueryToolStats_SinceFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Entry from 2 days ago
	old := makeEntry("tss-old", "sess-tss", "BashTool", OutcomeSuccess,
		now.Add(-48*time.Hour))
	// Entry from now
	recent := makeEntry("tss-new", "sess-tss", "BashTool", OutcomeSuccess, now)

	require.NoError(t, store.Insert(ctx, old))
	require.NoError(t, store.Insert(ctx, recent))

	// Query since 1 hour ago — should only return the recent entry
	stats, err := store.QueryToolStats(ctx, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, 1, stats[0].Count)
}

func TestQueryBySession_NoResults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	results, err := store.QueryBySession(ctx, "nonexistent-session")
	require.NoError(t, err)
	assert.Empty(t, results)
}
