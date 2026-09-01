package brain

import (
	"context"
	"fmt"
	"sort"
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
		return 1.0
	case shared == 2:
		return 0.9
	case shared == 1:
		return 0.7
	default:
		return 0.4
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

// BackfillConfig holds configuration for the one-shot edge backfill operation
type BackfillConfig struct {
	PerNodeCap int // Maximum edges to create per node (default: 5)
	GlobalCap  int // Maximum total edges to create (default: 2000)
}

// BackfillReport contains statistics from a backfill operation
type BackfillReport struct {
	TotalNodes      int // Total LTM nodes in database
	NodesProcessed  int // Nodes that were candidates for backfill
	EdgesCreated    int // Number of edges actually created
	EdgesSkipped    int // Edges not created due to caps or existing edges
	GlobalCap       int // Global cap configured for this backfill
}

// BackfillEdges performs a one-shot backfill of namespace edges for historical
// LTM nodes that lack connections. For each isolated node (no existing edges),
// it computes similarity to other LTM nodes via namespace overlap and creates
// edges up to the configured caps.
//
// This is designed to run once as a maintenance operation to fix the issue
// where only newly-written keys received edges, leaving historical data isolated.
func (b *Brain) BackfillEdges(ctx context.Context, config BackfillConfig) (*BackfillReport, error) {
	if !b.spreadingEnabled {
		return &BackfillReport{}, nil
	}

	// Apply defaults
	if config.PerNodeCap <= 0 {
		config.PerNodeCap = 5
	}
	if config.GlobalCap <= 0 {
		config.GlobalCap = 2000
	}

	report := &BackfillReport{GlobalCap: config.GlobalCap}
	edgesCreated := 0
	edgesSkipped := 0

	// Get all LTM nodes
	rows, err := b.db.Query("SELECT key FROM brain_ltm")
	if err != nil {
		return nil, fmt.Errorf("query LTM nodes: %w", err)
	}
	defer rows.Close()

	var allKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			continue
		}
		allKeys = append(allKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan LTM nodes: %w", err)
	}
	report.TotalNodes = len(allKeys)

	if len(allKeys) == 0 {
		return report, nil
	}

	// Build a set of existing edges to avoid duplicates, and track node degree
	// so per-node caps are respected across all iterations (a node can receive
	// edges both as a source and as a target of other nodes' backfill passes).
	existingEdges := make(map[string]bool)
	degree := make(map[string]int)
	edgeRows, err := b.db.Query("SELECT key_a, key_b FROM brain_relationships")
	if err != nil {
		return nil, fmt.Errorf("query existing edges: %w", err)
	}
	defer edgeRows.Close()
	for edgeRows.Next() {
		var a, b string
		if err := edgeRows.Scan(&a, &b); err != nil {
			continue
		}
		existingEdges[a+"\x00"+b] = true
		degree[a]++
		degree[b]++
	}

	// For each key, find isolated nodes and compute candidate edges
	seen := make(map[string]bool)
	var candidateEdges []pendingEdge

	for _, key := range allKeys {
		// Skip if this node already has edges
		var hasEdges bool
		checkErr := b.db.QueryRow(`
			SELECT EXISTS(
				SELECT 1 FROM brain_relationships
				WHERE key_a = ? OR key_b = ?
			)
		`, key, key).Scan(&hasEdges)
		if checkErr == nil && hasEdges {
			continue
		}

		// Find similar nodes using the same logic as flushPendingEdges
		prefixes := namespacePrefixes(key, 4)
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

		simRows, err := b.db.Query(q, args...)
		if err != nil {
			continue
		}

		var similarKeys []string
		type candidate struct {
			key        string
			confidence float64
		}
		var candidates []candidate

		for simRows.Next() {
			var nk string
			if simRows.Scan(&nk) != nil {
				continue
			}
			similarKeys = append(similarKeys, nk)
		}
		simRows.Close()

		// Compute confidence for each candidate
		for _, nk := range similarKeys {
			a, c := key, nk
			if a > c {
				a, c = c, a
			}
			dedupKey := a + "\x00" + c
			if seen[dedupKey] {
				continue
			}
			if existingEdges[dedupKey] {
				continue
			}
			seen[dedupKey] = true

			confidence := namespaceEdgeConfidence(a, c)
			candidates = append(candidates, candidate{key: dedupKey, confidence: confidence})
		}

		report.NodesProcessed++

		// Sort by confidence (highest first) and add up to PerNodeCap edges,
		// skipping any candidate whose endpoints have already hit the cap.
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].confidence > candidates[j].confidence
		})

		addedForSource := 0
		for _, cand := range candidates {
			if addedForSource >= config.PerNodeCap {
				break
			}
			parts := strings.Split(cand.key, "\x00")
			if len(parts) != 2 {
				continue
			}
			if degree[parts[0]] >= config.PerNodeCap || degree[parts[1]] >= config.PerNodeCap {
				continue
			}
			candidateEdges = append(candidateEdges, pendingEdge{
				a:          parts[0],
				b:          parts[1],
				confidence: cand.confidence,
			})
			degree[parts[0]]++
			degree[parts[1]]++
			addedForSource++
		}
	}

	// Sort all candidates by confidence globally and apply global cap
	sort.Slice(candidateEdges, func(i, j int) bool {
		return candidateEdges[i].confidence > candidateEdges[j].confidence
	})

	globalLimit := min(config.GlobalCap, len(candidateEdges))

	// Create edges in batches
	if globalLimit > 0 {
		err := database.RetryOnBusy(5, func() error {
			tx, err := b.db.Begin()
			if err != nil {
				return fmt.Errorf("backfill edges begin: %w", err)
			}
			stmt, err := tx.Prepare(`
				INSERT OR IGNORE INTO brain_relationships (key_a, key_b, relationship, confidence)
				VALUES (?, ?, 'namespace', ?)
			`)
			if err != nil {
				tx.Rollback()
				return fmt.Errorf("backfill edges prepare: %w", err)
			}
			defer stmt.Close()

			for i := 0; i < globalLimit; i++ {
				e := candidateEdges[i]
				res, err := stmt.Exec(e.a, e.b, e.confidence)
				if err != nil {
					tx.Rollback()
					return fmt.Errorf("backfill edges exec: %w", err)
				}
				rows, _ := res.RowsAffected()
				if rows > 0 {
					edgesCreated++
				} else {
					edgesSkipped++
				}
			}

			return tx.Commit()
		})
		if err != nil {
			return nil, err
		}
	}

	report.EdgesCreated = edgesCreated
	report.EdgesSkipped = edgesSkipped + (len(candidateEdges) - globalLimit)

	return report, nil
}
