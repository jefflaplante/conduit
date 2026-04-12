package gateway

import (
	"context"
	"time"

	"conduit/internal/reflection"
	"conduit/internal/tools/types"
)

// reflectionAdapter adapts *reflection.ReflectionStore to types.ReflectionService interface.
type reflectionAdapter struct {
	store *reflection.ReflectionStore
}

func newReflectionAdapter(s *reflection.ReflectionStore) *reflectionAdapter {
	return &reflectionAdapter{store: s}
}

func (a *reflectionAdapter) Insert(ctx context.Context, entry *types.ReflectionEntry) error {
	return a.store.Insert(ctx, toReflectionEntry(entry))
}

func (a *reflectionAdapter) InsertBatch(ctx context.Context, entries []*types.ReflectionEntry) error {
	converted := make([]*reflection.ReflectionEntry, len(entries))
	for i, e := range entries {
		converted[i] = toReflectionEntry(e)
	}
	return a.store.InsertBatch(ctx, converted)
}

func (a *reflectionAdapter) QueryBySession(ctx context.Context, sessionKey string) ([]*types.ReflectionEntry, error) {
	entries, err := a.store.QueryBySession(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	return fromReflectionEntries(entries), nil
}

func (a *reflectionAdapter) QueryUnprocessed(ctx context.Context) ([]*types.ReflectionEntry, error) {
	entries, err := a.store.QueryUnprocessed(ctx)
	if err != nil {
		return nil, err
	}
	return fromReflectionEntries(entries), nil
}

func (a *reflectionAdapter) MarkProcessed(ctx context.Context, ids []string) error {
	return a.store.MarkProcessed(ctx, ids)
}

func (a *reflectionAdapter) Groom(ctx context.Context, retentionDays int) (int, error) {
	return a.store.Groom(ctx, retentionDays)
}

func (a *reflectionAdapter) QueryToolStats(ctx context.Context, since time.Time) ([]types.ReflectionToolStat, error) {
	stats, err := a.store.QueryToolStats(ctx, since)
	if err != nil {
		return nil, err
	}
	result := make([]types.ReflectionToolStat, len(stats))
	for i, s := range stats {
		result[i] = types.ReflectionToolStat{
			Tool:        s.Tool,
			Outcome:     string(s.Outcome),
			Count:       s.Count,
			AvgDuration: s.AvgDuration,
			AvgRetries:  s.AvgRetries,
		}
	}
	return result, nil
}

// toReflectionEntry converts a types.ReflectionEntry to reflection.ReflectionEntry.
func toReflectionEntry(e *types.ReflectionEntry) *reflection.ReflectionEntry {
	return &reflection.ReflectionEntry{
		ID:          e.ID,
		SessionKey:  e.SessionKey,
		Timestamp:   e.Timestamp,
		Source:      e.Source,
		Type:        reflection.ReflectionType(e.Type),
		Tool:        e.Tool,
		Outcome:     reflection.Outcome(e.Outcome),
		RetryCount:  e.RetryCount,
		Duration:    e.Duration,
		Insight:     e.Insight,
		Score:       e.Score,
		Tags:        e.Tags,
		RelatedKeys: e.RelatedKeys,
	}
}

// fromReflectionEntry converts a reflection.ReflectionEntry to types.ReflectionEntry.
func fromReflectionEntry(e *reflection.ReflectionEntry) *types.ReflectionEntry {
	return &types.ReflectionEntry{
		ID:          e.ID,
		SessionKey:  e.SessionKey,
		Timestamp:   e.Timestamp,
		Source:      e.Source,
		Type:        string(e.Type),
		Tool:        e.Tool,
		Outcome:     string(e.Outcome),
		RetryCount:  e.RetryCount,
		Duration:    e.Duration,
		Insight:     e.Insight,
		Score:       e.Score,
		Tags:        e.Tags,
		RelatedKeys: e.RelatedKeys,
	}
}

// fromReflectionEntries converts a slice of reflection.ReflectionEntry to types.ReflectionEntry.
func fromReflectionEntries(entries []*reflection.ReflectionEntry) []*types.ReflectionEntry {
	result := make([]*types.ReflectionEntry, len(entries))
	for i, e := range entries {
		result[i] = fromReflectionEntry(e)
	}
	return result
}
