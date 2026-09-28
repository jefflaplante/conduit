package core

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"

	"conduit/internal/tools/types"
)

// rrfK is the constant used in Reciprocal Rank Fusion scoring: score = 1 / (k + rank).
// A value of 60 is standard in information retrieval literature.
const rrfK = 60

// searchMemoryFiles dispatches to the appropriate search strategy based on mode.
// In "hybrid" mode, it runs vector and FTS5 searches in parallel and merges
// results using Reciprocal Rank Fusion.
func (t *MemorySearchTool) searchMemoryFiles(ctx context.Context, query string, minScore float64, mode string) ([]MemoryResult, error) {
	switch mode {
	case "hybrid":
		return t.searchMemoryFilesHybrid(ctx, query)
	case "vector":
		return t.searchMemoryFilesVector(ctx, query)
	case "fts5":
		return t.searchMemoryFilesFTS(ctx, query)
	default: // "grep" fallback
		return t.searchMemoryFilesGrep(ctx, query, minScore)
	}
}

// searchMemoryFilesHybrid runs both vector (semantic) and FTS5 (keyword) searches
// in parallel, then merges results using Reciprocal Rank Fusion (RRF).
func (t *MemorySearchTool) searchMemoryFilesHybrid(ctx context.Context, query string) ([]MemoryResult, error) {
	// Fetch size is larger than what we return so RRF has good input
	const fetchSize = 30

	var (
		ftsResults    []MemoryResult
		vectorResults []MemoryResult
		ftsErr        error
		vectorErr     error
		wg            sync.WaitGroup
	)

	// Run both searches in parallel
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer types.RecoverPanic("MemorySearch FTS5 search", func(err error) { ftsErr = err })
		ftsResults, ftsErr = t.searchMemoryFilesFTS(ctx, query)
	}()
	go func() {
		defer wg.Done()
		defer types.RecoverPanic("MemorySearch vector search", func(err error) { vectorErr = err })
		vectorResults, vectorErr = t.searchMemoryFilesVectorRaw(ctx, query, fetchSize)
	}()
	wg.Wait()

	// Handle errors gracefully: if one fails, use the other
	if ftsErr != nil && vectorErr != nil {
		return nil, fmt.Errorf("both searches failed: fts5=%v, vector=%v", ftsErr, vectorErr)
	}
	if ftsErr != nil {
		log.Printf("Hybrid search: FTS5 failed (%v), using vector results only", ftsErr)
		return vectorResults, nil
	}
	if vectorErr != nil {
		log.Printf("Hybrid search: vector failed (%v), using FTS5 results only", vectorErr)
		return ftsResults, nil
	}

	// Read RRF weights from config (defaults: fts=1.0, vector=1.5)
	ftsWeight := 1.0
	vectorWeight := 1.5
	if t.services.ConfigMgr != nil {
		ftsWeight = t.services.ConfigMgr.Vector.GetFTSWeight()
		vectorWeight = t.services.ConfigMgr.Vector.GetVectorWeight()
	}

	// Merge using Reciprocal Rank Fusion with per-list weights
	merged := reciprocalRankFusion([]float64{ftsWeight, vectorWeight}, ftsResults, vectorResults)
	return merged, nil
}

// searchMemoryFilesVector performs semantic vector search only.
func (t *MemorySearchTool) searchMemoryFilesVector(ctx context.Context, query string) ([]MemoryResult, error) {
	return t.searchMemoryFilesVectorRaw(ctx, query, 20)
}

// searchMemoryFilesVectorRaw performs semantic vector search with a specified limit.
func (t *MemorySearchTool) searchMemoryFilesVectorRaw(ctx context.Context, query string, limit int) ([]MemoryResult, error) {
	if t.services.VectorSearch == nil {
		return nil, fmt.Errorf("vector search service not available")
	}

	vecResults, err := t.services.VectorSearch.Search(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	var results []MemoryResult
	for _, vr := range vecResults {
		content := vr.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}

		// Build context from metadata
		contextStr := vr.Content
		if title, ok := vr.Metadata["title"]; ok && title != "" {
			contextStr = title + "\n" + vr.Content
		}

		path := vr.ID
		if p, ok := vr.Metadata["path"]; ok && p != "" {
			path = p
		}

		results = append(results, MemoryResult{
			Path:       path,
			Content:    content,
			Score:      vr.Score,
			Context:    contextStr,
			Source:     "file",
			SearchType: "vector",
		})
	}

	return results, nil
}

// reciprocalRankFusion merges ranked result lists using Reciprocal Rank Fusion.
// Each list has a corresponding weight in the weights slice. A result's fused score
// is the sum of weight/(k + rank + 1) across all lists where it appears.
// If weights is nil, all lists default to weight 1.0.
// Results are identified by their content+path to handle deduplication.
func reciprocalRankFusion(weights []float64, lists ...[]MemoryResult) []MemoryResult {
	type fusedEntry struct {
		result MemoryResult
		score  float64
	}

	// Default weights to 1.0 for any missing entries
	if len(weights) < len(lists) {
		expanded := make([]float64, len(lists))
		copy(expanded, weights)
		for i := len(weights); i < len(lists); i++ {
			expanded[i] = 1.0
		}
		weights = expanded
	}

	// Map from dedup key to fused entry
	fused := make(map[string]*fusedEntry)

	for listIdx, list := range lists {
		w := weights[listIdx]
		for rank, result := range list {
			key := resultDeduplicationKey(result)
			rrfScore := w / float64(rrfK+rank+1) // rank is 0-based, so +1

			if existing, ok := fused[key]; ok {
				existing.score += rrfScore
				// Keep the result with more context or better metadata
				if len(result.Context) > len(existing.result.Context) {
					existing.result.Context = result.Context
				}
			} else {
				fused[key] = &fusedEntry{
					result: result,
					score:  rrfScore,
				}
			}
		}
	}

	// Normalize RRF scores to 0-1 range so they're comparable to raw vector/fts5 scores.
	// Max possible RRF score = sum(weights) / (k+1) when an item is ranked #0 in every list.
	var sumWeights float64
	for i := 0; i < len(lists); i++ {
		sumWeights += weights[i]
	}
	maxPossible := sumWeights / float64(rrfK+1)

	// Collect and sort by fused score
	results := make([]MemoryResult, 0, len(fused))
	for _, entry := range fused {
		entry.result.Score = entry.score / maxPossible
		entry.result.SearchType = "hybrid"
		results = append(results, entry.result)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

// resultDeduplicationKey generates a key for deduplicating results across search methods.
// For file results, we use path + a content prefix; for session results, session key + content prefix.
func resultDeduplicationKey(r MemoryResult) string {
	contentKey := r.Content
	if len(contentKey) > 100 {
		contentKey = contentKey[:100]
	}
	if r.Source == "session" {
		return "session:" + r.SessionKey + ":" + contentKey
	}
	return "file:" + r.Path + ":" + contentKey
}
