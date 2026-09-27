package reflection

import (
	"context"
	"testing"
	"time"
)

// conduit-31jg.54: MarkProcessed must not bind more variables than SQLite
// allows (32766), and pattern rows are excluded from tool stats.

func TestMarkProcessed_ChunksLargeIDLists(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const n = 3*markProcessedChunk + 7 // several chunks, including a partial one
	entries := make([]*ReflectionEntry, 0, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		e := NewEntry("system", TypeToolOutcome, OutcomeSuccess)
		e.SessionKey = "s"
		e.Tool = "Bash"
		entries = append(entries, e)
		ids = append(ids, e.ID)
	}
	if err := s.InsertBatch(ctx, entries); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkProcessed(ctx, ids); err != nil {
		t.Fatalf("MarkProcessed(%d ids): %v", n, err)
	}
	left, err := s.QueryUnprocessed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("%d entries left unprocessed", len(left))
	}
}

func TestQueryToolStats_ExcludesPatternRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	outcome := NewEntry("system", TypeToolOutcome, OutcomeFailure)
	outcome.SessionKey, outcome.Tool = "s", "WebFetch"
	pattern := NewEntry("system", TypePattern, OutcomeFailure)
	pattern.SessionKey, pattern.Tool = "s", "WebFetch"
	if err := s.InsertBatch(ctx, []*ReflectionEntry{outcome, pattern}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.QueryToolStats(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Count != 1 {
		t.Fatalf("stats = %+v, want one WebFetch/failure row with count 1", stats)
	}
}

func TestQueryPatterns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := NewEntry("system", TypePattern, OutcomeFailure)
	old.SessionKey, old.Timestamp = "a", time.Now().Add(-48*time.Hour)
	old.RelatedKeys = []string{ConsecutiveFailureKey("Bash")}
	recent := NewEntry("system", TypePattern, OutcomePartial)
	recent.SessionKey = "b"
	recent.RelatedKeys = []string{CircularPatternKey("h")}
	other := NewEntry("system", TypeToolOutcome, OutcomeSuccess)
	other.SessionKey = "b"
	if err := s.InsertBatch(ctx, []*ReflectionEntry{old, recent, other}); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryPatterns(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != recent.ID {
		t.Fatalf("QueryPatterns = %+v, want only the recent pattern row", got)
	}
}
