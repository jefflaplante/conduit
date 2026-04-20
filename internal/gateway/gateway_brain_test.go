package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/brain"
	"conduit/internal/tools/types"
)

func newTestBrainForGateway(t *testing.T) *brain.Brain {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	b, err := brain.New(dbPath, brain.WithAutoFlushInterval(0))
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestBrainAdapter_StoreGetDelete(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	// Store WM entry
	if err := a.Store(ctx, "test.key", "value", types.BrainTier(brain.TierWorking), "system"); err != nil {
		t.Fatalf("Store: %v", err)
	}

	// Get it back
	entry, err := a.Get(ctx, "test.key")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry == nil || entry.Value != "value" {
		t.Errorf("expected value='value', got %+v", entry)
	}
	if entry.Tier != types.BrainTier(brain.TierWorking) {
		t.Errorf("expected tier=working, got %v", entry.Tier)
	}

	// Delete it
	if err := a.Delete(ctx, "test.key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestBrainAdapter_GetMissing(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	entry, err := a.Get(ctx, "does-not-exist")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if entry != nil {
		t.Errorf("expected nil entry for missing key, got %+v", entry)
	}
}

func TestBrainAdapter_List(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	_ = a.Store(ctx, "jeff.birthday", "Oct 5", types.BrainTier(brain.TierLongTerm), "user")
	_ = a.Store(ctx, "jeff.favorite_color", "blue", types.BrainTier(brain.TierLongTerm), "user")

	entries, err := a.List(ctx, "jeff.", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 entries, got %d", len(entries))
	}
}

func TestBrainAdapter_Recall(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	_ = a.Store(ctx, "k1", "hello world", types.BrainTier(brain.TierLongTerm), "user")
	entries, err := a.Recall(ctx, "hello", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	// Recall may or may not return results depending on tokenizer — just ensure no panic
	_ = entries
}

func TestBrainAdapter_Scratchpad(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := context.Background()

	// Push, Peek, Pop
	if err := a.Push(ctx, "user1", "item1"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := a.Push(ctx, "user1", "item2"); err != nil {
		t.Fatalf("Push: %v", err)
	}

	top, err := a.Peek(ctx, "user1")
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if top != "item2" {
		t.Errorf("expected top='item2', got %q", top)
	}

	popped, err := a.Pop(ctx, "user1")
	if err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if popped != "item2" {
		t.Errorf("expected pop='item2', got %q", popped)
	}
}

func TestBrainAdapter_Status(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := context.Background()

	// Store some entries
	wmCtx := brain.WithUserID(ctx, "user1")
	_ = a.Store(wmCtx, "key1", "val1", types.BrainTier(brain.TierWorking), "test")

	status, err := a.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status == nil {
		t.Error("expected non-nil status")
	}
}

func TestBrainAdapter_WorkingMemoryEntries(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	_ = a.Store(ctx, "wm.key", "val", types.BrainTier(brain.TierWorking), "test")

	entries := a.WorkingMemoryEntries(ctx)
	// Should not panic. Entries is a slice (possibly empty).
	_ = entries
}

func TestBrainAdapter_Promote(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := brain.WithUserID(context.Background(), "user1")

	_ = a.Store(ctx, "pk", "val", types.BrainTier(brain.TierWorking), "test")
	if err := a.Promote(ctx, "pk"); err != nil {
		t.Errorf("Promote: %v", err)
	}
}

func TestBrainAdapter_Consolidate(t *testing.T) {
	b := newTestBrainForGateway(t)
	a := newBrainAdapter(b)
	ctx := context.Background()

	report, err := a.Consolidate(ctx, false)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if report == nil {
		t.Error("expected non-nil report")
	}
}

func TestConvertEntry(t *testing.T) {
	e := &brain.Entry{
		Key:         "k1",
		Value:       "v1",
		Tier:        brain.TierLongTerm,
		AccessCount: 3,
		Salience:    0.5,
		Source:      "test",
		Stale:       false,
	}
	out := convertEntry(e)
	if out.Key != "k1" || out.Value != "v1" {
		t.Errorf("unexpected output: %+v", out)
	}
	if out.Tier != types.BrainTier(brain.TierLongTerm) {
		t.Errorf("tier mismatch: %v", out.Tier)
	}
	if out.AccessCount != 3 || out.Salience != 0.5 {
		t.Errorf("numeric fields mismatch: %+v", out)
	}
}

func TestConvertEntries_Empty(t *testing.T) {
	out := convertEntries(nil)
	if len(out) != 0 {
		t.Errorf("expected empty slice, got %v", out)
	}
}

func TestConvertEntries_Multiple(t *testing.T) {
	entries := []*brain.Entry{
		{Key: "k1", Value: "v1", Tier: brain.TierWorking},
		{Key: "k2", Value: "v2", Tier: brain.TierLongTerm},
	}
	out := convertEntries(entries)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %d", len(out))
	}
	if out[0].Key != "k1" || out[1].Key != "k2" {
		t.Errorf("unexpected keys: %v, %v", out[0].Key, out[1].Key)
	}
}

func TestHeartbeatBrainWriter(t *testing.T) {
	b := newTestBrainForGateway(t)
	w := newHeartbeatBrainWriter(b)
	ctx := brain.WithUserID(context.Background(), "system")

	// StoreAlert
	if err := w.StoreAlert(ctx, "alert.1", "fire"); err != nil {
		t.Fatalf("StoreAlert: %v", err)
	}

	// ListAlertKeys
	keys, err := w.ListAlertKeys(ctx, "alert.")
	if err != nil {
		t.Fatalf("ListAlertKeys: %v", err)
	}
	found := false
	for _, k := range keys {
		if k == "alert.1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected alert.1 in keys %v", keys)
	}

	// DeleteAlert
	if err := w.DeleteAlert(ctx, "alert.1"); err != nil {
		t.Fatalf("DeleteAlert: %v", err)
	}
}
