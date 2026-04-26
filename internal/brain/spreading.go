package brain

import (
	"fmt"
	"math"
	"time"

	"conduit/internal/database"
)

// spreadActivation propagates activation from each accessed key to its direct
// neighbours in brain_relationships. The warmth boost delivered to each
// neighbour is:
//
//	warmth_boost = decay^distance * source_salience * effective_confidence
//
// where effective_confidence = base_confidence * (1 + alpha * log1p(access_count)),
// giving frequently-traversed edges a boost. With default alpha=0.1, an edge
// accessed 10 times gets ~24% more confidence, and 100 times gets ~46%.
//
// This implementation supports distance=1 (direct neighbours) only. The
// neighbour's warmth is updated to max(current_warmth, warmth_boost) capped
// at 1.0, so a warm neighbour never gets dimmed by a weaker activation wave.
//
// Edges with confidence below minConfidenceThreshold are excluded from
// spreading (boost is not calculated for them), but entries remain searchable
// via direct recall. This prevents low-confidence edges from polluting the
// activation graph while preserving searchability.
//
// spreadActivation is a no-op when spreadingEnabled is false or when the
// accessedKeys slice is empty. Errors are non-fatal; callers should log or
// ignore them rather than propagating to the user.
func (b *Brain) spreadActivation(accessedKeys []string) error {
	if !b.spreadingEnabled || len(accessedKeys) == 0 {
		return nil
	}

	type neighbourUpdate struct {
		key          string
		warmthBoost  float64
	}

	// Collect all (neighbour, boost) pairs for all source keys.
	// We de-duplicate by keeping the highest boost for each neighbour key.
	bestBoost := make(map[string]float64)
	// traversed records the (src, neighbour) pairs whose edges were scanned so
	// their last_traversed_at can be stamped after spreading.
	var traversed [][2]string

	for _, srcKey := range accessedKeys {
		// Fetch source salience from LTM. If the key isn't in LTM we still
		// proceed with a default salience so spreading works even for freshly-
		// stored entries that haven't had their salience recomputed yet.
		var srcSalience float64
		row := b.db.QueryRow(`SELECT salience FROM brain_ltm WHERE key = ?`, srcKey)
		if err := row.Scan(&srcSalience); err != nil {
			srcSalience = 0.5 // sensible default when not found / any error
		}

		// Fetch direct neighbours (both directions of the undirected edge).
		// Filter by minConfidenceThreshold to exclude weak edges from spreading.
		// Also fetch access_count for usage-weighted confidence.
		rows, err := b.db.Query(`
			SELECT key_b AS neighbour, confidence, COALESCE(access_count, 0)
			  FROM brain_relationships WHERE key_a = ? AND confidence >= ?
			UNION
			SELECT key_a AS neighbour, confidence, COALESCE(access_count, 0)
			  FROM brain_relationships WHERE key_b = ? AND confidence >= ?
		`, srcKey, b.minConfidenceThreshold, srcKey, b.minConfidenceThreshold)
		if err != nil {
			return fmt.Errorf("spread activation neighbours for %q: %w", srcKey, err)
		}

		for rows.Next() {
			var neighbour string
			var confidence float64
			var accessCount int
			if err := rows.Scan(&neighbour, &confidence, &accessCount); err != nil {
				rows.Close()
				return fmt.Errorf("spread activation scan: %w", err)
			}
			// Usage-weighted confidence: frequently-traversed edges get a boost.
			// effective_confidence = base_confidence * (1 + alpha * log1p(access_count))
			effectiveConf := confidence * (1 + b.edgeAccessAlpha*math.Log1p(float64(accessCount)))
			boost := b.spreadingDecay * srcSalience * effectiveConf
			// Cap at 1.0 to keep warmth in [0, 1].
			boost = math.Min(boost, 1.0)
			if boost > bestBoost[neighbour] {
				bestBoost[neighbour] = boost
			}
			traversed = append(traversed, [2]string{srcKey, neighbour})
		}
		rows.Close()
	}

	if len(bestBoost) == 0 {
		return nil // no relationships found — clean no-op
	}

	// Collect updates and batch them into a single transaction.
	updates := make([]neighbourUpdate, 0, len(bestBoost))
	for key, boost := range bestBoost {
		updates = append(updates, neighbourUpdate{key: key, warmthBoost: boost})
	}

	err := database.RetryOnBusy(5, func() error {
		tx, err := b.db.Begin()
		if err != nil {
			return fmt.Errorf("spread activation begin tx: %w", err)
		}
		stmt, err := tx.Prepare(`
			UPDATE brain_ltm
			   SET warmth = MIN(1.0, MAX(warmth, ?))
			 WHERE key = ?
		`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("spread activation prepare: %w", err)
		}
		defer stmt.Close()

		for _, u := range updates {
			if _, err := stmt.Exec(u.warmthBoost, u.key); err != nil {
				tx.Rollback()
				return fmt.Errorf("spread activation update %q: %w", u.key, err)
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}

	// Record metrics: one spread event per call, boost samples for averaging.
	b.mu.Lock()
	b.spreadEvents++
	for _, u := range updates {
		b.totalBoost += u.warmthBoost
		b.totalBoostCount++
	}
	b.mu.Unlock()

	// Stamp last_traversed_at and increment access_count on every edge we
	// traversed. Best-effort: a failure here only delays decay/boost by one
	// autoFlush cycle, so we don't propagate.
	if len(traversed) > 0 {
		_ = database.RetryOnBusy(3, func() error {
			tx, err := b.db.Begin()
			if err != nil {
				return err
			}
			stmp, err := tx.Prepare(`
				UPDATE brain_relationships
				   SET last_traversed_at = datetime('now'),
				       access_count = access_count + 1
				 WHERE (key_a = ? AND key_b = ?) OR (key_a = ? AND key_b = ?)
			`)
			if err != nil {
				tx.Rollback()
				return err
			}
			defer stmp.Close()
			for _, pair := range traversed {
				if _, err := stmp.Exec(pair[0], pair[1], pair[1], pair[0]); err != nil {
					tx.Rollback()
					return err
				}
			}
			return tx.Commit()
		})
	}

	return nil
}

// DecayEdgeConfidence multiplies the confidence of edges that haven't been
// traversed in the recent past by decayFactor, then deletes any edge whose
// confidence falls below pruneThreshold. Recently-traversed edges (within
// idleWindow) are left alone.
//
// Additionally, edge access_count is decayed by edgeAccessDecay each flush,
// preventing stale usage from permanently inflating confidence. Access counts
// that drop below 1.0 are zeroed to avoid floating-point dust.
//
// Called from autoFlush when spreadingEnabled is true. No-op otherwise.
func (b *Brain) DecayEdgeConfidence(decayFactor, pruneThreshold float64, idleWindow time.Duration) error {
	if !b.spreadingEnabled {
		return nil
	}
	// SQLite accepts datetime modifiers like '-6 hours'. Build the window
	// modifier dynamically from the requested Go duration.
	minutes := int(idleWindow.Minutes())
	if minutes < 0 {
		minutes = 0
	}
	windowModifier := fmt.Sprintf("-%d minutes", minutes)

	return database.RetryOnBusy(3, func() error {
		if _, err := b.db.Exec(`
			UPDATE brain_relationships
			   SET confidence = confidence * ?
			 WHERE confidence > 0.0
			   AND (last_traversed_at IS NULL
			        OR last_traversed_at < strftime('%Y-%m-%d %H:%M:%f', 'now', ?))
		`, decayFactor, windowModifier); err != nil {
			return err
		}
		// Decay access_count: multiply by edgeAccessDecay, zero dust below 1.0.
		if _, err := b.db.Exec(`
			UPDATE brain_relationships
			   SET access_count = CASE
			         WHEN CAST(access_count AS REAL) * ? < 1.0 THEN 0
			         ELSE CAST(CAST(access_count AS REAL) * ? AS INTEGER)
			       END
			 WHERE access_count > 0
		`, b.edgeAccessDecay, b.edgeAccessDecay); err != nil {
			return err
		}
		_, err := b.db.Exec(`DELETE FROM brain_relationships WHERE confidence < ?`, pruneThreshold)
		return err
	})
}

// DecayWarmth applies the per-flush warmth decay to all LTM entries that have
// a non-zero warmth value. A decay factor of 0.95 is applied; entries that
// drop below 0.01 are zeroed to avoid floating-point dust accumulation.
//
// This is called from autoFlush() when spreadingEnabled is true. It is also
// exported so tests can invoke it directly without waiting for the flush timer.
func (b *Brain) DecayWarmth(decayFactor float64) error {
	if !b.spreadingEnabled {
		return nil
	}
	return database.RetryOnBusy(3, func() error {
		_, err := b.db.Exec(`
			UPDATE brain_ltm
			   SET warmth = CASE
			         WHEN warmth * ? < 0.01 THEN 0.0
			         ELSE warmth * ?
			       END
			 WHERE warmth > 0.0
		`, decayFactor, decayFactor)
		return err
	})
}

// GetWarmth returns the current warmth value for a key in LTM. Returns 0 if
// the key is not found. Primarily useful for tests.
func (b *Brain) GetWarmth(key string) (float64, error) {
	var warmth float64
	err := b.db.QueryRow(`SELECT warmth FROM brain_ltm WHERE key = ?`, key).Scan(&warmth)
	if err != nil {
		return 0, nil // not found or any error → zero warmth
	}
	return warmth, nil
}

// StoreRelationship inserts or replaces a relationship in brain_relationships.
// Both key_a and key_b must already exist (or will be created on next store) —
// there is no FK constraint. Confidence should be in [0, 1].
//
// This is a convenience helper primarily for tests and external callers that
// want to seed the relationship graph without running a full REM cycle.
func (b *Brain) StoreRelationship(keyA, keyB string, relationship string, confidence float64) error {
	if keyA > keyB {
		keyA, keyB = keyB, keyA // canonical ordering: lexically smaller key first
	}
	return database.RetryOnBusy(5, func() error {
		_, err := b.db.Exec(`
			INSERT OR REPLACE INTO brain_relationships (key_a, key_b, relationship, confidence)
			VALUES (?, ?, ?, ?)
		`, keyA, keyB, relationship, confidence)
		return err
	})
}

// EffectiveEdgeConfidence computes the usage-weighted confidence for an edge.
// The formula is: base_confidence * (1 + alpha * log1p(access_count)).
//
// This creates a positive feedback loop where edges frequently traversed
// during recall/spreading get a confidence boost, making them stronger
// conduits for future activation. With default alpha=0.1:
//   - 0 accesses: effective = base * 1.0 (no change)
//   - 10 accesses: effective = base * 1.239
//   - 100 accesses: effective = base * 1.460
//   - 1000 accesses: effective = base * 1.691
func EffectiveEdgeConfidence(baseConfidence float64, accessCount int, alpha float64) float64 {
	return baseConfidence * (1 + alpha*math.Log1p(float64(accessCount)))
}
