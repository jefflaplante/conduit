package brain

import (
	"fmt"
	"strings"

	"conduit/internal/database"
)

// namespaceEdgeConfidence returns an edge-confidence value in [0.3, 0.9] based
// on the depth of the longest shared dotted prefix between two keys. Keys that
// share more namespace segments are assumed to be more tightly related.
//
//	"solar.battery.config" vs "solar.battery.plan"  → 2 shared segments → 0.9
//	"solar.battery.config" vs "solar.inverter"      → 1 shared segment  → 0.6
//	"solar.foo"            vs "house.foo"           → 0 shared segments → 0.3
func namespaceEdgeConfidence(keyA, keyB string) float64 {
	if keyA == keyB {
		return 0.0
	}
	a := strings.Split(keyA, ".")
	b := strings.Split(keyB, ".")
	shared := 0
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			break
		}
		shared++
	}
	switch {
	case shared >= 3:
		return 0.9
	case shared == 2:
		return 0.9
	case shared == 1:
		return 0.6
	default:
		return 0.3
	}
}

// flushPendingEdges materializes namespace edges for LTM keys that have been
// stored since the last flush. For each pending key, it scans LTM for existing
// keys sharing a dotted-namespace prefix (depth ≥ MinPrefixLength) and inserts
// or updates canonical edges in brain_relationships.
//
// Edge confidence is scaled by the depth of the longest shared prefix (see
// namespaceEdgeConfidence). Existing edges are upserted, so repeated
// observations do not duplicate rows.
//
// No-op when spreadingEnabled is false or when there are no pending keys.
// Errors are non-fatal and logged implicitly; partial progress is retained.
func (b *Brain) flushPendingEdges() error {
	if !b.spreadingEnabled {
		return nil
	}
	b.mu.Lock()
	if len(b.pendingEdgeKeys) == 0 {
		b.mu.Unlock()
		return nil
	}
	pending := make([]string, 0, len(b.pendingEdgeKeys))
	for k := range b.pendingEdgeKeys {
		pending = append(pending, k)
	}
	b.pendingEdgeKeys = make(map[string]struct{})
	b.mu.Unlock()

	edges := b.computeNamespaceEdges(pending)
	if len(edges) == 0 {
		return nil
	}

	return database.RetryOnBusy(5, func() error {
		tx, err := b.db.Begin()
		if err != nil {
			return fmt.Errorf("flush pending edges begin: %w", err)
		}
		stmt, err := tx.Prepare(`
			INSERT INTO brain_relationships (key_a, key_b, relationship, confidence)
			VALUES (?, ?, 'namespace', ?)
			ON CONFLICT(key_a, key_b) DO UPDATE SET
				confidence = MAX(confidence, excluded.confidence)
		`)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("flush pending edges prepare: %w", err)
		}
		defer stmt.Close()
		for _, e := range edges {
			if _, err := stmt.Exec(e.a, e.b, e.confidence); err != nil {
				tx.Rollback()
				return fmt.Errorf("flush pending edges exec: %w", err)
			}
		}
		return tx.Commit()
	})
}

// pendingEdge is a canonical edge candidate (key_a < key_b) with computed confidence.
type pendingEdge struct {
	a, b       string
	confidence float64
}

// computeNamespaceEdges finds existing LTM keys sharing a dotted prefix with
// each pending key and returns canonical edge candidates. Pairs among the
// pending set itself are also included (both sides are in LTM).
func (b *Brain) computeNamespaceEdges(pending []string) []pendingEdge {
	const minPrefix = 4

	seen := make(map[string]bool, len(pending)*4)
	var edges []pendingEdge

	for _, key := range pending {
		prefixes := namespacePrefixes(key, minPrefix)
		if len(prefixes) == 0 {
			continue
		}
		conds := make([]string, 0, len(prefixes))
		args := make([]interface{}, 0, len(prefixes)*2+1)
		for _, p := range prefixes {
			conds = append(conds, "(key LIKE ? OR key = ?)")
			args = append(args, p+".%", p)
		}
		args = append(args, key)
		q := fmt.Sprintf(
			`SELECT key FROM brain_ltm WHERE (%s) AND key != ? LIMIT 50`,
			strings.Join(conds, " OR "),
		)
		rows, err := b.db.Query(q, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var nk string
			if rows.Scan(&nk) != nil {
				continue
			}
			a, c := key, nk
			if a > c {
				a, c = c, a
			}
			dedupKey := a + "\x00" + c
			if seen[dedupKey] {
				continue
			}
			seen[dedupKey] = true
			edges = append(edges, pendingEdge{a: a, b: c, confidence: namespaceEdgeConfidence(a, c)})
		}
		rows.Close()
	}
	return edges
}
