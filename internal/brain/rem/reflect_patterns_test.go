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

// conduit-31jg.54: SPAR pivot/circular TypePattern entries are consolidated
// by REM Reflect into one Brain LTM entry per pattern (reflect.tools.*),
// thresholded on recurrence across sessions.

func pivotEntry(id, session, tool, errMsg string, ago time.Duration) *reflection.ReflectionEntry {
	return &reflection.ReflectionEntry{
		ID: id, SessionKey: session, Timestamp: time.Now().Add(-ago),
		Source: "system", Type: reflection.TypePattern, Tool: tool,
		Outcome: reflection.OutcomeFailure, RetryCount: 3, Insight: errMsg,
		Tags:        []string{"consecutive_failure"},
		RelatedKeys: []string{reflection.ConsecutiveFailureKey(tool)},
	}
}

func circularEntry(id, session, pattern, hash string, ago time.Duration) *reflection.ReflectionEntry {
	return &reflection.ReflectionEntry{
		ID: id, SessionKey: session, Timestamp: time.Now().Add(-ago),
		Source: "system", Type: reflection.TypePattern,
		Outcome: reflection.OutcomePartial, Insight: "circular tool-call pattern: " + pattern,
		Tags:        []string{"circular"},
		RelatedKeys: []string{reflection.CircularPatternKey(hash)},
	}
}

func ltmReflectToolsKeys(t *testing.T, r *REMCycle) []string {
	t.Helper()
	rows, err := r.db.Query(`SELECT key FROM brain_ltm WHERE key LIKE 'reflect.tools.%' ORDER BY key`)
	require.NoError(t, err)
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	return keys
}

func TestReflect_RepeatedPatternsPromotedToOneLTMEntry(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := context.Background()

	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		pivotEntry("p1", "sess-a", "WebFetch", "dial tcp: i/o timeout", 5*time.Hour),
		pivotEntry("p2", "sess-a", "WebFetch", "dial tcp: i/o timeout", 4*time.Hour),
		pivotEntry("p3", "sess-b", "WebFetch", "403 Forbidden", 1*time.Hour),
		circularEntry("c1", "sess-a", "Read→Grep", "abc123", 3*time.Hour),
		circularEntry("c2", "sess-c", "Read→Grep", "abc123", 2*time.Hour),
	})

	res, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 2, res.PatternsPromoted)
	assert.Equal(t,
		[]string{"reflect.tools.WebFetch.consecutive_failure", "reflect.tools.circular.abc123"},
		ltmReflectToolsKeys(t, rem))

	pivot, err := b.Get(ctx, "reflect.tools.WebFetch.consecutive_failure")
	require.NoError(t, err)
	require.NotNil(t, pivot)
	assert.Contains(t, pivot.Value, "WebFetch")
	assert.Contains(t, pivot.Value, "3 turns across 2 sessions")
	assert.Contains(t, pivot.Value, "403 Forbidden", "latest error is reported")
	assert.LessOrEqual(t, len(pivot.Value), 200, "fits the Situation Awareness line budget")
	assert.Equal(t, "system:rem-reflect", pivot.Source)

	circ, err := b.Get(ctx, "reflect.tools.circular.abc123")
	require.NoError(t, err)
	require.NotNil(t, circ)
	assert.Contains(t, circ.Value, "Read→Grep")
	assert.Contains(t, circ.Value, "2 turns across 2 sessions")

	// A later occurrence in another session updates the same entry (counts
	// are recomputed over the window, not appended).
	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		pivotEntry("p4", "sess-d", "WebFetch", "503 Service Unavailable", 10*time.Minute),
	})
	res, err = rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 1, res.PatternsPromoted, "only patterns with new evidence are rewritten")
	assert.Len(t, ltmReflectToolsKeys(t, rem), 2)
	pivot, err = b.Get(ctx, "reflect.tools.WebFetch.consecutive_failure")
	require.NoError(t, err)
	assert.Contains(t, pivot.Value, "4 turns across 3 sessions")
	assert.Contains(t, pivot.Value, "503 Service Unavailable")
}

func TestReflect_SinglePatternOccurrenceNotPromoted(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := context.Background()

	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		// One occurrence.
		pivotEntry("p1", "sess-a", "Bash", "exit status 1", time.Hour),
		// Repeated, but only ever within one session.
		circularEntry("c1", "sess-b", "Glob→Read", "h1", 2*time.Hour),
		circularEntry("c2", "sess-b", "Glob→Read", "h1", time.Hour),
	})
	res, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 0, res.PatternsPromoted)
	assert.Empty(t, ltmReflectToolsKeys(t, rem))
}

func TestReflect_PatternOutsideWindowIgnored(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := context.Background()

	// The older occurrence is beyond the retention window, so only one
	// occurrence counts.
	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		pivotEntry("p1", "sess-a", "Bash", "exit status 1", 45*24*time.Hour),
		pivotEntry("p2", "sess-b", "Bash", "exit status 1", time.Hour),
	})
	res, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 0, res.PatternsPromoted)
	assert.Empty(t, ltmReflectToolsKeys(t, rem))
}

func TestReflect_PatternDryRunWritesNothing(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := context.Background()

	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		pivotEntry("p1", "sess-a", "WebFetch", "timeout", 2*time.Hour),
		pivotEntry("p2", "sess-b", "WebFetch", "timeout", time.Hour),
	})
	res, err := rem.Reflect(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, 1, res.PatternsPromoted)
	assert.Empty(t, ltmReflectToolsKeys(t, rem))
	unprocessed, err := reflection.NewStore(rem.db).QueryUnprocessed(ctx)
	require.NoError(t, err)
	assert.Len(t, unprocessed, 2)
}

// Pattern rows record a threshold crossing, not a tool execution; they must
// not inflate tool+outcome clusters.
func TestReflect_PatternsNotCountedInToolClusters(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := brain.WithUserID(context.Background(), "u")

	seedReflectionEntries(t, rem, []*reflection.ReflectionEntry{
		pivotEntry("p1", "sess-a", "WebFetch", "timeout", 3*time.Hour),
		pivotEntry("p2", "sess-b", "WebFetch", "timeout", 2*time.Hour),
		pivotEntry("p3", "sess-c", "WebFetch", "timeout", time.Hour),
	})
	res, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, 0, res.ClustersFound)
	e, err := b.Get(ctx, "reflect.clusters.WebFetch.failure")
	require.NoError(t, err)
	assert.Nil(t, e)
}

// Reflect must not load or bind every unprocessed row: a large backlog is
// processed and marked in bulk.
func TestReflect_LargeBacklogMarkedProcessed(t *testing.T) {
	rem, b, _ := setupTestREMCycle(t)
	defer b.Close()
	ctx := context.Background()

	const n = 5000 // marked by one rowid-bounded UPDATE, no per-ID binding
	entries := make([]*reflection.ReflectionEntry, 0, n)
	base := time.Now().Add(-2 * time.Hour)
	for i := 0; i < n; i++ {
		e := reflection.NewEntry("system", reflection.TypeToolOutcome, reflection.OutcomeSuccess)
		e.SessionKey = "bulk"
		e.Tool = "Bash"
		e.Timestamp = base.Add(time.Duration(i) * time.Millisecond)
		entries = append(entries, e)
	}
	seedReflectionEntries(t, rem, entries)

	res, err := rem.Reflect(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, n, res.EntriesProcessed)
	var left int
	require.NoError(t, rem.db.QueryRow(`SELECT COUNT(*) FROM brain_reflections WHERE rem_processed = 0`).Scan(&left))
	assert.Zero(t, left)
}
