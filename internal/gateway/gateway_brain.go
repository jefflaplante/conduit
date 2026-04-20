package gateway

import (
	"context"
	"time"

	"conduit/internal/brain"
	"conduit/internal/brain/rem"
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

func (a *brainAdapter) StoreWithTTL(ctx context.Context, key, value string, tier types.BrainTier, source string, ttl time.Duration) error {
	return a.b.StoreWithTTL(ctx, key, value, brain.Tier(tier), source, ttl)
}

func (a *brainAdapter) StoreBulk(ctx context.Context, entries []types.BrainBulkEntry) error {
	if len(entries) == 0 {
		return nil
	}
	converted := make([]brain.BulkEntry, len(entries))
	for i, e := range entries {
		converted[i] = brain.BulkEntry{
			Key:    e.Key,
			Value:  e.Value,
			Tier:   brain.Tier(e.Tier),
			Source: e.Source,
		}
	}
	return a.b.StoreBulk(ctx, converted)
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

func (a *brainAdapter) RecallWithContext(ctx context.Context, query string, limit int, contextStr string) ([]*types.BrainEntry, error) {
	entries, err := a.b.RecallWithContext(ctx, query, limit, contextStr)
	if err != nil {
		return nil, err
	}
	return convertEntries(entries), nil
}

func (a *brainAdapter) List(ctx context.Context, prefix string, sourcePrefix string) ([]*types.BrainEntry, error) {
	entries, err := a.b.List(ctx, prefix, sourcePrefix)
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
		HottestKeys: s.HottestKeys, ColdestKeys: s.ColdestKeys,
		ExpiringSoon: s.ExpiringSoon,
	}, nil
}

func (a *brainAdapter) WorkingMemoryEntries(ctx context.Context) []*types.BrainEntry {
	return convertEntries(a.b.WorkingMemoryEntries(ctx))
}

func (a *brainAdapter) Close() error {
	return a.b.Close()
}

// convertEntry converts a brain.Entry to types.BrainEntry.
func convertEntry(e *brain.Entry) *types.BrainEntry {
	return &types.BrainEntry{
		Key: e.Key, Value: e.Value, Tier: types.BrainTier(e.Tier),
		CreatedAt: e.CreatedAt, AccessedAt: e.AccessedAt,
		AccessCount: e.AccessCount, Salience: e.Salience, Source: e.Source, Stale: e.Stale,
		ExpiresAt: e.ExpiresAt,
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

// heartbeatBrainWriter adapts *brain.Brain to heartbeat.BrainWriter interface.
// It writes heartbeat alerts into Brain's working memory under sense.alerts.* namespace.
type heartbeatBrainWriter struct {
	b *brain.Brain
}

func newHeartbeatBrainWriter(b *brain.Brain) *heartbeatBrainWriter {
	return &heartbeatBrainWriter{b: b}
}

func (w *heartbeatBrainWriter) StoreAlert(ctx context.Context, key, value string) error {
	return w.b.Store(ctx, key, value, brain.TierWorking, "system:heartbeat")
}

func (w *heartbeatBrainWriter) DeleteAlert(ctx context.Context, key string) error {
	return w.b.Delete(ctx, key)
}

func (w *heartbeatBrainWriter) ListAlertKeys(ctx context.Context, prefix string) ([]string, error) {
	entries, err := w.b.List(ctx, prefix, "")
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys, nil
}

// remCycleAdapter adapts *rem.REMCycle to types.REMCycleRunner interface.
type remCycleAdapter struct {
	cycle *rem.REMCycle
}

func newREMCycleAdapter(c *rem.REMCycle) *remCycleAdapter {
	return &remCycleAdapter{cycle: c}
}

func (a *remCycleAdapter) RunREMCycle(ctx context.Context, phases []string, dryRun bool) (*types.REMCycleReport, error) {
	report, err := a.cycle.Run(ctx, phases, dryRun)
	if err != nil {
		return nil, err
	}
	return convertREMReport(report), nil
}

// convertREMReport converts a rem.REMReport to types.REMCycleReport.
func convertREMReport(r *rem.REMReport) *types.REMCycleReport {
	result := &types.REMCycleReport{
		Date:   r.Date.Format("2006-01-02 15:04:05"),
		DryRun: r.DryRun,
	}

	if r.Triage != nil {
		result.Triage = map[string]interface{}{
			"daily_log_scanned": r.Triage.DailyLogScanned,
			"wm_keys_found":     r.Triage.WMKeysFound,
			"new_facts":         r.Triage.NewFacts,
			"updated_facts":     r.Triage.UpdatedFacts,
			"stale_candidates":  r.Triage.StaleCandidates,
		}
	}

	if r.Consolidation != nil {
		result.Consolidation = map[string]interface{}{
			"promoted":         r.Consolidation.Promoted,
			"merged":           r.Consolidation.Merged,
			"salience_decayed": r.Consolidation.SalienceDecayed,
			"salience_boosted": r.Consolidation.SalienceBoosted,
		}
	}

	if r.Pruning != nil {
		result.Pruning = map[string]interface{}{
			"archived":            r.Pruning.Archived,
			"orphaned":            r.Pruning.Orphaned,
			"cold_evicted":        r.Pruning.ColdEvicted,
			"reflections_groomed": r.Pruning.ReflectionsGroomed,
		}
	}

	if r.Integration != nil {
		result.Integration = map[string]interface{}{
			"relationships_created": r.Integration.RelationshipsCreated,
			"patterns":              r.Integration.Patterns,
		}
	}

	if r.Grooming != nil {
		result.Grooming = map[string]interface{}{
			"files_checked":        r.Grooming.FilesChecked,
			"files_changed":        r.Grooming.FilesChanged,
			"keys_updated":         r.Grooming.KeysUpdated,
			"entries_marked_stale": r.Grooming.EntriesMarkedStale,
		}
	}

	return result
}
