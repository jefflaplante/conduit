package brain

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Status struct {
	LTMEntries   int      `json:"ltm_entries"`
	WMEntries    int      `json:"wm_entries"`
	ScratchDepth int      `json:"scratch_depth"`
	AvgSalience  float64  `json:"avg_salience,omitempty"`
	HottestKeys  []string `json:"hottest_keys,omitempty"`
	ColdestKeys  []string `json:"coldest_keys,omitempty"`
	ExpiringSoon int      `json:"expiring_soon,omitempty"`

	// Spreading activation metrics (session-lifetime counters).
	SpreadEvents    int64          `json:"spread_events,omitempty"`
	AvgWarmthBoost  float64        `json:"avg_warmth_boost,omitempty"`
	ClusterHitRate  float64        `json:"cluster_hit_rate,omitempty"`
	WarmthHitCount  int64          `json:"warmth_hit_count,omitempty"`
	EdgeCountByType map[string]int `json:"edge_count_by_type,omitempty"`

	// Access bonus metrics.
	AvgAccessBonus float64  `json:"avg_access_bonus,omitempty"`
	TopAccessBonus []string `json:"top_access_bonus_keys,omitempty"`
}

func (b *Brain) List(ctx context.Context, prefix string, sourcePrefix string) ([]*Entry, error) {
	var results []*Entry
	seen := make(map[string]bool)
	userID := userIDFromCtx(ctx)
	parentID := parentUserIDFromCtx(ctx)
	now := time.Now()

	b.mu.RLock()
	if wm, ok := b.working[userID]; ok {
		for _, entry := range wm {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if strings.HasPrefix(entry.Key, prefix) {
				if sourcePrefix == "" || strings.HasPrefix(entry.Source, sourcePrefix) {
					copied := *entry // conduit-31jg.32: snapshot, never the live entry
					results = append(results, &copied)
					seen[entry.Key] = true
				}
			}
		}
	}
	// Include parent's WM, then the shared bucket (read-only copies, deduped
	// by key). conduit-31jg.30
	for _, bucket := range readOnlyBuckets(userID, parentID) {
		for _, entry := range b.working[bucket] {
			if entry.ExpiresAt != nil && !entry.ExpiresAt.After(now) {
				continue
			}
			if !seen[entry.Key] && strings.HasPrefix(entry.Key, prefix) {
				if sourcePrefix == "" || strings.HasPrefix(entry.Source, sourcePrefix) {
					copied := *entry
					results = append(results, &copied)
					seen[entry.Key] = true
				}
			}
		}
	}
	b.mu.RUnlock()

	query := `SELECT key, value, created_at, accessed_at, access_count, ` + b.EffectiveSalienceSQL() + `, source, stale, expires_at
		FROM brain_ltm WHERE key LIKE ? ESCAPE '\' AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))`
	args := []interface{}{EscapeLike(prefix) + "%"}
	if sourcePrefix != "" {
		query += ` AND source LIKE ? ESCAPE '\'`
		args = append(args, EscapeLike(sourcePrefix)+"%")
	}
	query += " ORDER BY key"

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return results, fmt.Errorf("list LTM: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entry := &Entry{Tier: TierLongTerm}
		var staleInt int
		var expiresAtNT sql.NullTime
		if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt, &entry.AccessedAt,
			&entry.AccessCount, &entry.Salience, &entry.Source, &staleInt, &expiresAtNT); err != nil {
			continue
		}
		entry.Stale = staleInt != 0
		if expiresAtNT.Valid {
			t := expiresAtNT.Time
			entry.ExpiresAt = &t
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return results, fmt.Errorf("list LTM rows: %w", err)
	}

	return results, nil
}

func (b *Brain) Status(ctx context.Context) (*Status, error) {
	userID := userIDFromCtx(ctx)
	now := time.Now()
	soonCutoff := now.Add(24 * time.Hour)
	b.mu.RLock()
	wmCount := 0
	scratchDepth := 0
	var totalSalience float64
	expiringSoon := 0
	if wm, ok := b.working[userID]; ok {
		for _, e := range wm {
			if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
				continue // already-expired entries don't count as "active"
			}
			wmCount++
			totalSalience += e.Salience
			if e.ExpiresAt != nil && !e.ExpiresAt.After(soonCutoff) {
				expiringSoon++
			}
		}
	}
	if stack, ok := b.scratch[userID]; ok {
		scratchDepth = len(stack)
	}
	b.mu.RUnlock()

	var ltmCount int
	b.db.QueryRow(`SELECT COUNT(*) FROM brain_ltm WHERE expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now')`).Scan(&ltmCount)
	var hottestKeys []string
	rows, err := b.db.Query(`SELECT key FROM brain_ltm WHERE expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now') ORDER BY ` + b.EffectiveSalienceSQL() + ` DESC LIMIT 5`)
	if err == nil {
		for rows.Next() {
			var key string
			if rows.Scan(&key) == nil {
				hottestKeys = append(hottestKeys, key)
			}
		}
		rows.Close()
	}
	var coldestKeys []string
	coldRows, err := b.db.Query("SELECT key FROM brain_ltm ORDER BY access_count ASC, accessed_at ASC LIMIT 5")
	if err == nil {
		for coldRows.Next() {
			var key string
			if coldRows.Scan(&key) == nil {
				coldestKeys = append(coldestKeys, key)
			}
		}
		coldRows.Close()
	}
	// Count LTM entries expiring within the next 24h.
	var ltmExpiringSoon int
	b.db.QueryRow(
		`SELECT COUNT(*) FROM brain_ltm WHERE expires_at IS NOT NULL AND expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now') AND expires_at <= strftime('%Y-%m-%d %H:%M:%f', 'now', '+24 hours')`,
	).Scan(&ltmExpiringSoon)
	expiringSoon += ltmExpiringSoon

	avgSalience := 0.0
	if wmCount > 0 {
		avgSalience = totalSalience / float64(wmCount)
	}

	// Spreading activation metrics. Counter snapshots under mu; edge-count query
	// outside the lock.
	b.mu.RLock()
	spreadEvents := b.spreadEvents
	totalBoost := b.totalBoost
	totalBoostCount := b.totalBoostCount
	clusterHits := b.clusterHitCount
	directHits := b.directHitCount
	warmthHits := b.warmthHitCount
	b.mu.RUnlock()

	var avgBoost float64
	if totalBoostCount > 0 {
		avgBoost = totalBoost / float64(totalBoostCount)
	}
	var clusterHitRate float64
	if total := clusterHits + directHits; total > 0 {
		clusterHitRate = float64(clusterHits) / float64(total)
	}

	var edgeCounts map[string]int
	if edgeRows, err := b.db.Query(
		`SELECT COALESCE(relationship, ''), COUNT(*) FROM brain_relationships GROUP BY relationship`,
	); err == nil {
		edgeCounts = make(map[string]int)
		for edgeRows.Next() {
			var rel string
			var n int
			if edgeRows.Scan(&rel, &n) == nil {
				if rel == "" {
					rel = "unknown"
				}
				edgeCounts[rel] = n
			}
		}
		edgeRows.Close()
	}

	// Access bonus metrics: average and top-5 keys by access bonus.
	var avgAccessBonus float64
	var topAccessBonusKeys []string
	if bonusRows, err := b.db.Query(`
		SELECT key, access_count, accessed_at
		  FROM brain_ltm
		 WHERE access_count > 0 AND (expires_at IS NULL OR expires_at > strftime('%Y-%m-%d %H:%M:%f', 'now'))
		 ORDER BY accessed_at DESC
		 LIMIT 100
	`); err == nil {
		var totalBonus float64
		var count int
		type bonusEntry struct {
			key   string
			bonus float64
		}
		var entries []bonusEntry
		for bonusRows.Next() {
			var key string
			var ac int
			var aa sql.NullTime
			if bonusRows.Scan(&key, &ac, &aa) != nil {
				continue
			}
			e := &Entry{AccessCount: ac, Tier: TierLongTerm}
			if aa.Valid {
				e.AccessedAt = aa.Time
			}
			bonus := b.computeAccessBonus(e)
			if bonus > 0 {
				totalBonus += bonus
				count++
				entries = append(entries, bonusEntry{key: key, bonus: bonus})
			}
		}
		bonusRows.Close()
		if count > 0 {
			avgAccessBonus = totalBonus / float64(count)
		}
		// Sort by bonus descending and take top 5.
		sort.Slice(entries, func(i, j int) bool { return entries[i].bonus > entries[j].bonus })
		limit := 5
		if len(entries) < limit {
			limit = len(entries)
		}
		topAccessBonusKeys = make([]string, limit)
		for i := 0; i < limit; i++ {
			topAccessBonusKeys[i] = entries[i].key
		}
	}

	return &Status{
		LTMEntries: ltmCount, WMEntries: wmCount, ScratchDepth: scratchDepth,
		AvgSalience: avgSalience, HottestKeys: hottestKeys, ColdestKeys: coldestKeys,
		ExpiringSoon:    expiringSoon,
		SpreadEvents:    spreadEvents,
		AvgWarmthBoost:  avgBoost,
		ClusterHitRate:  clusterHitRate,
		WarmthHitCount:  warmthHits,
		EdgeCountByType: edgeCounts,
		AvgAccessBonus:  avgAccessBonus,
		TopAccessBonus:  topAccessBonusKeys,
	}, nil
}
