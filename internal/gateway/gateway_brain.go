package gateway

import (
	"context"

	"conduit/internal/brain"
	"conduit/internal/tools/types"
)

// brainAdapter adapts *brain.Brain to types.BrainService interface.
type brainAdapter struct {
	b *brain.Brain
}

func newBrainAdapter(b *brain.Brain) *brainAdapter {
	return &brainAdapter{b: b}
}

func (a *brainAdapter) Store(ctx context.Context, key, value string, tier types.BrainTier, source string) error {
	return a.b.Store(ctx, key, value, brain.Tier(tier), source)
}

func (a *brainAdapter) Get(ctx context.Context, key string) (*types.BrainEntry, error) {
	e, err := a.b.Get(ctx, key)
	if err != nil || e == nil {
		return nil, err
	}
	return convertEntry(e), nil
}

func (a *brainAdapter) Recall(ctx context.Context, query string, limit int) ([]*types.BrainEntry, error) {
	entries, err := a.b.Recall(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	return convertEntries(entries), nil
}

func (a *brainAdapter) List(ctx context.Context, prefix string) ([]*types.BrainEntry, error) {
	entries, err := a.b.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	return convertEntries(entries), nil
}

func (a *brainAdapter) Delete(ctx context.Context, key string) error {
	return a.b.Delete(ctx, key)
}

func (a *brainAdapter) Push(ctx context.Context, userID, value string) error {
	return a.b.Push(ctx, userID, value)
}

func (a *brainAdapter) Pop(ctx context.Context, userID string) (string, error) {
	return a.b.Pop(ctx, userID)
}

func (a *brainAdapter) Peek(ctx context.Context, userID string) (string, error) {
	return a.b.Peek(ctx, userID)
}

func (a *brainAdapter) Promote(ctx context.Context, key string) error {
	return a.b.Promote(ctx, key)
}

func (a *brainAdapter) Consolidate(ctx context.Context, autoPromote bool) (*types.ConsolidationReport, error) {
	r, err := a.b.Consolidate(ctx, autoPromote)
	if err != nil {
		return nil, err
	}
	return &types.ConsolidationReport{
		PromotedCount: r.PromotedCount, EvictedCount: r.EvictedCount,
		LTMSize: r.LTMSize, PromotedKeys: r.PromotedKeys, EvictedKeys: r.EvictedKeys,
	}, nil
}

func (a *brainAdapter) Status(ctx context.Context) (*types.BrainStatus, error) {
	s, err := a.b.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &types.BrainStatus{
		LTMEntries: s.LTMEntries, WMEntries: s.WMEntries,
		ScratchDepth: s.ScratchDepth, AvgSalience: s.AvgSalience,
		HottestKeys: s.HottestKeys,
	}, nil
}

func (a *brainAdapter) Close() error {
	return a.b.Close()
}

// convertEntry converts a brain.Entry to types.BrainEntry.
func convertEntry(e *brain.Entry) *types.BrainEntry {
	return &types.BrainEntry{
		Key: e.Key, Value: e.Value, Tier: types.BrainTier(e.Tier),
		CreatedAt: e.CreatedAt, AccessedAt: e.AccessedAt,
		AccessCount: e.AccessCount, Salience: e.Salience, Source: e.Source,
	}
}

// convertEntries converts a slice of brain.Entry to types.BrainEntry.
func convertEntries(entries []*brain.Entry) []*types.BrainEntry {
	result := make([]*types.BrainEntry, len(entries))
	for i, e := range entries {
		result[i] = convertEntry(e)
	}
	return result
}
