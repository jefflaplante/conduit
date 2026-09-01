package rem

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"conduit/internal/brain"
)

// maxNodeDegree is the maximum number of edges allowed per node in the brain graph.
// This prevents unbounded edge accumulation that degrades performance and search quality.
const maxNodeDegree = 25

// namespacePairCap caps the number of pair candidates generated per namespace prefix.
// Without this, a namespace with N entries generates N*(N-1)/2 pairs (quadratic explosion).
// Example: learned.* with 248 entries → 30,628 pairs (noise, not signal). Cap at 50.
const namespacePairCap = 50

// relationshipCandidate represents a potential relationship between two keys
type relationshipCandidate struct {
	keyA       string
	keyB       string
	confidence float64
	reason     string
}

// Integrate detects relationships between stored memories.
//
// When manual is false (the auto cron path) the run is gated to the configured
// integration day so the expensive O(N²) LTM sweep only fires once a week.
// When manual is true (caller explicitly requested the integration phase) the
// gate is bypassed: explicit user intent overrides the schedule.
func (r *REMCycle) Integrate(ctx context.Context, dryRun, manual bool) (*IntegrationResult, error) {
	result := &IntegrationResult{
		RelationshipsCreated: 0,
		Patterns:             []string{},
	}

	if !manual && !r.shouldRunIntegration() {
		return result, nil // Skip with empty result
	}

	// Fetch all LTM entries
	entries, err := r.fetchAllLTMEntries(ctx)
	if err != nil {
		return result, fmt.Errorf("fetch LTM entries: %w", err)
	}

	if len(entries) == 0 {
		return result, nil
	}

	// Detect relationships
	candidates := r.detectRelationships(entries)

	// Insert relationships unless dry run
	if !dryRun {
		for _, candidate := range candidates {
			if err := r.insertRelationship(ctx, candidate); err != nil {
				return result, fmt.Errorf("insert relationship: %w", err)
			}
			result.RelationshipsCreated++
		}
	} else {
		result.RelationshipsCreated = len(candidates)
	}

	// Detect patterns
	patterns := r.detectPatterns(entries)
	result.Patterns = patterns

	return result, nil
}

// shouldRunIntegration checks if today matches the configured integration day
func (r *REMCycle) shouldRunIntegration() bool {
	return int(time.Now().Weekday()) == r.config.IntegrationDay
}

// fetchAllLTMEntries retrieves all long-term memory entries from the database
func (r *REMCycle) fetchAllLTMEntries(ctx context.Context) ([]brain.Entry, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT key, value, created_at, accessed_at, access_count, salience, source, stale
		FROM brain_ltm
		ORDER BY key
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []brain.Entry
	for rows.Next() {
		var e brain.Entry
		e.Tier = brain.TierLongTerm
		var staleInt int
		if err := rows.Scan(&e.Key, &e.Value, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &e.Salience, &e.Source, &staleInt); err != nil {
			return nil, err
		}
		e.Stale = staleInt != 0
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// detectRelationships finds related keys via namespace and token overlap
func (r *REMCycle) detectRelationships(entries []brain.Entry) []relationshipCandidate {
	var candidates []relationshipCandidate

	// Build namespace index
	namespaceMap := make(map[string][]string)
	for _, e := range entries {
		parts := strings.Split(e.Key, ".")
		for i := 1; i <= len(parts)-1; i++ {
			prefix := strings.Join(parts[:i], ".")
			namespaceMap[prefix] = append(namespaceMap[prefix], e.Key)
		}
	}

	// Build token index for each entry
	tokenMap := make(map[string][]string) // key -> tokens
	for _, e := range entries {
		tokens := brain.TokenizeQuery(e.Value)
		tokenMap[e.Key] = tokens
	}

	// Build entry map for efficient salience lookup
	entryMap := make(map[string]brain.Entry)
	for _, e := range entries {
		entryMap[e.Key] = e
	}

	// Track seen pairs to avoid duplicates
	seen := make(map[string]bool)

	// 1. Detect namespace relationships
	for prefix, keys := range namespaceMap {
		if len(keys) >= 2 {
			// Sort keys alphabetically for deterministic behavior
			sort.Strings(keys)

			// Cap candidates per namespace to prevent quadratic explosion
			// Example: learned.* with 248 entries → 30,628 pairs → cap at 50
			var pairsInNamespace int
			for i := 0; i < len(keys) && pairsInNamespace < namespacePairCap; i++ {
				for j := i + 1; j < len(keys) && pairsInNamespace < namespacePairCap; j++ {
					keyA, keyB := keys[i], keys[j]
					if keyA > keyB {
						keyA, keyB = keyB, keyA
					}
					pairKey := keyA + "|" + keyB
					if !seen[pairKey] {
						// Skip pairs where both entries have low salience (< 0.5)
						// Only add if at least one entry has meaningful importance
						entryA, okA := entryMap[keyA]
						entryB, okB := entryMap[keyB]

						// Only create candidate if both entries exist and at least one has salience >= 0.5
						if okA && okB && (entryA.Salience >= 0.5 || entryB.Salience >= 0.5) {
							candidates = append(candidates, relationshipCandidate{
								keyA:       keyA,
								keyB:       keyB,
								confidence: 0.7,
								reason:     fmt.Sprintf("shared namespace: %s", prefix),
							})
							seen[pairKey] = true
							pairsInNamespace++
						}
					}
				}
			}
		}
	}

	// 2. Detect token overlap relationships
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			keyA, keyB := entries[i].Key, entries[j].Key
			if keyA > keyB {
				keyA, keyB = keyB, keyA
			}
			pairKey := keyA + "|" + keyB

			// Skip if already related via namespace
			if seen[pairKey] {
				continue
			}

			tokensA := tokenMap[entries[i].Key]
			tokensB := tokenMap[entries[j].Key]

			// Require both entries to have at least 2 non-stopword tokens
			// This eliminates noise from very short or generic entries
			if len(tokensA) < 2 || len(tokensB) < 2 {
				continue
			}

			// Calculate Jaccard similarity
			overlap := countOverlap(tokensA, tokensB)
			union := len(tokensA) + len(tokensB) - overlap

			if union > 0 && overlap > 0 {
				similarity := float64(overlap) / float64(union)
				// Require at least 60% similarity (raised from 0.3 to eliminate noise)
				// The 42,500 candidates at 0.3 were overwhelmingly stopword/token overlap noise
				if similarity >= 0.6 {
					confidence := 0.4 + (similarity * 0.2) // Range: 0.52 to 0.64
					candidates = append(candidates, relationshipCandidate{
						keyA:       keyA,
						keyB:       keyB,
						confidence: confidence,
						reason:     fmt.Sprintf("token overlap: %.0f%%", similarity*100),
					})
					seen[pairKey] = true
				}
			}
		}
	}

	return candidates
}

// countOverlap counts how many tokens are shared between two token lists
func countOverlap(a, b []string) int {
	tokenSet := make(map[string]bool)
	for _, t := range a {
		tokenSet[t] = true
	}
	count := 0
	for _, t := range b {
		if tokenSet[t] {
			count++
		}
	}
	return count
}

// insertRelationship inserts or updates a relationship in the database.
// It enforces maxNodeDegree by evicting the lowest-confidence edge if necessary.
func (r *REMCycle) insertRelationship(ctx context.Context, candidate relationshipCandidate) error {
	// Normalize so keyA < keyB for consistency
	keyA, keyB := candidate.keyA, candidate.keyB
	if keyA > keyB {
		keyA, keyB = candidate.keyB, candidate.keyA
	}

	// Check if we would exceed degree cap for keyA
	var countA int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?",
		keyA, keyA).Scan(&countA); err != nil {
		return fmt.Errorf("count edges for keyA: %w", err)
	}

	// Check if we would exceed degree cap for keyB
	var countB int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?",
		keyB, keyB).Scan(&countB); err != nil {
		return fmt.Errorf("count edges for keyB: %w", err)
	}

	// If either node is at cap, we need to make room
	if countA >= maxNodeDegree || countB >= maxNodeDegree {
		// Find the lowest-confidence edge to evict
		var evictKeyA, evictKeyB string
		var evictConfidence float64

		if countA >= maxNodeDegree {
			err := r.db.QueryRowContext(ctx,
				`SELECT key_a, key_b, confidence
				 FROM brain_relationships
				 WHERE key_a = ? OR key_b = ?
				 ORDER BY confidence ASC LIMIT 1`,
				keyA, keyA).Scan(&evictKeyA, &evictKeyB, &evictConfidence)
			if err != nil {
				return fmt.Errorf("find lowest edge for keyA: %w", err)
			}

			// Only evict if new edge has higher confidence
			if candidate.confidence > evictConfidence {
				if _, err := r.db.ExecContext(ctx,
					"DELETE FROM brain_relationships WHERE key_a = ? AND key_b = ?",
					evictKeyA, evictKeyB); err != nil {
					return fmt.Errorf("evict low-confidence edge for keyA: %w", err)
				}
			} else {
				// New edge is not better than existing ones; skip insertion
				return nil
			}
		}

		if countB >= maxNodeDegree {
			// Re-check countB as it may have changed if we evicted an edge above
			var newCountB int
			if err := r.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM brain_relationships WHERE key_a = ? OR key_b = ?",
				keyB, keyB).Scan(&newCountB); err != nil {
				return fmt.Errorf("re-count edges for keyB: %w", err)
			}

			if newCountB >= maxNodeDegree {
				err := r.db.QueryRowContext(ctx,
					`SELECT key_a, key_b, confidence
					 FROM brain_relationships
					 WHERE key_a = ? OR key_b = ?
					 ORDER BY confidence ASC LIMIT 1`,
					keyB, keyB).Scan(&evictKeyA, &evictKeyB, &evictConfidence)
				if err != nil {
					return fmt.Errorf("find lowest edge for keyB: %w", err)
				}

				// Only evict if new edge has higher confidence
				if candidate.confidence > evictConfidence {
					if _, err := r.db.ExecContext(ctx,
						"DELETE FROM brain_relationships WHERE key_a = ? AND key_b = ?",
						evictKeyA, evictKeyB); err != nil {
						return fmt.Errorf("evict low-confidence edge for keyB: %w", err)
					}
				} else {
					// New edge is not better than existing ones; skip insertion
					return nil
				}
			}
		}
	}

	// Insert or replace the relationship
	_, err := r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO brain_relationships (key_a, key_b, relationship, confidence)
		VALUES (?, ?, 'related', ?)
	`, keyA, keyB, candidate.confidence)
	return err
}

// detectPatterns identifies interesting patterns in the memory store
func (r *REMCycle) detectPatterns(entries []brain.Entry) []string {
	var patterns []string

	// Count keys per namespace prefix
	namespaceCounts := make(map[string]int)
	for _, e := range entries {
		parts := strings.Split(e.Key, ".")
		if len(parts) > 1 {
			// Use first component as primary namespace
			namespace := parts[0]
			namespaceCounts[namespace]++
		}
	}

	// Report namespaces with significant entries
	for namespace, count := range namespaceCounts {
		if count >= 5 {
			patterns = append(patterns, fmt.Sprintf("You have %d entries about %s", count, namespace))
		}
	}

	// Detect high-salience clusters
	highSalience := 0
	for _, e := range entries {
		if e.Salience > 0.7 {
			highSalience++
		}
	}
	if highSalience >= 3 {
		patterns = append(patterns, fmt.Sprintf("You have %d high-importance memories (salience > 0.7)", highSalience))
	}

	// Detect frequently accessed entries
	highAccess := 0
	for _, e := range entries {
		if e.AccessCount >= 5 {
			highAccess++
		}
	}
	if highAccess >= 3 {
		patterns = append(patterns, fmt.Sprintf("You have %d frequently referenced memories (accessed 5+ times)", highAccess))
	}

	return patterns
}
