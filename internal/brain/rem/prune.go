package rem

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"conduit/internal/reflection"
)

// isFilePath returns true if the source string looks like a filesystem path
// (absolute path or file: prefixed). Non-path sources like "tool", "user:manual",
// "llm:generated" etc. should NOT be checked with os.Stat.
func isFilePath(source string) bool {
	if source == "" {
		return false
	}
	// Explicit file: prefix (used by groom.go convention)
	if strings.HasPrefix(source, "file:") {
		return true
	}
	// Absolute filesystem path
	if strings.HasPrefix(source, "/") {
		return true
	}
	return false
}

// Prune moves low-value entries to the archive (safe deletion).
// When the LTM table is under MaxLTMEntries, pruning is skipped entirely —
// there's no performance or quality reason to evict entries from a small table.
func (r *REMCycle) Prune(ctx context.Context, dryRun bool) (*PruneResult, error) {
	result := &PruneResult{
		Archived: []ArchiveRecord{},
		Orphaned: []string{},
	}

	// Phase 0: Delete entries whose TTL has expired. This runs unconditionally
	// and is reported separately so expired entries are not counted as
	// low-salience evictions.
	if !dryRun {
		n, err := r.brain.PruneExpired(ctx)
		if err != nil {
			return result, fmt.Errorf("prune expired: %w", err)
		}
		result.ExpiredDeleted = n
	} else {
		// Dry-run: count without deleting.
		var n int
		_ = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM brain_ltm WHERE expires_at IS NOT NULL AND expires_at <= strftime('%Y-%m-%d %H:%M:%f', 'now')`,
		).Scan(&n)
		result.ExpiredDeleted = n
	}

	// Guard: skip pruning when LTM is under the size threshold.
	// A small table doesn't degrade performance or search quality.
	maxEntries := r.config.MaxLTMEntries
	if maxEntries <= 0 {
		maxEntries = 10000 // safe default
	}

	var ltmCount int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM brain_ltm").Scan(&ltmCount); err != nil {
		return nil, fmt.Errorf("count LTM entries: %w", err)
	}

	var err error

	if ltmCount < maxEntries {
		// Under threshold — only do orphan detection for entries whose source
		// files have genuinely been deleted from disk. Skip salience-based eviction.
		result, err = r.pruneOrphansOnly(ctx, result, dryRun)
		if err != nil {
			return result, err
		}
	} else {
		// Over threshold — run full salience-based eviction + orphan detection.

		// Get evict threshold from brain config (default 0.1)
		evictThreshold := 0.1

		// 1. Find entries to evict based on salience and age
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
				_, err := r.db.Exec(`
					INSERT OR REPLACE INTO brain_archive (key, value, source, tier, salience, reason, archived_at)
					VALUES (?, ?, ?, 'longterm', ?, 'low_salience', datetime('now'))
				`, c.key, c.value, c.source, c.salience)
				if err != nil {
					return nil, fmt.Errorf("archive entry %q: %w", c.key, err)
				}

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

		// 3. Detect orphaned keys (file-path sources only)
		result, err = r.pruneOrphansOnly(ctx, result, dryRun)
		if err != nil {
			return result, err
		}
	}

	// 4. Groom old processed reflection entries
	groomed, err := r.groomReflections(ctx, dryRun)
	if err != nil {
		return result, fmt.Errorf("groom reflections: %w", err)
	}
	result.ReflectionsGroomed = groomed

	return result, nil
}

// groomReflections deletes processed reflection entries older than retention days.
func (r *REMCycle) groomReflections(ctx context.Context, dryRun bool) (int, error) {
	retentionDays := r.config.PruneAgeDays
	if retentionDays <= 0 {
		retentionDays = 30
	}

	if dryRun {
		// Count how many would be groomed
		cutoff := time.Now().UTC().Add(-time.Duration(retentionDays) * 24 * time.Hour)
		cutoffStr := cutoff.Format("2006-01-02 15:04:05")
		var count int
		err := r.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM brain_reflections WHERE rem_processed = 1 AND timestamp < ?",
			cutoffStr).Scan(&count)
		if err != nil {
			// Table may not exist yet; that's fine
			return 0, nil
		}
		return count, nil
	}

	store := reflection.NewStore(r.db)
	groomed, err := store.Groom(ctx, retentionDays)
	if err != nil {
		// Table may not exist yet; that's fine
		return 0, nil
	}
	return groomed, nil
}

// pruneOrphansOnly detects entries whose source files have been deleted.
// Only checks entries with file-path sources (absolute paths or file: prefix).
// Non-path sources like "tool", "user:manual", "llm:generated" are skipped.
func (r *REMCycle) pruneOrphansOnly(ctx context.Context, result *PruneResult, dryRun bool) (*PruneResult, error) {
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

		// Only check sources that are filesystem paths.
		// "tool", "user:manual", "llm:generated" etc. are NOT file paths.
		if !isFilePath(o.source) {
			continue
		}

		// Resolve the actual path to stat.
		pathToCheck := o.source
		if strings.HasPrefix(pathToCheck, "file:") {
			pathToCheck = strings.TrimPrefix(pathToCheck, "file:")
		}

		if _, err := os.Stat(pathToCheck); os.IsNotExist(err) {
			orphans = append(orphans, o)
		}
	}
	if err := orphanRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate orphan candidates: %w", err)
	}

	// Archive orphaned entries
	for _, o := range orphans {
		if !dryRun {
			_, err := r.db.Exec(`
				INSERT OR REPLACE INTO brain_archive (key, value, source, tier, salience, reason, archived_at)
				VALUES (?, ?, ?, 'longterm', ?, 'orphaned', datetime('now'))
			`, o.key, o.value, o.source, o.salience)
			if err != nil {
				return nil, fmt.Errorf("archive orphaned entry %q: %w", o.key, err)
			}

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
