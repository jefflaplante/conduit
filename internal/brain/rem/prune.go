package rem

import (
	"context"
	"fmt"
	"os"
)

// Prune moves low-value entries to the archive (safe deletion)
func (r *REMCycle) Prune(ctx context.Context, dryRun bool) (*PruneResult, error) {
	result := &PruneResult{
		Archived: []ArchiveRecord{},
		Orphaned: []string{},
	}

	// Get evict threshold from brain config (default 0.1 if not available)
	evictThreshold := 0.1
	if r.brain != nil {
		// The REMCycle has access to brain via the struct, but we need evictThreshold
		// For now, use the default from the config field
		// TODO: Consider passing BrainConfig to REMCycle if needed
	}

	// 1. Find entries to evict based on salience and age
	//    Query: salience < config.EvictThreshold AND accessed_at < (now - PruneAgeDays)
	query := `
		SELECT key, value, source, salience
		FROM brain_ltm
		WHERE salience < ?
		AND accessed_at < datetime('now', ? || ' days')
	`

	rows, err := r.db.Query(query, evictThreshold, fmt.Sprintf("-%d", r.config.PruneAgeDays))
	if err != nil {
		return nil, fmt.Errorf("query low-salience entries: %w", err)
	}
	defer rows.Close()

	type evictCandidate struct {
		key      string
		value    string
		source   string
		salience float64
	}
	var candidates []evictCandidate

	for rows.Next() {
		var c evictCandidate
		if err := rows.Scan(&c.key, &c.value, &c.source, &c.salience); err != nil {
			return nil, fmt.Errorf("scan evict candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evict candidates: %w", err)
	}

	// 2. Archive entries (move, don't delete)
	for _, c := range candidates {
		if !dryRun {
			// Insert into archive
			_, err := r.db.Exec(`
				INSERT INTO brain_archive (key, value, source, tier, salience, reason)
				VALUES (?, ?, ?, 'longterm', ?, 'low_salience')
			`, c.key, c.value, c.source, c.salience)
			if err != nil {
				return nil, fmt.Errorf("archive entry %q: %w", c.key, err)
			}

			// Delete from LTM
			_, err = r.db.Exec("DELETE FROM brain_ltm WHERE key = ?", c.key)
			if err != nil {
				return nil, fmt.Errorf("delete entry %q from LTM: %w", c.key, err)
			}
		}

		result.Archived = append(result.Archived, ArchiveRecord{
			Key:    c.key,
			Reason: "low_salience",
		})
	}

	// 3. Detect orphaned keys
	//    - Entries with source pointing to non-existent files
	//    - Check if source file exists using os.Stat()
	//    - Archive with reason = 'orphaned'
	orphanQuery := `
		SELECT key, value, source, salience
		FROM brain_ltm
		WHERE source != '' AND source IS NOT NULL
	`

	orphanRows, err := r.db.Query(orphanQuery)
	if err != nil {
		return nil, fmt.Errorf("query potential orphans: %w", err)
	}
	defer orphanRows.Close()

	type orphanCandidate struct {
		key      string
		value    string
		source   string
		salience float64
	}
	var orphans []orphanCandidate

	for orphanRows.Next() {
		var o orphanCandidate
		if err := orphanRows.Scan(&o.key, &o.value, &o.source, &o.salience); err != nil {
			return nil, fmt.Errorf("scan orphan candidate: %w", err)
		}

		// Check if source file exists
		if _, err := os.Stat(o.source); os.IsNotExist(err) {
			orphans = append(orphans, o)
		}
	}
	if err := orphanRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphan candidates: %w", err)
	}

	// Archive orphaned entries
	for _, o := range orphans {
		if !dryRun {
			// Insert into archive
			_, err := r.db.Exec(`
				INSERT INTO brain_archive (key, value, source, tier, salience, reason)
				VALUES (?, ?, ?, 'longterm', ?, 'orphaned')
			`, o.key, o.value, o.source, o.salience)
			if err != nil {
				return nil, fmt.Errorf("archive orphaned entry %q: %w", o.key, err)
			}

			// Delete from LTM
			_, err = r.db.Exec("DELETE FROM brain_ltm WHERE key = ?", o.key)
			if err != nil {
				return nil, fmt.Errorf("delete orphaned entry %q from LTM: %w", o.key, err)
			}
		}

		result.Archived = append(result.Archived, ArchiveRecord{
			Key:    o.key,
			Reason: "orphaned",
		})
		result.Orphaned = append(result.Orphaned, o.key)
	}

	return result, nil
}
