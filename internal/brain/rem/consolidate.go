package rem

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Consolidate promotes high-value working memory, merges duplicates, and adjusts salience
func (r *REMCycle) Consolidate(ctx context.Context, dryRun bool) (*ConsolidationResult, error) {
	result := &ConsolidationResult{
		Promoted:        []string{},
		Merged:          []MergeRecord{},
		SalienceDecayed: 0,
		SalienceBoosted: 0,
	}

	// Phase 0: Prune expired entries before any promotion work so expired
	// WM entries don't get promoted to LTM.
	if !dryRun {
		if _, err := r.brain.PruneExpired(ctx); err != nil {
			return result, fmt.Errorf("prune expired before consolidate: %w", err)
		}
	}

	// Phase 1: Promote high-salience WM entries to LTM
	if err := r.promoteHighSalienceEntries(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("promote high-salience entries: %w", err)
	}

	// Phase 2: Detect and merge duplicate keys in LTM
	if err := r.mergeDuplicates(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("merge duplicates: %w", err)
	}

	// Phase 3: Apply salience decay to untouched entries (>7 days)
	if err := r.applySalienceDecay(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("apply salience decay: %w", err)
	}

	// Phase 4: Boost salience for recently accessed entries
	if err := r.boostRecentlyAccessed(ctx, result, dryRun); err != nil {
		return result, fmt.Errorf("boost recently accessed: %w", err)
	}

	return result, nil
}

// promoteHighSalienceEntries promotes WM entries with high salience to LTM.
// Also promotes entries with AccessCount >= HeatPromotionThreshold regardless of salience,
// so frequently-used keys get persisted even if the salience formula under-values them.
func (r *REMCycle) promoteHighSalienceEntries(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
	entries := r.brain.WorkingMemoryEntries(ctx)
	if len(entries) == 0 {
		return nil
	}

	threshold := r.config.ConsolidateThreshold
	if threshold <= 0 {
		threshold = 0.6
	}

	heatThreshold := r.brain.HeatPromotionThreshold()
	if heatThreshold <= 0 {
		heatThreshold = 3
	}

	promoted := make(map[string]bool)
	for _, entry := range entries {
		salienceHit := entry.Salience >= threshold
		heatHit := entry.AccessCount >= heatThreshold
		if !salienceHit && !heatHit {
			continue
		}
		if promoted[entry.Key] {
			continue
		}
		if !dryRun {
			if err := r.brain.Promote(ctx, entry.Key); err != nil {
				// Entry may have been removed between snapshot and promote, skip
				continue
			}
		}
		result.Promoted = append(result.Promoted, entry.Key)
		promoted[entry.Key] = true
	}

	return nil
}

// mergeDuplicates detects and merges duplicate keys in LTM
func (r *REMCycle) mergeDuplicates(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
	// Query all LTM entries
	rows, err := r.db.Query(`
		SELECT key, value, salience, access_count
		FROM brain_ltm
		ORDER BY key
	`)
	if err != nil {
		return fmt.Errorf("query LTM entries: %w", err)
	}
	defer rows.Close()

	type ltmEntry struct {
		key         string
		value       string
		salience    float64
		accessCount int
	}
	var entries []ltmEntry
	for rows.Next() {
		var e ltmEntry
		if err := rows.Scan(&e.key, &e.value, &e.salience, &e.accessCount); err != nil {
			return fmt.Errorf("scan entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows iteration: %w", err)
	}

	// Detect duplicates by normalized key comparison
	merged := make(map[string]bool)
	for i := 0; i < len(entries); i++ {
		if merged[entries[i].key] {
			continue
		}
		keyA := normalizeKey(entries[i].key)
		for j := i + 1; j < len(entries); j++ {
			if merged[entries[j].key] {
				continue
			}
			keyB := normalizeKey(entries[j].key)
			if keyA == keyB {
				// Found duplicate - keep the one with higher salience
				kept, toMerge := entries[i], entries[j]
				if entries[j].salience > entries[i].salience {
					kept, toMerge = entries[j], entries[i]
				}

				result.Merged = append(result.Merged, MergeRecord{
					Kept:   kept.key,
					Merged: toMerge.key,
				})

				if !dryRun {
					// Archive the merged entry
					if err := r.archiveEntry(toMerge.key, toMerge.value, toMerge.salience, "merged into "+kept.key); err != nil {
						return fmt.Errorf("archive merged entry %q: %w", toMerge.key, err)
					}
					// Delete from LTM
					if _, err := r.db.Exec("DELETE FROM brain_ltm WHERE key = ?", toMerge.key); err != nil {
						return fmt.Errorf("delete merged entry %q: %w", toMerge.key, err)
					}
				}
				merged[toMerge.key] = true
			}
		}
	}

	return nil
}

// applySalienceDecay reduces salience for entries not accessed in >7 days.
// Skipped when LTM is under MaxLTMEntries — no need to push entries toward
// eviction when the table is small.
func (r *REMCycle) applySalienceDecay(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
	// Guard: skip decay when LTM is under the size threshold.
	maxEntries := r.config.MaxLTMEntries
	if maxEntries <= 0 {
		maxEntries = 10000
	}

	var ltmCount int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmCount); err != nil {
		return fmt.Errorf("count LTM entries: %w", err)
	}
	if ltmCount < maxEntries {
		return nil // small table, no decay needed
	}

	sevenDaysAgo := time.Now().Add(-7 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	if dryRun {
		// Just count how many would be decayed
		var count int
		err := r.db.QueryRow(`
			SELECT COUNT(*)
			FROM brain_ltm
			WHERE accessed_at < ?
		`, sevenDaysAgo).Scan(&count)
		if err != nil {
			return fmt.Errorf("count decay candidates: %w", err)
		}
		result.SalienceDecayed = count
		return nil
	}

	// Apply decay
	res, err := r.db.Exec(`
		UPDATE brain_ltm
		SET salience = MAX(0.0, salience - ?)
		WHERE accessed_at < ?
	`, r.config.SalienceDecayRate, sevenDaysAgo)
	if err != nil {
		return fmt.Errorf("apply decay: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	result.SalienceDecayed = int(affected)

	return nil
}

// boostRecentlyAccessed increases salience for recently accessed entries
func (r *REMCycle) boostRecentlyAccessed(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
	oneDayAgo := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	if dryRun {
		// Just count how many would be boosted
		var count int
		err := r.db.QueryRow(`
			SELECT COUNT(*)
			FROM brain_ltm
			WHERE accessed_at >= ?
		`, oneDayAgo).Scan(&count)
		if err != nil {
			return fmt.Errorf("count boost candidates: %w", err)
		}
		result.SalienceBoosted = count
		return nil
	}

	// Apply boost (cap at 1.0)
	res, err := r.db.Exec(`
		UPDATE brain_ltm
		SET salience = MIN(1.0, salience + 0.05)
		WHERE accessed_at >= ?
	`, oneDayAgo)
	if err != nil {
		return fmt.Errorf("apply boost: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected: %w", err)
	}
	result.SalienceBoosted = int(affected)

	return nil
}

// archiveEntry moves an entry to the archive table
func (r *REMCycle) archiveEntry(key, value string, salience float64, reason string) error {
	_, err := r.db.Exec(`
		INSERT OR REPLACE INTO brain_archive (key, value, tier, salience, reason, archived_at)
		VALUES (?, ?, 'longterm', ?, ?, datetime('now'))
	`, key, value, salience, reason)
	if err != nil {
		return fmt.Errorf("archive entry: %w", err)
	}
	return nil
}

// normalizeKey normalizes a key for comparison (lowercase, trim spaces, collapse whitespace)
func normalizeKey(key string) string {
	key = strings.ToLower(key)
	key = strings.TrimSpace(key)
	// Collapse multiple spaces to single space
	fields := strings.Fields(key)
	return strings.Join(fields, " ")
}
