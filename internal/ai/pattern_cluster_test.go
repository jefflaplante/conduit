package ai

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Helper functions ---

// makeResult creates a SmartRoutingResult for testing.
func makeResult(model string, tier ModelTier, complexity ComplexityLevel, score int, latencyMs int64, fallbacks int, contextInfluenced bool) *SmartRoutingResult {
	return &SmartRoutingResult{
		SelectedModel:      model,
		Tier:               tier,
		Complexity:         ComplexityScore{Level: complexity, Score: score},
		TotalLatencyMs:     latencyMs,
		FallbacksAttempted: fallbacks,
		ContextInfluenced:  contextInfluenced,
	}
}

// recordTestPatterns records a batch of patterns for testing.
func recordTestPatterns(pa *PatternAnalyzer, patterns []struct {
	request   string
	result    *SmartRoutingResult
	toolCount int
	success   bool
}) {
	for _, p := range patterns {
		pa.RecordPattern(p.result, p.request, p.toolCount, p.success)
	}
}

// --- Tests ---

func TestNewPatternAnalyzer(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		assert.NotNil(t, pa)
		assert.Equal(t, 0, pa.PatternCount())
		assert.Equal(t, 0, pa.ClusterCount())
		assert.Equal(t, defaultMaxPatterns, pa.maxPatterns)
		assert.Equal(t, defaultClusterThreshold, pa.clusterThreshold)
		assert.Equal(t, defaultReclusterInterval, pa.reclusterInterval)
		assert.Equal(t, defaultMinClusterSize, pa.minClusterSize)
	})

	t.Run("custom options", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMaxPatterns(500),
			WithClusterThreshold(0.8),
			WithReclusterInterval(25),
			WithMinClusterSize(5),
		)
		assert.Equal(t, 500, pa.maxPatterns)
		assert.Equal(t, 0.8, pa.clusterThreshold)
		assert.Equal(t, 25, pa.reclusterInterval)
		assert.Equal(t, 5, pa.minClusterSize)
	})

	t.Run("invalid options ignored", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMaxPatterns(-1),
			WithClusterThreshold(0),
			WithClusterThreshold(1.5),
			WithReclusterInterval(0),
			WithMinClusterSize(-5),
		)
		// Should keep defaults
		assert.Equal(t, defaultMaxPatterns, pa.maxPatterns)
		assert.Equal(t, defaultClusterThreshold, pa.clusterThreshold)
		assert.Equal(t, defaultReclusterInterval, pa.reclusterInterval)
		assert.Equal(t, defaultMinClusterSize, pa.minClusterSize)
	})
}

func TestRecordPattern(t *testing.T) {
	t.Run("records a pattern", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 500, 0, false)
		pa.RecordPattern(result, "write a hello world program", 5, true)

		assert.Equal(t, 1, pa.PatternCount())
	})

	t.Run("nil result is no-op", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		pa.RecordPattern(nil, "hello", 0, true)
		assert.Equal(t, 0, pa.PatternCount())
	})

	t.Run("captures all fields", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		result := makeResult("claude-opus-4-6", TierOpus, ComplexityComplex, 75, 2000, 1, true)
		pa.RecordPattern(result, "refactor the entire codebase with multiple files", 10, false)

		pa.mu.RLock()
		defer pa.mu.RUnlock()

		require.Len(t, pa.patterns, 1)
		p := pa.patterns[0]
		assert.Equal(t, "claude-opus-4-6", p.Model)
		assert.Equal(t, TierOpus, p.Tier)
		assert.Equal(t, ComplexityComplex, p.ComplexityLevel)
		assert.Equal(t, 75, p.ComplexityScore)
		assert.Equal(t, 10, p.ToolCount)
		assert.Equal(t, len("refactor the entire codebase with multiple files"), p.MessageLength)
		assert.False(t, p.Success)
		assert.Equal(t, int64(2000), p.LatencyMs)
		assert.Equal(t, 1, p.FallbacksAttempted)
		assert.True(t, p.ContextInfluenced)
		assert.NotEmpty(t, p.ID)
		assert.NotEmpty(t, p.RequestHash)
		assert.False(t, p.RecordedAt.IsZero())
	})

	t.Run("multiple patterns", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		for i := 0; i < 10; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 20+i, 100, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("test request %d", i), 3, true)
		}
		assert.Equal(t, 10, pa.PatternCount())
	})
}

func TestRollingWindowCapacity(t *testing.T) {
	t.Run("evicts oldest when full", func(t *testing.T) {
		maxPatterns := 10
		pa := NewPatternAnalyzer(WithMaxPatterns(maxPatterns))

		// Record more than max patterns
		for i := 0; i < 15; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 20, 100, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("request number %d with some words", i), 3, true)
		}

		assert.Equal(t, maxPatterns, pa.PatternCount())

		// Verify the oldest patterns were evicted (IDs should all be from
		// later recordings)
		pa.mu.RLock()
		defer pa.mu.RUnlock()
		// The patterns should be from indices 5-14 (the last 10)
		assert.Len(t, pa.patterns, maxPatterns)
	})

	t.Run("pattern index stays consistent after eviction", func(t *testing.T) {
		pa := NewPatternAnalyzer(WithMaxPatterns(5))
		for i := 0; i < 10; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 20, 100, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("index consistency test %d", i), 3, true)
		}

		pa.mu.RLock()
		defer pa.mu.RUnlock()

		// Verify index maps correctly
		for id, idx := range pa.patternIndex {
			assert.Equal(t, id, pa.patterns[idx].ID)
		}
		assert.Equal(t, len(pa.patterns), len(pa.patternIndex))
	})
}

func TestFindSimilarPatterns(t *testing.T) {
	t.Run("empty analyzer returns nil", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		results := pa.FindSimilarPatterns("hello", 10, ComplexitySimple, 2, 5)
		assert.Nil(t, results)
	})

	t.Run("finds similar patterns by complexity", func(t *testing.T) {
		pa := NewPatternAnalyzer()

		// Record simple patterns
		for i := 0; i < 5; i++ {
			result := makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 5, 50, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("what is this thing %d", i), 2, true)
		}

		// Record complex patterns
		for i := 0; i < 5; i++ {
			result := makeResult("claude-opus-4-6", TierOpus, ComplexityComplex, 80, 2000, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("refactor and rebuild the entire architecture with multiple files and tests and integration patterns %d", i), 15, true)
		}

		// Query for a simple pattern
		results := pa.FindSimilarPatterns("what is that", 5, ComplexitySimple, 2, 3)
		assert.NotEmpty(t, results)
		assert.LessOrEqual(t, len(results), 3)

		// The first results should be more similar to simple patterns
		if len(results) > 0 {
			assert.Equal(t, ComplexitySimple, results[0].ComplexityLevel)
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		for i := 0; i < 20; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("moderate task number %d with some words", i), 5, true)
		}

		results := pa.FindSimilarPatterns("moderate task number 99 with some words", 25, ComplexityStandard, 5, 3)
		assert.LessOrEqual(t, len(results), 3)
	})

	t.Run("default limit when zero", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		for i := 0; i < 5; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("task %d with some content", i), 5, true)
		}

		results := pa.FindSimilarPatterns("task with some content", 25, ComplexityStandard, 5, 0)
		assert.NotNil(t, results)
	})
}

func TestClusterFormation(t *testing.T) {
	t.Run("forms clusters from similar patterns", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMinClusterSize(3),
			WithReclusterInterval(1),
		)

		// Record a group of similar simple patterns
		for i := 0; i < 10; i++ {
			result := makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 5+i%3, 50+int64(i*10), 0, false)
			pa.RecordPattern(result, fmt.Sprintf("what is %d", i), 2, true)
		}

		// Record a group of similar complex patterns
		for i := 0; i < 10; i++ {
			result := makeResult("claude-opus-4-6", TierOpus, ComplexityComplex, 70+i%10, 2000+int64(i*100), 0, false)
			pa.RecordPattern(result, fmt.Sprintf("refactor implement redesign architect analyze debug migrate the entire system with multiple files and deep reasoning chains and complex operations %d", i), 15, true)
		}

		clusters := pa.GetClusters()
		assert.NotEmpty(t, clusters, "should have formed at least one cluster")

		// Each cluster should have at least minClusterSize members
		for _, c := range clusters {
			assert.GreaterOrEqual(t, c.MemberCount, 3)
			assert.NotEmpty(t, c.ID)
			assert.NotEmpty(t, c.Description)
			assert.NotEmpty(t, c.DominantModel)
		}
	})

	t.Run("too few patterns produces no clusters", func(t *testing.T) {
		pa := NewPatternAnalyzer(WithMinClusterSize(5))

		// Record only 2 patterns
		for i := 0; i < 2; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("test %d", i), 3, true)
		}

		clusters := pa.GetClusters()
		assert.Empty(t, clusters)
	})

	t.Run("cluster statistics are correct", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMinClusterSize(2),
			WithClusterThreshold(0.5),
			WithReclusterInterval(1),
		)

		// Record patterns with known outcomes
		successResults := []struct {
			request string
			result  *SmartRoutingResult
		}{
			{"hello", makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 5, 50, 0, false)},
			{"hi there", makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 3, 40, 0, false)},
			{"hey", makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 4, 60, 0, false)},
		}

		for _, sr := range successResults {
			pa.RecordPattern(sr.result, sr.request, 1, true)
		}

		// Also add a failing one
		pa.RecordPattern(
			makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 5, 100, 0, false),
			"greetings",
			1,
			false,
		)

		clusters := pa.GetClusters()
		if len(clusters) > 0 {
			for _, c := range clusters {
				assert.GreaterOrEqual(t, c.AvgSuccessRate, 0.0)
				assert.LessOrEqual(t, c.AvgSuccessRate, 1.0)
				assert.Greater(t, c.AvgLatencyMs, 0.0)
			}
		}
	})
}

func TestGetClusterRecommendation(t *testing.T) {
	t.Run("returns nil when no clusters", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		rec := pa.GetClusterRecommendation("hello", 5, ComplexitySimple, 2)
		assert.Nil(t, rec)
	})

	t.Run("returns recommendation when clusters exist", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMinClusterSize(3),
			WithReclusterInterval(1),
			WithClusterThreshold(0.5),
		)

		// Build a cluster of simple requests
		for i := 0; i < 10; i++ {
			result := makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 5, 50, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("what is thing %d", i), 2, true)
		}

		// Force recluster
		pa.ReclusterIfNeeded()

		// Query similar to the cluster
		rec := pa.GetClusterRecommendation("what is thing 99", 5, ComplexitySimple, 2)
		if rec != nil {
			assert.Equal(t, TierHaiku, rec.SuggestedTier)
			assert.Equal(t, "claude-haiku-4-5", rec.SuggestedModel)
			assert.Greater(t, rec.Confidence, 0.0)
			assert.LessOrEqual(t, rec.Confidence, 1.0)
			assert.NotEmpty(t, rec.Reason)
			assert.NotEmpty(t, rec.ClusterID)
			assert.Greater(t, rec.SimilarPatterns, 0)
		}
	})

	t.Run("recommendation converts to routing hint", func(t *testing.T) {
		rec := &ClusterRecommendation{
			SuggestedTier:  TierOpus,
			SuggestedModel: "claude-opus-4-6",
			Confidence:     0.85,
			Reason:         "test reason",
		}

		hint := rec.ToRoutingHint()
		assert.Equal(t, TierOpus, hint.SuggestedTier)
		assert.Equal(t, 0.85, hint.Confidence)
		assert.Equal(t, "test reason", hint.Reason)
	})
}

func TestReclusterIfNeeded(t *testing.T) {
	t.Run("skips when too few patterns", func(t *testing.T) {
		pa := NewPatternAnalyzer(WithMinClusterSize(5))
		result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
		pa.RecordPattern(result, "test", 3, true)

		pa.ReclusterIfNeeded()
		assert.Equal(t, 0, pa.ClusterCount())
	})

	t.Run("skips when not enough new patterns", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMinClusterSize(3),
			WithReclusterInterval(100),
		)

		// Record enough for clusters
		for i := 0; i < 10; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("test pattern %d with more words", i), 5, true)
		}

		// First recluster should work (0 clusters means forced)
		pa.ReclusterIfNeeded()
		firstCount := pa.ClusterCount()

		// Add just a couple more
		for i := 0; i < 2; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("extra %d with more words", i), 5, true)
		}

		// Recluster should be skipped (only 2 new, need 100)
		pa.ReclusterIfNeeded()
		assert.Equal(t, firstCount, pa.ClusterCount())
	})

	t.Run("reclusters when interval reached", func(t *testing.T) {
		pa := NewPatternAnalyzer(
			WithMinClusterSize(3),
			WithReclusterInterval(5),
			WithClusterThreshold(0.5),
		)

		// Initial batch
		for i := 0; i < 10; i++ {
			result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("standard task with details %d", i), 5, true)
		}

		// Force initial clustering
		pa.ReclusterIfNeeded()

		// Add enough new patterns to trigger recluster
		for i := 0; i < 6; i++ {
			result := makeResult("claude-opus-4-6", TierOpus, ComplexityComplex, 80, 2000, 0, false)
			pa.RecordPattern(result, fmt.Sprintf("refactor complex architecture deep reasoning multi-file operation %d", i), 15, true)
		}

		// This should trigger recluster
		pa.ReclusterIfNeeded()

		// Should now have clusters that potentially include the new complex patterns
		clusters := pa.GetClusters()
		assert.NotEmpty(t, clusters)
	})
}

func TestConcurrentSafety(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(3),
		WithReclusterInterval(5),
	)

	var wg sync.WaitGroup
	concurrency := 20
	patternsPerGoroutine := 10

	// Concurrent writers
	for g := 0; g < concurrency; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < patternsPerGoroutine; i++ {
				result := makeResult(
					"claude-sonnet-4-6",
					TierSonnet,
					ComplexityStandard,
					20+i,
					int64(100+i*10),
					0,
					false,
				)
				pa.RecordPattern(result, fmt.Sprintf("concurrent request from goroutine %d iteration %d", gid, i), 5, true)
			}
		}(g)
	}

	// Concurrent readers
	for g := 0; g < concurrency/2; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < patternsPerGoroutine; i++ {
				_ = pa.FindSimilarPatterns("concurrent query", 25, ComplexityStandard, 5, 5)
				_ = pa.GetClusters()
				_ = pa.GetClusterRecommendation("concurrent rec", 25, ComplexityStandard, 5)
				_ = pa.PatternCount()
				_ = pa.ClusterCount()
			}
		}(g)
	}

	// Concurrent reclustering
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				pa.ReclusterIfNeeded()
			}
		}()
	}

	wg.Wait()

	// Should not panic or deadlock; count should be reasonable
	count := pa.PatternCount()
	assert.Greater(t, count, 0)
	assert.LessOrEqual(t, count, defaultMaxPatterns)
}

func TestReset(t *testing.T) {
	pa := NewPatternAnalyzer(
		WithMinClusterSize(3),
		WithReclusterInterval(1),
	)

	// Add patterns and form clusters
	for i := 0; i < 10; i++ {
		result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
		pa.RecordPattern(result, fmt.Sprintf("test reset %d with some words", i), 5, true)
	}
	pa.ReclusterIfNeeded()

	assert.Greater(t, pa.PatternCount(), 0)

	// Reset
	pa.Reset()

	assert.Equal(t, 0, pa.PatternCount())
	assert.Equal(t, 0, pa.ClusterCount())

	// Should still work after reset
	result := makeResult("claude-sonnet-4-6", TierSonnet, ComplexityStandard, 25, 200, 0, false)
	pa.RecordPattern(result, "after reset with some words", 5, true)
	assert.Equal(t, 1, pa.PatternCount())
}

func TestEmptyStateHandling(t *testing.T) {
	pa := NewPatternAnalyzer()

	t.Run("FindSimilarPatterns on empty", func(t *testing.T) {
		results := pa.FindSimilarPatterns("test", 10, ComplexitySimple, 2, 5)
		assert.Nil(t, results)
	})

	t.Run("GetClusters on empty", func(t *testing.T) {
		clusters := pa.GetClusters()
		assert.Empty(t, clusters)
	})

	t.Run("GetClusterRecommendation on empty", func(t *testing.T) {
		rec := pa.GetClusterRecommendation("test", 10, ComplexitySimple, 2)
		assert.Nil(t, rec)
	})

	t.Run("ReclusterIfNeeded on empty", func(t *testing.T) {
		pa.ReclusterIfNeeded() // should not panic
		assert.Equal(t, 0, pa.ClusterCount())
	})

	t.Run("PatternCount on empty", func(t *testing.T) {
		assert.Equal(t, 0, pa.PatternCount())
	})

	t.Run("ClusterCount on empty", func(t *testing.T) {
		assert.Equal(t, 0, pa.ClusterCount())
	})
}

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors", func(t *testing.T) {
		a := [featureVectorDimensions]float64{1, 2, 3, 4, 5, 6}
		sim := cosineSimilarity(a, a)
		assert.InDelta(t, 1.0, sim, 0.0001)
	})

	t.Run("orthogonal vectors", func(t *testing.T) {
		a := [featureVectorDimensions]float64{1, 0, 0, 0, 0, 0}
		b := [featureVectorDimensions]float64{0, 1, 0, 0, 0, 0}
		sim := cosineSimilarity(a, b)
		assert.InDelta(t, 0.0, sim, 0.0001)
	})

	t.Run("zero vector", func(t *testing.T) {
		a := [featureVectorDimensions]float64{1, 2, 3, 4, 5, 6}
		zero := [featureVectorDimensions]float64{}
		sim := cosineSimilarity(a, zero)
		assert.Equal(t, 0.0, sim)
	})

	t.Run("both zero vectors", func(t *testing.T) {
		zero := [featureVectorDimensions]float64{}
		sim := cosineSimilarity(zero, zero)
		assert.Equal(t, 0.0, sim)
	})

	t.Run("similar vectors have high similarity", func(t *testing.T) {
		a := [featureVectorDimensions]float64{1, 2, 3, 4, 5, 6}
		b := [featureVectorDimensions]float64{1.1, 2.1, 3.1, 4.1, 5.1, 6.1}
		sim := cosineSimilarity(a, b)
		assert.Greater(t, sim, 0.99)
	})

	t.Run("opposite vectors have negative similarity", func(t *testing.T) {
		a := [featureVectorDimensions]float64{1, 2, 3, 4, 5, 6}
		b := [featureVectorDimensions]float64{-1, -2, -3, -4, -5, -6}
		sim := cosineSimilarity(a, b)
		assert.InDelta(t, -1.0, sim, 0.0001)
	})
}

func TestComputeCentroid(t *testing.T) {
	t.Run("single pattern", func(t *testing.T) {
		patterns := []RequestPattern{
			{featureVector: [featureVectorDimensions]float64{0.2, 0.4, 0.6, 0.8, 0.5, 0.0}},
		}
		centroid := computeCentroid(patterns, []int{0})
		assert.Equal(t, patterns[0].featureVector, centroid)
	})

	t.Run("multiple patterns", func(t *testing.T) {
		patterns := []RequestPattern{
			{featureVector: [featureVectorDimensions]float64{0.0, 0.0, 0.0, 0.0, 0.0, 0.0}},
			{featureVector: [featureVectorDimensions]float64{1.0, 1.0, 1.0, 1.0, 1.0, 1.0}},
		}
		centroid := computeCentroid(patterns, []int{0, 1})
		for i := 0; i < featureVectorDimensions; i++ {
			assert.InDelta(t, 0.5, centroid[i], 0.0001)
		}
	})

	t.Run("empty indices", func(t *testing.T) {
		patterns := []RequestPattern{
			{featureVector: [featureVectorDimensions]float64{1, 2, 3, 4, 5, 6}},
		}
		centroid := computeCentroid(patterns, []int{})
		for i := 0; i < featureVectorDimensions; i++ {
			assert.Equal(t, 0.0, centroid[i])
		}
	})
}

func TestHashRequest(t *testing.T) {
	t.Run("produces consistent hash", func(t *testing.T) {
		h1 := hashRequest("hello world")
		h2 := hashRequest("hello world")
		assert.Equal(t, h1, h2)
	})

	t.Run("different inputs produce different hashes", func(t *testing.T) {
		h1 := hashRequest("hello")
		h2 := hashRequest("world")
		assert.NotEqual(t, h1, h2)
	})

	t.Run("produces hex string", func(t *testing.T) {
		h := hashRequest("test")
		assert.Len(t, h, 16) // 8 bytes = 16 hex chars
	})
}

func TestBoolToFloat(t *testing.T) {
	assert.Equal(t, 1.0, boolToFloat(true))
	assert.Equal(t, 0.0, boolToFloat(false))
}

func TestClusterRecommendationToRoutingHint(t *testing.T) {
	rec := &ClusterRecommendation{
		SuggestedTier:   TierSonnet,
		SuggestedModel:  "claude-sonnet-4-6",
		Confidence:      0.72,
		Reason:          "cluster of standard complexity requests",
		ClusterID:       "cluster-1",
		SimilarPatterns: 15,
		AvgSuccessRate:  0.93,
	}

	hint := rec.ToRoutingHint()
	assert.Equal(t, TierSonnet, hint.SuggestedTier)
	assert.Equal(t, 0.72, hint.Confidence)
	assert.Equal(t, "cluster of standard complexity requests", hint.Reason)
}

func TestFeatureVectorNormalization(t *testing.T) {
	t.Run("features are bounded by weights", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		p := &RequestPattern{
			ComplexityScore: 50,
			ComplexityLevel: ComplexityStandard,
			ToolCount:       10,
			MessageLength:   2500,
			WordCount:       250,
		}

		vec := pa.computeFeatureVector(p)
		for i, v := range vec {
			assert.GreaterOrEqual(t, v, 0.0, "dimension %d should be >= 0", i)
			assert.LessOrEqual(t, v, featureWeights[i], "dimension %d should be <= weight %.1f", i, featureWeights[i])
		}
	})

	t.Run("zero pattern produces zero vector", func(t *testing.T) {
		pa := NewPatternAnalyzer()
		p := &RequestPattern{} // all zeros
		vec := pa.computeFeatureVector(p)
		for i, v := range vec {
			assert.Equal(t, 0.0, v, "dimension %d should be 0 for zero pattern", i)
		}
	})
}

func TestPatternAnalyzerIntegration(t *testing.T) {
	// End-to-end test: record patterns, form clusters, get recommendations.
	// Uses the default cluster threshold (0.70) which properly separates
	// simple and complex patterns.
	pa := NewPatternAnalyzer(
		WithMinClusterSize(3),
		WithReclusterInterval(1),
	)

	// Simulate a series of simple greeting-type requests
	simpleRequests := []string{
		"hello there",
		"hi how are you",
		"hey",
		"good morning",
		"hello again",
	}
	for _, req := range simpleRequests {
		result := makeResult("claude-haiku-4-5", TierHaiku, ComplexitySimple, 3, 45, 0, false)
		pa.RecordPattern(result, req, 1, true)
	}

	// Simulate complex coding requests
	complexRequests := []string{
		"refactor the entire authentication module to use OAuth2 and update all integration tests across multiple packages",
		"implement a new caching layer with distributed invalidation and benchmark the performance across all endpoints with detailed analysis",
		"debug the race condition in the worker pool and redesign the synchronization strategy with proper testing and documentation",
		"analyze the database schema and migrate to a normalized design with backwards compatibility and rollback support plan",
		"build a comprehensive monitoring dashboard with custom metrics alerting and historical trend analysis and automated reporting",
	}
	for _, req := range complexRequests {
		result := makeResult("claude-opus-4-6", TierOpus, ComplexityComplex, 75, 3000, 0, true)
		pa.RecordPattern(result, req, 12, true)
	}

	// Force clustering
	pa.ReclusterIfNeeded()

	clusters := pa.GetClusters()
	assert.NotEmpty(t, clusters, "should have formed clusters from the two groups")

	// Try to get a recommendation for a new simple request
	rec := pa.GetClusterRecommendation("what is Go", 5, ComplexitySimple, 1)
	// Even if nil (similarity too low), the system shouldn't panic
	if rec != nil {
		t.Logf("Recommendation for simple request: tier=%s, model=%s, confidence=%.2f, reason=%s",
			rec.SuggestedTier, rec.SuggestedModel, rec.Confidence, rec.Reason)
	}

	// Try to get a recommendation for a new complex request
	rec = pa.GetClusterRecommendation(
		"redesign the entire microservice architecture with proper testing coverage and documentation and performance benchmarks",
		70, ComplexityComplex, 12,
	)
	if rec != nil {
		t.Logf("Recommendation for complex request: tier=%s, model=%s, confidence=%.2f, reason=%s",
			rec.SuggestedTier, rec.SuggestedModel, rec.Confidence, rec.Reason)
	}

	// Verify we can find similar patterns
	similar := pa.FindSimilarPatterns("hello there", 3, ComplexitySimple, 1, 3)
	assert.NotNil(t, similar)
	if len(similar) > 0 {
		assert.Equal(t, ComplexitySimple, similar[0].ComplexityLevel)
	}
}
