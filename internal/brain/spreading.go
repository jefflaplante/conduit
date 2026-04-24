package brain

import (
	"fmt"
	"math"

	"conduit/internal/database"
)

// spreadActivation propagates activation from each accessed key to its direct
// neighbours in brain_relationships. The warmth boost delivered to each
// neighbour is:
//
//	warmth_boost = decay^distance * source_salience * edge_confidence
//
// This implementation supports distance=1 (direct neighbours) only. The
// neighbour's warmth is updated to max(current_warmth, warmth_boost) capped
// at 1.0, so a warm neighbour never gets dimmed by a weaker activation wave.
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
		rows, err := b.db.Query(`
			SELECT key_b AS neighbour, confidence
			  FROM brain_relationships WHERE key_a = ?
			UNION
			SELECT key_a AS neighbour, confidence
			  FROM brain_relationships WHERE key_b = ?
		`, srcKey, srcKey)
		if err != nil {
			return fmt.Errorf("spread activation neighbours for %q: %w", srcKey, err)
		}

		for rows.Next() {
			var neighbour string
			var confidence float64
			if err := rows.Scan(&neighbour, &confidence); err != nil {
				rows.Close()
				return fmt.Errorf("spread activation scan: %w", err)
			}
			boost := b.spreadingDecay * srcSalience * confidence
			// Cap at 1.0 to keep warmth in [0, 1].
			boost = math.Min(boost, 1.0)
			if boost > bestBoost[neighbour] {
				bestBoost[neighbour] = boost
			}
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

	return database.RetryOnBusy(5, func() error {
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
