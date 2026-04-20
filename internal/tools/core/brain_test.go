package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/tools/types"
)

// fakeBrainService captures calls for assertions. It satisfies types.BrainService
// but only the methods we need for these tests do real work.
type fakeBrainService struct {
	bulkCalls    [][]types.BrainBulkEntry
	bulkErr      error
	singleStores []types.BrainBulkEntry
	ttlCalls     []fakeTTLCall
}

type fakeTTLCall struct {
	Key    string
	Value  string
	Tier   types.BrainTier
	Source string
	TTL    time.Duration
}

func (f *fakeBrainService) Store(ctx context.Context, key, value string, tier types.BrainTier, source string) error {
	f.singleStores = append(f.singleStores, types.BrainBulkEntry{Key: key, Value: value, Tier: tier, Source: source})
	return nil
}

func (f *fakeBrainService) StoreWithTTL(ctx context.Context, key, value string, tier types.BrainTier, source string, ttl time.Duration) error {
	f.ttlCalls = append(f.ttlCalls, fakeTTLCall{Key: key, Value: value, Tier: tier, Source: source, TTL: ttl})
	return nil
}


func (f *fakeBrainService) StoreBulk(ctx context.Context, entries []types.BrainBulkEntry) error {
	if f.bulkErr != nil {
		return f.bulkErr
	}
	// Copy to avoid external mutation leaking into captured calls.
	captured := make([]types.BrainBulkEntry, len(entries))
	copy(captured, entries)
	f.bulkCalls = append(f.bulkCalls, captured)
	return nil
}

func (f *fakeBrainService) Get(ctx context.Context, key string) (*types.BrainEntry, error) {
	return nil, nil
}
func (f *fakeBrainService) Recall(ctx context.Context, query string, limit int) ([]*types.BrainEntry, error) {
	return nil, nil
}
func (f *fakeBrainService) RecallWithContext(ctx context.Context, query string, limit int, recallContext string) ([]*types.BrainEntry, error) {
	return nil, nil
}
func (f *fakeBrainService) List(ctx context.Context, prefix, sourcePrefix string) ([]*types.BrainEntry, error) {
	return nil, nil
}
func (f *fakeBrainService) Delete(ctx context.Context, key string) error            { return nil }
func (f *fakeBrainService) Push(ctx context.Context, userID, value string) error    { return nil }
func (f *fakeBrainService) Pop(ctx context.Context, userID string) (string, error)  { return "", nil }
func (f *fakeBrainService) Peek(ctx context.Context, userID string) (string, error) { return "", nil }
func (f *fakeBrainService) Promote(ctx context.Context, key string) error           { return nil }
func (f *fakeBrainService) Consolidate(ctx context.Context, autoPromote bool) (*types.ConsolidationReport, error) {
	return &types.ConsolidationReport{}, nil
}
func (f *fakeBrainService) WorkingMemoryEntries(ctx context.Context) []*types.BrainEntry {
	return nil
}
func (f *fakeBrainService) Status(ctx context.Context) (*types.BrainStatus, error) {
	return &types.BrainStatus{}, nil
}
func (f *fakeBrainService) Close() error { return nil }

func TestBrainTool_StoreBulkDispatch(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	args := map[string]interface{}{
		"action": "store_bulk",
		"entries": []interface{}{
			map[string]interface{}{"key": "a", "value": "1", "tier": "working", "source": "file:x.md"},
			map[string]interface{}{"key": "b", "value": "2", "tier": "longterm"},
			map[string]interface{}{"key": "c", "value": "3"}, // default tier + source
		},
	}

	res, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got err=%q", res.Error)
	}
	if len(fake.bulkCalls) != 1 {
		t.Fatalf("expected 1 StoreBulk call, got %d", len(fake.bulkCalls))
	}
	got := fake.bulkCalls[0]
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// Entry 0: explicit tier + source
	if got[0].Key != "a" || got[0].Value != "1" || got[0].Tier != types.BrainTierWorking || got[0].Source != "file:x.md" {
		t.Errorf("entry 0 mismatch: %+v", got[0])
	}
	// Entry 1: longterm, default source
	if got[1].Key != "b" || got[1].Tier != types.BrainTierLongTerm || got[1].Source != "tool" {
		t.Errorf("entry 1 mismatch: %+v", got[1])
	}
	// Entry 2: defaults to working + "tool"
	if got[2].Tier != types.BrainTierWorking || got[2].Source != "tool" {
		t.Errorf("entry 2 mismatch: %+v", got[2])
	}
}

func TestBrainTool_StoreBulkMissingEntries(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "store_bulk"})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if res.Success {
		t.Fatalf("expected failure when entries missing")
	}
}

func TestBrainTool_StoreBulkInvalidTier(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"action": "store_bulk",
		"entries": []interface{}{
			map[string]interface{}{"key": "a", "value": "1", "tier": "scratch"},
		},
	})
	if res.Success {
		t.Fatalf("expected failure for scratch tier in store_bulk")
	}
}

func TestBrainTool_StoreBulkPropagatesError(t *testing.T) {
	fake := &fakeBrainService{bulkErr: fmt.Errorf("boom")}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"action": "store_bulk",
		"entries": []interface{}{
			map[string]interface{}{"key": "a", "value": "1"},
		},
	})
	if res.Success {
		t.Fatalf("expected failure when StoreBulk returns error")
	}
}

func TestBrainTool_StoreWithTTL(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "store",
		"key":    "theo.grooming_next",
		"value":  "April 23",
		"tier":   "longterm",
		"ttl":    "7d",
	})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got err=%q", res.Error)
	}
	if len(fake.ttlCalls) != 1 {
		t.Fatalf("expected 1 StoreWithTTL call, got %d", len(fake.ttlCalls))
	}
	if fake.ttlCalls[0].TTL != 7*24*time.Hour {
		t.Errorf("expected TTL 7d, got %v", fake.ttlCalls[0].TTL)
	}
	if len(fake.singleStores) != 0 {
		t.Errorf("expected no Store call when ttl provided, got %d", len(fake.singleStores))
	}
}

func TestBrainTool_StoreWithoutTTL_UsesStore(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "store",
		"key":    "persist.key",
		"value":  "v",
	})
	if err != nil {
		t.Fatalf("Execute err: %v", err)
	}
	if !res.Success {
		t.Fatalf("expected success, got err=%q", res.Error)
	}
	if len(fake.ttlCalls) != 0 {
		t.Errorf("expected no StoreWithTTL call when ttl absent, got %d", len(fake.ttlCalls))
	}
	if len(fake.singleStores) != 1 {
		t.Errorf("expected 1 Store call, got %d", len(fake.singleStores))
	}
}

func TestBrainTool_StoreInvalidTTL(t *testing.T) {
	fake := &fakeBrainService{}
	tool := NewBrainTool(&types.ToolServices{Brain: fake})

	res, _ := tool.Execute(context.Background(), map[string]interface{}{
		"action": "store",
		"key":    "k",
		"value":  "v",
		"ttl":    "not-a-duration",
	})
	if res.Success {
		t.Fatalf("expected failure for invalid ttl")
	}
}

func TestParseTTLString(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", 0, false},
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"bogus", 0, true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseTTLString(c.in)
			if c.err && err == nil {
				t.Fatalf("expected err for %q", c.in)
			}
			if !c.err && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !c.err && got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
