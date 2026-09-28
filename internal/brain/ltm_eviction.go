package brain

import (
	"context"
	"log"
	"time"

	"conduit/internal/database"
)

// DefaultLTMEvictionGrace is how long a newly written/accessed LTM row is
// exempt from capacity eviction. conduit-31jg.28
const DefaultLTMEvictionGrace = time.Hour

// ltmEvictionScoreSQL ranks LTM rows for capacity eviction using the
// query-independent part of Recall's blended score:
//
//	salience*salienceWeight + warmth*warmthWeight + accessBonus*accessBonusWeight
//
// where accessBonus mirrors computeAccessBonus (recency computed at query time
// from accessed_at, never baked in). Lowest score is evicted first.
// Placeholders: salienceWeight, warmthWeight, accessBonusCap, accessBonusAlpha,
// accessBonusDecay, accessBonusWeight. conduit-31jg.28
//
// conduit-31jg.53: salience here is the query-time effective salience (base +
// recency computed from accessed_at), matching what Recall ranks on.
func (b *Brain) ltmEvictionScoreSQL() string {
	return `(
	` + b.EffectiveSalienceSQL() + ` * ? +
	COALESCE(warmth, 0) * ? +
	COALESCE(MIN(?, MAX(access_count, 0) * ? *
		pow(?, MAX(0.0, julianday('now') - julianday(accessed_at)))), 0) * ?
)`
}

// evictLTMOverCapacity trims brain_ltm down to maxLTMEntries.
//
// conduit-31jg.28: the previous implementation deleted ORDER BY salience ASC,
// and because the upsert bakes a recency of 1.0 into salience for any touched
// row (~0.564) while fresh inserts get 0.5, the row just inserted was almost
// always the one deleted. Now:
//   - rows accessed/written within ltmEvictionGrace of now are never
//     candidates, so the current Store/StoreBulk write can't be evicted (the
//     table may briefly exceed the cap if everything is that fresh);
//   - candidates are ranked by the same salience/warmth/access-bonus signal
//     Recall uses, so old-but-valuable facts are not evicted merely for age;
//   - evicted rows are archived to brain_archive (reason 'capacity') in the
//     same transaction as the delete, and each key is logged.
//
// Best-effort: errors are logged, never returned, matching prior behaviour.
func (b *Brain) evictLTMOverCapacity(ctx context.Context, now time.Time) {
	if b.maxLTMEntries <= 0 {
		return
	}
	grace := b.ltmEvictionGrace
	if grace < 0 {
		grace = 0
	}
	// Strict "<" against cutoff: with grace == 0 the cutoff equals the
	// write's own timestamp, so the current write is still protected.
	cutoff := now.Add(-grace).UTC().Format("2006-01-02 15:04:05")

	// Fast path: a single read, no transaction, when under capacity (the
	// common case). The count is re-checked inside the tx below.
	var total int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM brain_ltm`).Scan(&total); err != nil {
		log.Printf("Brain: LTM capacity check failed: %v", err)
		return
	}
	if total <= b.maxLTMEntries {
		return
	}

	var evicted []string
	var shortfall int
	err := database.RetryOnBusy(5, func() error {
		evicted, shortfall = nil, 0
		tx, err := b.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM brain_ltm`).Scan(&count); err != nil {
			return err
		}
		excess := count - b.maxLTMEntries
		if excess <= 0 {
			return nil
		}

		rows, err := tx.QueryContext(ctx, `
			SELECT key, value, COALESCE(source, ''), `+b.PeakSalienceSQL()+`
			FROM brain_ltm
			WHERE accessed_at < ? AND created_at < ?
			ORDER BY `+b.ltmEvictionScoreSQL()+` ASC, accessed_at ASC
			LIMIT ?`,
			cutoff, cutoff,
			b.salienceWeight, b.warmthWeight,
			b.accessBonusCap, b.accessBonusAlpha, b.accessBonusDecay, b.accessBonusWeight,
			excess)
		if err != nil {
			return err
		}
		type victim struct {
			key, value, source string
			salience           float64
		}
		var victims []victim
		for rows.Next() {
			var v victim
			if err := rows.Scan(&v.key, &v.value, &v.source, &v.salience); err != nil {
				rows.Close()
				return err
			}
			victims = append(victims, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, v := range victims {
			if _, err := tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO brain_archive (key, value, source, tier, salience, reason, archived_at)
				VALUES (?, ?, ?, 'longterm', ?, 'capacity', datetime('now'))`,
				v.key, v.value, v.source, v.salience); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM brain_ltm WHERE key = ?`, v.key); err != nil {
				return err
			}
			evicted = append(evicted, v.key)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		shortfall = excess - len(victims)
		return nil
	})
	if err != nil {
		log.Printf("Brain: LTM capacity eviction failed: %v", err)
		return
	}
	for _, k := range evicted {
		log.Printf("Brain: LTM at capacity (%d) — archived and evicted key=%q", b.maxLTMEntries, k)
	}
	if shortfall > 0 {
		log.Printf("Brain: LTM over capacity by %d but all remaining rows are within the %s eviction grace window; deferring",
			shortfall, grace)
	}
}
