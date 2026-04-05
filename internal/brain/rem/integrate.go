package rem

import (
	"context"
	"fmt"
	"strings"
	"time"

	"conduit/internal/brain"
)

// relationshipCandidate represents a potential relationship between two keys
type relationshipCandidate struct {
	keyA       string
	keyB       string
	confidence float64
	reason     string
}

// Integrate detects relationships between stored memories
func (r *REMCycle) Integrate(ctx context.Context, dryRun bool) (*IntegrationResult, error) {
	result := &IntegrationResult{
		RelationshipsCreated: 0,
		Patterns:             []string{},
	}

	// Only run on configured integration day (default Sunday = 0)
	if !r.shouldRunIntegration() {
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
		SELECT key, value, created_at, accessed_at, access_count, salience, source
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
		if err := rows.Scan(&e.Key, &e.Value, &e.CreatedAt, &e.AccessedAt, &e.AccessCount, &e.Salience, &e.Source); err != nil {
			return nil, err
		}
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

	// Track seen pairs to avoid duplicates
	seen := make(map[string]bool)

	// 1. Detect namespace relationships
	for prefix, keys := range namespaceMap {
		if len(keys) >= 2 {
			// Create relationships between keys sharing a namespace
			for i := 0; i < len(keys); i++ {
				for j := i + 1; j < len(keys); j++ {
					keyA, keyB := keys[i], keys[j]
					if keyA > keyB {
						keyA, keyB = keyB, keyA
					}
					pairKey := keyA + "|" + keyB
					if !seen[pairKey] {
						candidates = append(candidates, relationshipCandidate{
							keyA:       keyA,
							keyB:       keyB,
							confidence: 0.7,
							reason:     fmt.Sprintf("shared namespace: %s", prefix),
						})
						seen[pairKey] = true
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

			// Calculate Jaccard similarity
			overlap := countOverlap(tokensA, tokensB)
			union := len(tokensA) + len(tokensB) - overlap

			if union > 0 && overlap > 0 {
				similarity := float64(overlap) / float64(union)
				// Require at least 30% similarity
				if similarity >= 0.3 {
					confidence := 0.3 + (similarity * 0.3) // Range: 0.3 to 0.6
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

// insertRelationship inserts or updates a relationship in the database
func (r *REMCycle) insertRelationship(ctx context.Context, candidate relationshipCandidate) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO brain_relationships (key_a, key_b, relationship, confidence)
		VALUES (?, ?, 'related', ?)
	`, candidate.keyA, candidate.keyB, candidate.confidence)
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
