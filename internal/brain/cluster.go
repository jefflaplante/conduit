package brain

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ClusterResult holds the result of a namespace cluster recall, including
// both direct keyword matches and cluster-expanded neighbours.
type ClusterResult struct {
	// Direct are entries that matched the query terms directly.
	Direct []*Entry `json:"direct"`
	// Cluster are entries discovered via namespace expansion from direct matches.
	// These share a namespace prefix with a direct match but didn't match the
	// query keywords themselves.
	Cluster []*Entry `json:"cluster"`
}

// clusterConfig controls namespace clustering behaviour.
type clusterConfig struct {
	// MaxDepth is the maximum BFS depth through namespace prefixes.
	// Depth 1 = same immediate parent (e.g. solar.battery.* and solar.inverter.*
	// share "solar" at depth 1). Default: 2.
	MaxDepth int
	// MaxClusterEntries caps the number of cluster-expanded entries returned.
	// Prevents noisy results from large namespaces. Default: 10.
	MaxClusterEntries int
	// MinPrefixLength requires namespace prefixes to be at least this many
	// characters before expanding. Avoids trivially short prefixes like "j"
	// matching everything under jeff.*. Default: 4.
	MinPrefixLength int
}

var defaultClusterConfig = clusterConfig{
	MaxDepth:          2,
	MaxClusterEntries: 10,
	MinPrefixLength:   4,
}

// namespacePrefixes returns all dot-separated prefixes of a key, ordered from
// longest (most specific) to shortest (most general). For example:
//
//	"learned.memory.spreading_activation" →
//	  ["learned.memory", "learned"]
//
// Prefixes shorter than minLen are excluded.
func namespacePrefixes(key string, minLen int) []string {
	parts := strings.Split(key, ".")
	var prefixes []string
	for i := 1; i < len(parts); i++ {
		prefix := strings.Join(parts[:i], ".")
		if len(prefix) >= minLen {
			prefixes = append(prefixes, prefix)
		}
	}
	// Reverse so most specific prefix comes first.
	for i, j := 0, len(prefixes)-1; i < j; i, j = i+1, j-1 {
		prefixes[i], prefixes[j] = prefixes[j], prefixes[i]
	}
	return prefixes
}

// clusterNeighbours discovers LTM entries that share namespace prefixes with
// the given seed keys but weren't already in the matched set. It performs a
// BFS traversal through namespace levels:
//
//   - Start from each seed key's immediate namespace (key minus last segment).
//   - At each depth level, collect all keys sharing the current prefixes.
//   - Expand to the next broader prefix level.
//   - Score by prefix depth: closer namespace = higher score.
//
// Only keys not already in the matched set are returned, ensuring cluster
// results are genuinely new discoveries.
func (b *Brain) clusterNeighbours(seedKeys []string, matchedKeys map[string]bool, cfg clusterConfig) ([]*Entry, error) {
	if len(seedKeys) == 0 {
		return nil, nil
	}

	// Build initial set of prefixes from seed keys.
	currentPrefixes := make(map[string]bool)
	for _, key := range seedKeys {
		for _, prefix := range namespacePrefixes(key, cfg.MinPrefixLength) {
			currentPrefixes[prefix] = true
		}
	}

	if len(currentPrefixes) == 0 {
		return nil, nil
	}

	// BFS through namespace levels.
	// discovered tracks all cluster entries found, keyed by entry key.
	type scoredEntry struct {
		entry    *Entry
		score    float64 // higher = more relevant
		depth    int     // BFS depth where discovered
	}
	discovered := make(map[string]*scoredEntry)

	for depth := 0; depth <= cfg.MaxDepth; depth++ {
		if len(currentPrefixes) == 0 {
			break
		}

		// Query all LTM keys matching any current prefix.
		prefixSlice := make([]string, 0, len(currentPrefixes))
		for p := range currentPrefixes {
			prefixSlice = append(prefixSlice, p)
		}

		// Build query: SELECT ... WHERE (key LIKE 'prefix1.%' OR key = 'prefix1' OR ...)
		var conditions []string
		var args []interface{}
		for _, prefix := range prefixSlice {
			conditions = append(conditions, "(key LIKE ? OR key = ?)")
			args = append(args, prefix+".%", prefix)
		}

		query := fmt.Sprintf(`
			SELECT key, value, created_at, accessed_at, access_count, salience, source, stale, warmth
			FROM brain_ltm
			WHERE (%s) AND (expires_at IS NULL OR expires_at > strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now'))
		`, strings.Join(conditions, " OR "))

		rows, err := b.db.Query(query, args...)
		if err != nil {
			return nil, fmt.Errorf("cluster neighbours query: %w", err)
		}

		var nextPrefixes []string
		for rows.Next() {
			entry := &Entry{Tier: TierLongTerm}
			var staleInt int
			if err := rows.Scan(&entry.Key, &entry.Value, &entry.CreatedAt,
				&entry.AccessedAt, &entry.AccessCount, &entry.Salience,
				&entry.Source, &staleInt, &entry.Warmth); err != nil {
				continue
			}
			entry.Stale = staleInt != 0

			// Skip if already a direct match or already discovered at a better depth.
			if matchedKeys[entry.Key] {
				continue
			}
			if existing, ok := discovered[entry.Key]; ok && existing.depth <= depth {
				continue
			}

			// Score: entries found at depth 0 (same immediate parent) score higher
			// than entries at depth 1 (grandparent level). Weight by salience.
			// depthScore decays: 1.0 at depth 0, 0.6 at depth 1, 0.3 at depth 2.
			depthScore := 1.0 / (1.0 + float64(depth)*1.5)
			blendedScore := depthScore*0.5 + entry.Salience*0.3 + entry.Warmth*0.2

			discovered[entry.Key] = &scoredEntry{
				entry: entry,
				score: blendedScore,
				depth: depth,
			}

			// For next BFS level, add this key's broader prefixes.
			for _, prefix := range namespacePrefixes(entry.Key, cfg.MinPrefixLength) {
				if !currentPrefixes[prefix] {
					nextPrefixes = append(nextPrefixes, prefix)
				}
			}
		}
		rows.Close()

		// Prepare next level: expand to broader prefixes from discovered entries.
		currentPrefixes = make(map[string]bool)
		for _, p := range nextPrefixes {
			currentPrefixes[p] = true
		}
	}

	// Sort by score descending and cap results.
	var results []*scoredEntry
	for _, se := range discovered {
		results = append(results, se)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > cfg.MaxClusterEntries {
		results = results[:cfg.MaxClusterEntries]
	}

	entries := make([]*Entry, len(results))
	for i, se := range results {
		entries[i] = se.entry
	}
	return entries, nil
}

// RecallWithCluster performs a recall query augmented by namespace clustering.
// It delegates to RecallWithContext which now internally performs cluster
// expansion when spreadingEnabled is true. The combined results are then
// separated into direct matches and cluster-expanded entries based on the
// ClusterHit flag.
//
// When spreadingEnabled is false, no cluster expansion occurs in
// RecallWithContext, so this method returns only direct matches with an
// empty cluster list.
func (b *Brain) RecallWithCluster(ctx context.Context, query string, limit int) (*ClusterResult, error) {
	if limit <= 0 {
		limit = 20
	}

	// RecallWithContext now performs cluster expansion internally when
	// spreading is enabled, returning a combined list with ClusterHit flags.
	combinedResults, err := b.RecallWithContext(ctx, query, limit, "")
	if err != nil {
		return nil, fmt.Errorf("cluster recall base: %w", err)
	}

	result := &ClusterResult{}

	if len(combinedResults) == 0 {
		return result, nil
	}

	// Separate combined results into direct and cluster based on the flag.
	for _, e := range combinedResults {
		if e.ClusterHit {
			result.Cluster = append(result.Cluster, e)
		} else {
			result.Direct = append(result.Direct, e)
		}
	}

	return result, nil
}
