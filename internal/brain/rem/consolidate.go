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

// promoteHighSalienceEntries promotes WM entries with high salience to LTM
func (r *REMCycle) promoteHighSalienceEntries(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
	// Get all WM entries for all users by listing with empty prefix
	// Note: Brain.List requires a userID in context, but we want all users
	// We'll need to query the working memory map directly
	// Since we have direct access to r.brain, we'll need to get the entries

	// For now, we'll use a threshold-based approach on LTM entries themselves
	// and promote based on the config.consolidateThreshold

	// Since WM is in-memory and per-user, we'll use the Brain's Consolidate method
	// which already handles promotion logic. However, we need to trigger it for all users.

	// For REM consolidation, we'll focus on LTM operations and assume WM->LTM promotion
	// happens through the normal Brain.Consolidate() flow during runtime.
	// REM consolidation primarily works on the persistent LTM store.

	// TODO: If we need to promote specific WM entries, we'd need to:
	// 1. Iterate through brain.working map (requires exposing it or adding a method)
	// 2. For each user, check entry.Salience >= threshold
	// 3. Call r.brain.Promote(ctx, key) for those entries

	// For now, skip WM promotion in REM and focus on LTM consolidation
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

// applySalienceDecay reduces salience for entries not accessed in >7 days
func (r *REMCycle) applySalienceDecay(ctx context.Context, result *ConsolidationResult, dryRun bool) error {
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
		INSERT INTO brain_archive (key, value, tier, salience, reason, archived_at)
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
