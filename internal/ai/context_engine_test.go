package ai

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/fts"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ContextEngine creation ---

func TestNewContextEngine_Defaults(t *testing.T) {
	engine := NewContextEngine()
	require.NotNil(t, engine)

	assert.Equal(t, 30*time.Second, engine.cacheTTL)
	assert.Equal(t, 5, engine.maxResults)
	assert.Equal(t, 2*time.Second, engine.searchTimeout)
	assert.Equal(t, 200, engine.maxContentLen)
	assert.Nil(t, engine.searchService)
	assert.Nil(t, engine.vectorService)
	assert.Nil(t, engine.usageTracker)
}

func TestNewContextEngine_WithOptions(t *testing.T) {
	search := &mockSearchService{}
	vector := &mockVectorService{}
	tracker := NewUsageTracker()

	engine := NewContextEngine(
		WithSearchService(search),
		WithVectorService(vector),
		WithUsageTracker(tracker),
		WithCacheTTL(10*time.Second),
		WithMaxResults(3),
		WithSearchTimeout(500*time.Millisecond),
	)

	assert.NotNil(t, engine.searchService)
	assert.NotNil(t, engine.vectorService)
	assert.NotNil(t, engine.usageTracker)
	assert.Equal(t, 10*time.Second, engine.cacheTTL)
	assert.Equal(t, 3, engine.maxResults)
	assert.Equal(t, 500*time.Millisecond, engine.searchTimeout)
}

func TestNewContextEngine_WithMaxResults_IgnoresZero(t *testing.T) {
	engine := NewContextEngine(WithMaxResults(0))
	assert.Equal(t, 5, engine.maxResults) // Should keep default
}

// --- RetrieveContext with no services ---

func TestRetrieveContext_NoServices(t *testing.T) {
	engine := NewContextEngine()
	rc := engine.RetrieveContext(context.Background(), "hello world")

	require.NotNil(t, rc)
	assert.True(t, rc.IsEmpty())
	assert.Equal(t, "none", rc.Source)
}

func TestRetrieveContext_EmptyRequest(t *testing.T) {
	engine := NewContextEngine()
	rc := engine.RetrieveContext(context.Background(), "")

	require.NotNil(t, rc)
	assert.Equal(t, "none", rc.Source)
	assert.True(t, rc.IsEmpty())
}

// --- RetrieveContext with FTS5 search ---

func TestRetrieveContext_FTS5Only(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID:  "msg-1",
				SessionKey: "session-abc",
				Role:       "user",
				Content:    "How do I configure the gateway?",
				Rank:       -5.2,
			},
			{
				MessageID:  "msg-2",
				SessionKey: "session-abc",
				Role:       "assistant",
				Content:    "You can configure the gateway by editing config.json with the following settings...",
				Rank:       -3.1,
			},
		},
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "gateway configuration")

	require.NotNil(t, rc)
	assert.False(t, rc.IsEmpty())
	assert.Equal(t, "fts5", rc.Source)
	assert.Len(t, rc.SimilarRequests, 2)
	assert.Equal(t, "fts5", rc.SimilarRequests[0].Source)
	assert.Equal(t, "session-abc", rc.SimilarRequests[0].SessionKey)
	assert.Greater(t, rc.SearchLatencyMs, int64(-1))
}

func TestRetrieveContext_FTS5Error_GracefulDegradation(t *testing.T) {
	search := &mockSearchService{
		err: fmt.Errorf("database locked"),
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "some query")

	// Should not panic or return nil — graceful degradation
	require.NotNil(t, rc)
	assert.True(t, rc.IsEmpty())
	assert.Equal(t, "none", rc.Source)
}

// --- RetrieveContext with vector search ---

func TestRetrieveContext_VectorOnly(t *testing.T) {
	vector := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:      "vec-1",
				Score:   0.92,
				Content: "How do I set up the database connection?",
				Metadata: map[string]string{
					"session_key": "session-xyz",
					"role":        "user",
				},
			},
		},
	}

	engine := NewContextEngine(WithVectorService(vector))
	rc := engine.RetrieveContext(context.Background(), "database setup")

	require.NotNil(t, rc)
	assert.False(t, rc.IsEmpty())
	assert.Equal(t, "vector", rc.Source)
	assert.Len(t, rc.SimilarRequests, 1)
	assert.Equal(t, "vector", rc.SimilarRequests[0].Source)
	assert.Equal(t, "session-xyz", rc.SimilarRequests[0].SessionKey)
	assert.Equal(t, "user", rc.SimilarRequests[0].Role)
	assert.InDelta(t, 0.92, rc.SimilarRequests[0].Score, 0.01)
}

func TestRetrieveContext_VectorError_GracefulDegradation(t *testing.T) {
	vector := &mockVectorService{
		err: fmt.Errorf("vector index not available"),
	}

	engine := NewContextEngine(WithVectorService(vector))
	rc := engine.RetrieveContext(context.Background(), "some query")

	require.NotNil(t, rc)
	assert.True(t, rc.IsEmpty())
	assert.Equal(t, "none", rc.Source)
}

// --- RetrieveContext with hybrid (both services) ---

func TestRetrieveContext_Hybrid(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID:  "msg-1",
				SessionKey: "session-1",
				Role:       "user",
				Content:    "FTS5 matched content here",
				Rank:       -4.0,
			},
		},
	}

	vector := &mockVectorService{
		results: []types.VectorSearchResult{
			{
				ID:      "vec-1",
				Score:   0.88,
				Content: "Vector matched content here",
				Metadata: map[string]string{
					"session_key": "session-2",
					"role":        "assistant",
				},
			},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithVectorService(vector),
	)
	rc := engine.RetrieveContext(context.Background(), "some query")

	require.NotNil(t, rc)
	assert.False(t, rc.IsEmpty())
	assert.Equal(t, "hybrid", rc.Source)
	// Vector results come first in merge
	assert.Len(t, rc.SimilarRequests, 2)
	assert.Equal(t, "vector", rc.SimilarRequests[0].Source)
	assert.Equal(t, "fts5", rc.SimilarRequests[1].Source)
}

func TestRetrieveContext_Hybrid_OneServiceFails(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID:  "msg-1",
				SessionKey: "session-1",
				Role:       "user",
				Content:    "FTS result",
				Rank:       -3.0,
			},
		},
	}

	vector := &mockVectorService{
		err: fmt.Errorf("vector unavailable"),
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithVectorService(vector),
	)
	rc := engine.RetrieveContext(context.Background(), "some query")

	require.NotNil(t, rc)
	assert.False(t, rc.IsEmpty())
	assert.Equal(t, "fts5", rc.Source) // Only FTS contributed
	assert.Len(t, rc.SimilarRequests, 1)
}

// --- Caching behavior ---

func TestRetrieveContext_CacheHit(t *testing.T) {
	callCount := 0
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID:  "msg-1",
				SessionKey: "session-1",
				Role:       "user",
				Content:    "cached content",
				Rank:       -2.0,
			},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithCacheTTL(5*time.Second),
	)

	// First call — cache miss, should search
	rc1 := engine.RetrieveContext(context.Background(), "test query")
	require.NotNil(t, rc1)
	assert.False(t, rc1.IsEmpty())
	callCount++

	// Second call with same query — should hit cache
	rc2 := engine.RetrieveContext(context.Background(), "test query")
	require.NotNil(t, rc2)
	assert.False(t, rc2.IsEmpty())

	// Results should be identical (same pointer from cache)
	assert.Equal(t, rc1, rc2)
}

func TestRetrieveContext_CacheExpiry(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID:  "msg-1",
				SessionKey: "session-1",
				Role:       "user",
				Content:    "content",
				Rank:       -1.0,
			},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithCacheTTL(50*time.Millisecond), // Very short TTL
	)

	rc1 := engine.RetrieveContext(context.Background(), "test query")
	require.NotNil(t, rc1)

	// Wait for cache to expire
	time.Sleep(100 * time.Millisecond)

	// Should be a cache miss now
	rc2 := engine.RetrieveContext(context.Background(), "test query")
	require.NotNil(t, rc2)

	// Different pointer means it was re-fetched
	assert.NotSame(t, rc1, rc2)
}

func TestRetrieveContext_CacheKeyNormalization(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID: "msg-1",
				Role:      "user",
				Content:   "result",
				Rank:      -1.0,
			},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithCacheTTL(5*time.Second),
	)

	// These should produce the same cache key (case-insensitive, trimmed)
	rc1 := engine.RetrieveContext(context.Background(), "  Test Query  ")
	rc2 := engine.RetrieveContext(context.Background(), "test query")

	assert.Equal(t, rc1, rc2) // Same cached result
}

func TestClearCache(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{MessageID: "m1", Role: "user", Content: "c", Rank: -1.0},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithCacheTTL(5*time.Second),
	)

	rc1 := engine.RetrieveContext(context.Background(), "test")
	require.NotNil(t, rc1)

	engine.ClearCache()

	// After clearing, should refetch
	rc2 := engine.RetrieveContext(context.Background(), "test")
	require.NotNil(t, rc2)
	assert.NotSame(t, rc1, rc2)
}

// --- Content truncation ---

func TestRetrieveContext_ContentTruncation(t *testing.T) {
	longContent := ""
	for i := 0; i < 300; i++ {
		longContent += "x"
	}

	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID: "msg-1",
				Role:      "user",
				Content:   longContent,
				Rank:      -1.0,
			},
		},
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "query")

	require.NotNil(t, rc)
	require.Len(t, rc.SimilarRequests, 1)
	// Should be truncated to maxContentLen + "..."
	assert.Equal(t, 200+3, len(rc.SimilarRequests[0].Content))
	assert.True(t, len(rc.SimilarRequests[0].Content) < len(longContent))
}

// --- Max results limit ---

func TestRetrieveContext_MaxResults(t *testing.T) {
	messages := make([]fts.MessageResult, 10)
	for i := range messages {
		messages[i] = fts.MessageResult{
			MessageID: fmt.Sprintf("msg-%d", i),
			Role:      "user",
			Content:   fmt.Sprintf("content %d", i),
			Rank:      float64(-10 + i),
		}
	}

	search := &mockSearchService{messages: messages}
	engine := NewContextEngine(
		WithSearchService(search),
		WithMaxResults(3),
	)

	rc := engine.RetrieveContext(context.Background(), "query")
	require.NotNil(t, rc)
	assert.LessOrEqual(t, len(rc.SimilarRequests), 3)
}

// --- Routing hints ---

func TestDeriveHints_FromAssistantResponses(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID: "msg-1",
				Role:      "assistant",
				Content:   "This is a very long response explaining the architecture in detail with many paragraphs and code examples showing how everything works together in the system",
				Rank:      -5.0,
			},
			{
				MessageID: "msg-2",
				Role:      "assistant",
				Content:   "Another detailed response with lots of explanation about the design patterns used in this codebase and how they integrate with each other for maximum flexibility",
				Rank:      -3.0,
			},
		},
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "architecture question")

	require.NotNil(t, rc)
	require.NotEmpty(t, rc.Hints)

	// Long assistant responses should suggest a higher tier
	hint := rc.Hints[0]
	assert.GreaterOrEqual(t, int(hint.SuggestedTier), int(TierSonnet))
	assert.Greater(t, hint.Confidence, 0.0)
	assert.NotEmpty(t, hint.Reason)
}

func TestDeriveHints_ShortResponses(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID: "msg-1",
				Role:      "assistant",
				Content:   "Yes, it works.",
				Rank:      -5.0,
			},
			{
				MessageID: "msg-2",
				Role:      "assistant",
				Content:   "No problem.",
				Rank:      -3.0,
			},
		},
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "simple question")

	require.NotNil(t, rc)
	require.NotEmpty(t, rc.Hints)

	// Short responses should suggest haiku
	hint := rc.Hints[0]
	assert.Equal(t, TierHaiku, hint.SuggestedTier)
}

func TestDeriveHints_NoAssistantMessages(t *testing.T) {
	search := &mockSearchService{
		messages: []fts.MessageResult{
			{
				MessageID: "msg-1",
				Role:      "user",
				Content:   "User question only",
				Rank:      -5.0,
			},
		},
	}

	engine := NewContextEngine(WithSearchService(search))
	rc := engine.RetrieveContext(context.Background(), "question")

	require.NotNil(t, rc)
	// Should have no complexity hint (no assistant messages to analyze)
	// but may have usage hints if tracker is set
	for _, hint := range rc.Hints {
		assert.NotContains(t, hint.Reason, "similar past requests produced")
	}
}

func TestDeriveHints_UsageTracker(t *testing.T) {
	tracker := NewUsageTracker()
	// Record successful usage of sonnet
	for i := 0; i < 10; i++ {
		tracker.RecordUsage("anthropic", "claude-sonnet-4-6", 1000, 500, 100)
	}
	// Record some errors for haiku
	for i := 0; i < 3; i++ {
		tracker.RecordError("anthropic", "claude-haiku-4-5")
	}
	tracker.RecordUsage("anthropic", "claude-haiku-4-5", 100, 50, 20)

	search := &mockSearchService{
		messages: []fts.MessageResult{
			{MessageID: "m1", Role: "assistant", Content: "response", Rank: -1.0},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithUsageTracker(tracker),
	)
	rc := engine.RetrieveContext(context.Background(), "query")

	require.NotNil(t, rc)

	// Should have a usage-based hint suggesting sonnet (best success rate)
	var hasUsageHint bool
	for _, hint := range rc.Hints {
		if hint.Reason != "" && hint.SuggestedTier == TierSonnet {
			hasUsageHint = true
			break
		}
	}
	assert.True(t, hasUsageHint, "expected a usage-based hint suggesting sonnet")
}

func TestDeriveHints_UsageTracker_InsufficientData(t *testing.T) {
	tracker := NewUsageTracker()
	// Only 2 requests — below the threshold of 3
	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 100)
	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 100)

	engine := NewContextEngine(WithUsageTracker(tracker))
	rc := engine.RetrieveContext(context.Background(), "query")

	require.NotNil(t, rc)
	// No usage hint should be generated with insufficient data
	for _, hint := range rc.Hints {
		assert.NotContains(t, hint.Reason, "success rate")
	}
}

// --- RoutingContext methods ---

func TestRoutingContext_IsEmpty(t *testing.T) {
	assert.True(t, (&RoutingContext{}).IsEmpty())
	assert.True(t, (&RoutingContext{Source: "none"}).IsEmpty())
	assert.False(t, (&RoutingContext{
		SimilarRequests: []SimilarRequest{{Content: "x"}},
	}).IsEmpty())
	assert.False(t, (&RoutingContext{
		Hints: []RoutingHint{{SuggestedTier: TierSonnet}},
	}).IsEmpty())
}

func TestRoutingContext_SuggestedTier(t *testing.T) {
	// No hints
	rc := &RoutingContext{}
	assert.Equal(t, ModelTier(-1), rc.SuggestedTier())

	// Single hint
	rc = &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierSonnet, Confidence: 0.7},
		},
	}
	assert.Equal(t, TierSonnet, rc.SuggestedTier())

	// Multiple hints — majority wins
	rc = &RoutingContext{
		Hints: []RoutingHint{
			{SuggestedTier: TierSonnet, Confidence: 0.7},
			{SuggestedTier: TierOpus, Confidence: 0.5},
			{SuggestedTier: TierSonnet, Confidence: 0.6},
		},
	}
	assert.Equal(t, TierSonnet, rc.SuggestedTier())
}

// --- inferTierFromModel ---

func TestInferTierFromModel(t *testing.T) {
	assert.Equal(t, TierHaiku, inferTierFromModel("claude-haiku-4-5-20251001"))
	assert.Equal(t, TierSonnet, inferTierFromModel("claude-sonnet-4-6"))
	assert.Equal(t, TierOpus, inferTierFromModel("claude-opus-4-6"))
	assert.Equal(t, TierSonnet, inferTierFromModel("unknown-model"))
}

// --- Merge deduplication ---

func TestMergeResults_Deduplication(t *testing.T) {
	engine := NewContextEngine(WithMaxResults(10))

	ftsResults := []SimilarRequest{
		{Content: "shared content", SessionKey: "s1", Source: "fts5"},
		{Content: "fts only", SessionKey: "s2", Source: "fts5"},
	}
	vectorResults := []SimilarRequest{
		{Content: "shared content", SessionKey: "s1", Source: "vector"},
		{Content: "vector only", SessionKey: "s3", Source: "vector"},
	}

	merged := engine.mergeResults(ftsResults, vectorResults)

	// "shared content" from s1 should appear only once (vector wins)
	assert.Len(t, merged, 3)
	assert.Equal(t, "vector", merged[0].Source)
	assert.Equal(t, "shared content", merged[0].Content)
}

// --- Interface compliance ---

func TestContextEngine_ImplementsInterface(t *testing.T) {
	var _ ContextEngine = (*DefaultContextEngine)(nil)
}

// --- Search timeout ---

func TestRetrieveContext_SearchTimeout(t *testing.T) {
	// Use a cancelled context to simulate timeout
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	search := &mockSearchService{
		messages: []fts.MessageResult{
			{MessageID: "m1", Role: "user", Content: "c", Rank: -1.0},
		},
	}

	engine := NewContextEngine(
		WithSearchService(search),
		WithSearchTimeout(1*time.Millisecond),
	)

	// Should still return a result (degraded) even with cancelled parent context
	rc := engine.RetrieveContext(ctx, "test query")
	require.NotNil(t, rc)
	// Results may or may not be present depending on timing, but no panic
}

// --- Cache eviction ---

func TestCacheEviction_OnWrite(t *testing.T) {
	engine := NewContextEngine(
		WithCacheTTL(1 * time.Millisecond), // Very short TTL
	)

	// Fill cache with many entries
	for i := 0; i < 150; i++ {
		key := fmt.Sprintf("query-%d", i)
		engine.putCached(key, &RoutingContext{Source: "test"})
	}

	// Wait for all entries to expire
	time.Sleep(5 * time.Millisecond)

	// Writing a new entry should trigger eviction of expired entries
	engine.putCached("new-query", &RoutingContext{Source: "new"})

	engine.cacheMu.RLock()
	cacheSize := len(engine.cache)
	engine.cacheMu.RUnlock()

	// After eviction, only the new entry (and possibly a few not-yet-expired) should remain
	assert.LessOrEqual(t, cacheSize, 10) // Most expired entries should be removed
}
