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
		// Peak salience keeps the historical 0.1 threshold meaningful
		// (conduit-31jg.53).
		peak := r.peakSalienceSQL()
		query := `
			SELECT key, value, source, ` + peak + `
			FROM brain_ltm
			WHERE ` + peak + ` < ?
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

	// 4. Archive cold LTM entries: stored once, never used since, and older
	// than 30 days. Only runs at/over capacity and is batch-limited; see
	// evictColdLTM. conduit-31jg.28
	coldEvicted, err := r.evictColdLTM(ctx, dryRun)
	if err != nil {
		return result, fmt.Errorf("evict cold LTM: %w", err)
	}
	result.ColdEvicted = coldEvicted

	// 5. Groom old processed reflection entries
	groomed, err := r.groomReflections(ctx, dryRun)
	if err != nil {
		return result, fmt.Errorf("groom reflections: %w", err)
	}
	result.ReflectionsGroomed = groomed

	// 6. Prune hub nodes that exceed maxNodeDegree
	hubsPruned, edgesRemoved, err := r.HubPrune(ctx, dryRun)
	if err != nil {
		return result, fmt.Errorf("prune hubs: %w", err)
	}
	result.HubsPruned = hubsPruned
	result.EdgesRemoved = edgesRemoved

	return result, nil
}

// coldEvictBatchLimit caps how many cold entries one Prune run may archive,
// so a large backlog is drained gradually rather than in one nightly sweep.
// conduit-31jg.28
const coldEvictBatchLimit = 100

// evictColdLTM archives LTM entries that were stored once and never used
// since: access_count <= 1 (Store inserts with access_count = 1, and every
// Get, Recall hit or re-Store increments it), and neither created nor accessed
// in the last 30 days.
//
// conduit-31jg.28: this previously matched `access_count = 0`, which no
// Store-written row ever has, so it was a silent no-op. Widening the predicate
// alone would have hard-deleted every write-once fact older than 30 days on
// the next nightly run, so the sweep is now also:
//   - gated on LTM being at/over MaxLTMEntries (same rule as the salience
//     sweep: a table under capacity is never trimmed);
//   - bounded to coldEvictBatchLimit rows per run, lowest salience first;
//   - archived to brain_archive (reason 'cold') before deletion, in one tx.
func (r *REMCycle) evictColdLTM(ctx context.Context, dryRun bool) (int, error) {
	maxEntries := r.config.MaxLTMEntries
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	var ltmCount int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM brain_ltm").Scan(&ltmCount); err != nil {
		return 0, fmt.Errorf("count LTM entries: %w", err)
	}
	if ltmCount < maxEntries {
		return 0, nil
	}

	cutoff := time.Now().Add(-30 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cold LTM tx: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT key, value, COALESCE(source, ''), `+r.peakSalienceSQL()+`
		FROM brain_ltm
		WHERE access_count <= 1 AND created_at < ? AND accessed_at < ?
		ORDER BY salience ASC, accessed_at ASC
		LIMIT ?`, cutoff, cutoff, coldEvictBatchLimit)
	if err != nil {
		return 0, fmt.Errorf("query cold LTM candidates: %w", err)
	}
	type coldCandidate struct {
		key, value, source string
		salience           float64
	}
	var candidates []coldCandidate
	for rows.Next() {
		var c coldCandidate
		if err := rows.Scan(&c.key, &c.value, &c.source, &c.salience); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan cold LTM candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate cold LTM candidates: %w", err)
	}

	if dryRun || len(candidates) == 0 {
		return len(candidates), nil
	}

	for _, c := range candidates {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO brain_archive (key, value, source, tier, salience, reason, archived_at)
			VALUES (?, ?, ?, 'longterm', ?, 'cold', datetime('now'))`,
			c.key, c.value, c.source, c.salience); err != nil {
			return 0, fmt.Errorf("archive cold entry %q: %w", c.key, err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM brain_ltm WHERE key = ?", c.key); err != nil {
			return 0, fmt.Errorf("delete cold entry %q: %w", c.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit cold LTM eviction: %w", err)
	}
	return len(candidates), nil
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

// HubPrune removes lowest-confidence edges from nodes that exceed maxNodeDegree.
// This prevents unbounded edge accumulation that degrades graph performance.
func (r *REMCycle) HubPrune(ctx context.Context, dryRun bool) (hubsPruned, edgesRemoved int, err error) {
	// Find all nodes that exceed the degree cap
	query := `
		SELECT DISTINCT node
		FROM (
			SELECT key_a as node FROM brain_relationships
			UNION ALL
			SELECT key_b as node FROM brain_relationships
		)
		GROUP BY node
		HAVING COUNT(*) > ?
	`

	rows, err := r.db.Query(query, maxNodeDegree)
	if err != nil {
		return 0, 0, fmt.Errorf("query hub nodes: %w", err)
	}
	defer rows.Close()

	var hubNodes []string
	for rows.Next() {
		var node string
		if err := rows.Scan(&node); err != nil {
			return 0, 0, fmt.Errorf("scan hub node: %w", err)
		}
		hubNodes = append(hubNodes, node)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate hub nodes: %w", err)
	}

	if len(hubNodes) == 0 {
		return 0, 0, nil // No hubs to prune
	}

	// For each hub, prune edges down to the cap
	for _, hub := range hubNodes {
		// Get all edges for this hub, ordered by confidence (lowest first)
		edgeQuery := `
			SELECT key_a, key_b, confidence
			FROM brain_relationships
			WHERE key_a = ? OR key_b = ?
			ORDER BY confidence ASC
		`

		edgeRows, err := r.db.Query(edgeQuery, hub, hub)
		if err != nil {
			return 0, 0, fmt.Errorf("query edges for hub %q: %w", hub, err)
		}

		var edges []struct {
			keyA       string
			keyB       string
			confidence float64
		}

		for edgeRows.Next() {
			var e struct {
				keyA       string
				keyB       string
				confidence float64
			}
			if err := edgeRows.Scan(&e.keyA, &e.keyB, &e.confidence); err != nil {
				edgeRows.Close()
				return 0, 0, fmt.Errorf("scan edge for hub %q: %w", hub, err)
			}
			edges = append(edges, e)
		}
		edgeRows.Close()

		edgeCount := len(edges)
		if edgeCount <= maxNodeDegree {
			continue // Already under cap
		}

		// Remove edges beyond the cap (lowest confidence first)
		edgesToRemove := edgeCount - maxNodeDegree
		for i := 0; i < edgesToRemove; i++ {
			e := edges[i]
			if !dryRun {
				_, err := r.db.ExecContext(ctx,
					"DELETE FROM brain_relationships WHERE key_a = ? AND key_b = ?",
					e.keyA, e.keyB)
				if err != nil {
					return 0, 0, fmt.Errorf("delete edge for hub %q: %w", hub, err)
				}
			}
			edgesRemoved++
		}

		hubsPruned++
	}

	return hubsPruned, edgesRemoved, nil
}

// pruneOrphansOnly detects entries whose source files have been deleted.
// Only checks entries with file-path sources (absolute paths or file: prefix).
// Non-path sources like "tool", "user:manual", "llm:generated" are skipped.
func (r *REMCycle) pruneOrphansOnly(ctx context.Context, result *PruneResult, dryRun bool) (*PruneResult, error) {
	orphanQuery := `
		SELECT key, value, source, ` + r.peakSalienceSQL() + `
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
		pathToCheck := strings.TrimPrefix(o.source, "file:")

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
