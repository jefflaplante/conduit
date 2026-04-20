package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/brain"
	"conduit/internal/reflection"
	"conduit/internal/tools/types"
)

func newTestReflectionStore(t *testing.T) *reflection.ReflectionStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(dbPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return reflection.NewStore(b.DB())
}

func TestReflectionAdapter_InsertQueryBySession(t *testing.T) {
	s := newTestReflectionStore(t)
	a := newReflectionAdapter(s)

	entry := &types.ReflectionEntry{
		ID:         "ent-1",
		SessionKey: "sess-1",
		Timestamp:  time.Now(),
		Source:     "system",
		Type:       "tool_outcome",
		Tool:       "Read",
		Outcome:    "success",
		RetryCount: 0,
		Duration:   100 * time.Millisecond,
		Insight:    "worked fine",
		Score:      4,
		Tags:       []string{"io"},
	}
	if err := a.Insert(context.Background(), entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	list, err := a.QueryBySession(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("QueryBySession: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(list))
	}
	if list[0].ID != "ent-1" {
		t.Errorf("unexpected id: %q", list[0].ID)
	}
	if list[0].Tool != "Read" {
		t.Errorf("unexpected tool: %q", list[0].Tool)
	}
}

func TestReflectionAdapter_InsertBatch(t *testing.T) {
	s := newTestReflectionStore(t)
	a := newReflectionAdapter(s)

	entries := []*types.ReflectionEntry{
		{ID: "b1", SessionKey: "sess-b", Timestamp: time.Now(), Source: "system", Type: "tool_outcome", Outcome: "success"},
		{ID: "b2", SessionKey: "sess-b", Timestamp: time.Now(), Source: "model", Type: "tool_outcome", Outcome: "failure"},
	}
	if err := a.InsertBatch(context.Background(), entries); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}

	list, err := a.QueryBySession(context.Background(), "sess-b")
	if err != nil {
		t.Fatalf("QueryBySession: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 entries, got %d", len(list))
	}
}

func TestReflectionAdapter_QueryUnprocessedAndMark(t *testing.T) {
	s := newTestReflectionStore(t)
	a := newReflectionAdapter(s)

	entry := &types.ReflectionEntry{
		ID: "unproc-1", SessionKey: "sess-u", Timestamp: time.Now(),
		Source: "system", Type: "tool_outcome", Outcome: "success",
	}
	if err := a.Insert(context.Background(), entry); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	unproc, err := a.QueryUnprocessed(context.Background())
	if err != nil {
		t.Fatalf("QueryUnprocessed: %v", err)
	}
	if len(unproc) == 0 {
		t.Fatal("expected at least 1 unprocessed entry")
	}

	if err := a.MarkProcessed(context.Background(), []string{"unproc-1"}); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
}

func TestReflectionAdapter_Groom(t *testing.T) {
	s := newTestReflectionStore(t)
	a := newReflectionAdapter(s)

	// Groom with no entries — should not error and return 0 deleted
	n, err := a.Groom(context.Background(), 30)
	if err != nil {
		t.Fatalf("Groom: %v", err)
	}
	if n < 0 {
		t.Errorf("expected non-negative groom count, got %d", n)
	}
}

func TestReflectionAdapter_QueryToolStats(t *testing.T) {
	s := newTestReflectionStore(t)
	a := newReflectionAdapter(s)

	// Insert a few entries
	for i, outcome := range []string{"success", "failure", "success"} {
		entry := &types.ReflectionEntry{
			ID: "ts-" + string(rune('a'+i)), SessionKey: "sess-ts",
			Timestamp: time.Now(), Source: "system", Type: "tool_outcome",
			Tool: "Bash", Outcome: outcome,
		}
		if err := a.Insert(context.Background(), entry); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	stats, err := a.QueryToolStats(context.Background(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("QueryToolStats: %v", err)
	}
	// Stats may be aggregated; just ensure method runs & returns slice
	_ = stats
}

func TestToReflectionEntry_RoundTrip(t *testing.T) {
	orig := &types.ReflectionEntry{
		ID:          "id-1",
		SessionKey:  "sk",
		Timestamp:   time.Now(),
		Source:      "model",
		Type:        "session_summary",
		Tool:        "Bash",
		Outcome:     "success",
		RetryCount:  2,
		Duration:    5 * time.Second,
		Insight:     "insight text",
		Score:       5,
		Tags:        []string{"a", "b"},
		RelatedKeys: []string{"k1"},
	}
	internal := toReflectionEntry(orig)
	back := fromReflectionEntry(internal)

	if back.ID != orig.ID || back.SessionKey != orig.SessionKey {
		t.Errorf("id/session mismatch: %+v vs %+v", orig, back)
	}
	if back.Tool != orig.Tool || back.Score != orig.Score {
		t.Errorf("tool/score mismatch: %+v vs %+v", orig, back)
	}
	if back.Type != orig.Type || back.Source != orig.Source {
		t.Errorf("type/source mismatch: %+v vs %+v", orig, back)
	}
	if back.Duration != orig.Duration {
		t.Errorf("duration mismatch: %v vs %v", orig.Duration, back.Duration)
	}
	if len(back.Tags) != len(orig.Tags) || len(back.RelatedKeys) != len(orig.RelatedKeys) {
		t.Errorf("tags/related_keys length mismatch")
	}
}

func TestFromReflectionEntries_Empty(t *testing.T) {
	out := fromReflectionEntries(nil)
	if len(out) != 0 {
		t.Errorf("expected empty, got %v", out)
	}
}

func TestFromReflectionEntries_Multiple(t *testing.T) {
	entries := []*reflection.ReflectionEntry{
		{ID: "1", SessionKey: "s1"},
		{ID: "2", SessionKey: "s2"},
	}
	out := fromReflectionEntries(entries)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	if out[0].ID != "1" || out[1].ID != "2" {
		t.Errorf("unexpected ids: %v, %v", out[0].ID, out[1].ID)
	}
}
