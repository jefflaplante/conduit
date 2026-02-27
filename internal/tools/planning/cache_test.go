package planning

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestResultCache_BasicOperations(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10) // 10MB limit

	ctx := context.Background()

	// Test cache miss
	result, found := cache.Get(ctx, "nonexistent_key")
	if found {
		t.Error("Expected cache miss for nonexistent key")
	}
	if result != nil {
		t.Error("Result should be nil for cache miss")
	}

	// Test cache set and get
	stepResult := &StepResult{
		StepID:     "test_step",
		ToolName:   "web_search",
		Success:    true,
		Content:    "Test search results",
		Duration:   time.Second,
		ExecutedAt: time.Now(),
	}

	err := cache.Set(ctx, "test_key", "web_search", map[string]interface{}{"query": "test"}, stepResult)
	if err != nil {
		t.Fatalf("Cache set failed: %v", err)
	}

	// Test cache hit
	cachedResult, found := cache.Get(ctx, "test_key")
	if !found {
		t.Error("Expected cache hit for existing key")
	}
	if cachedResult == nil {
		t.Error("Cached result should not be nil")
	}
	if cachedResult.Content != stepResult.Content {
		t.Errorf("Cached content mismatch: expected %s, got %s", stepResult.Content, cachedResult.Content)
	}
	if !cachedResult.CacheHit {
		t.Error("Cache hit flag should be set")
	}
}

func TestResultCache_TTLExpiration(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	ctx := context.Background()

	// Create a short-lived cache policy for testing
	testPolicy := &TestCachePolicy{
		toolName: "test_tool",
		ttl:      time.Millisecond * 50, // Very short TTL
	}
	cache.policies = []CachePolicy{testPolicy}

	stepResult := &StepResult{
		StepID:   "expiring_step",
		ToolName: "test_tool",
		Success:  true,
		Content:  "Expiring content",
	}

	// Set cache entry
	err := cache.Set(ctx, "expiring_key", "test_tool", map[string]interface{}{}, stepResult)
	if err != nil {
		t.Fatalf("Cache set failed: %v", err)
	}

	// Should be cached immediately
	result, found := cache.Get(ctx, "expiring_key")
	if !found {
		t.Error("Expected cache hit before expiration")
	}
	if result == nil {
		t.Error("Result should not be nil")
	}

	// Wait for expiration
	time.Sleep(time.Millisecond * 100)

	// Should be expired now
	expiredResult, found := cache.Get(ctx, "expiring_key")
	if found {
		t.Error("Expected cache miss after expiration")
	}
	if expiredResult != nil {
		t.Error("Expired result should be nil")
	}
}

func TestResultCache_Invalidation(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	ctx := context.Background()

	// Set multiple cache entries
	stepResult1 := &StepResult{
		StepID:   "step1",
		ToolName: "web_search",
		Success:  true,
		Content:  "Search result 1",
	}

	stepResult2 := &StepResult{
		StepID:   "step2",
		ToolName: "memory_search",
		Success:  true,
		Content:  "Memory result 1",
	}

	cache.Set(ctx, "key1", "web_search", map[string]interface{}{}, stepResult1)
	cache.Set(ctx, "key2", "memory_search", map[string]interface{}{}, stepResult2)

	// Verify both are cached
	if _, found := cache.Get(ctx, "key1"); !found {
		t.Error("key1 should be cached")
	}
	if _, found := cache.Get(ctx, "key2"); !found {
		t.Error("key2 should be cached")
	}

	// Invalidate web_search entries
	err := cache.Invalidate(ctx, "web_search")
	if err != nil {
		t.Fatalf("Invalidation failed: %v", err)
	}

	// key1 should be invalidated, key2 should remain
	if _, found := cache.Get(ctx, "key1"); found {
		t.Error("key1 should be invalidated")
	}
	if _, found := cache.Get(ctx, "key2"); !found {
		t.Error("key2 should still be cached")
	}
}

func TestResultCache_ClearAll(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	ctx := context.Background()

	// Set multiple entries (use web_search which has a default policy)
	for i := 0; i < 5; i++ {
		stepResult := &StepResult{
			StepID:   fmt.Sprintf("step_%d", i),
			ToolName: "web_search",
			Success:  true,
			Content:  fmt.Sprintf("Content %d", i),
		}
		cache.Set(ctx, fmt.Sprintf("key_%d", i), "web_search", map[string]interface{}{"query": fmt.Sprintf("test_%d", i)}, stepResult)
	}

	// Verify entries exist
	for i := 0; i < 5; i++ {
		if _, found := cache.Get(ctx, fmt.Sprintf("key_%d", i)); !found {
			t.Errorf("key_%d should be cached", i)
		}
	}

	// Clear cache
	err := cache.Clear(ctx)
	if err != nil {
		t.Fatalf("Clear cache failed: %v", err)
	}

	// Verify all entries are gone
	for i := 0; i < 5; i++ {
		if _, found := cache.Get(ctx, fmt.Sprintf("key_%d", i)); found {
			t.Errorf("key_%d should be cleared", i)
		}
	}
}

func TestResultCache_Policies(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	ctx := context.Background()

	// Test web search caching policy
	webSearchResult := &StepResult{
		StepID:   "web_search_step",
		ToolName: "web_search",
		Success:  true,
		Content:  "Search results",
	}

	err := cache.Set(ctx, "web_search_key", "web_search",
		map[string]interface{}{"query": "test"}, webSearchResult)
	if err != nil {
		t.Fatalf("Web search cache set failed: %v", err)
	}

	// Should be cached
	if _, found := cache.Get(ctx, "web_search_key"); !found {
		t.Error("Web search result should be cached")
	}

	// Test exec command (should not be cached by default)
	execResult := &StepResult{
		StepID:   "exec_step",
		ToolName: "exec",
		Success:  true,
		Content:  "Command output",
	}

	err = cache.Set(ctx, "exec_key", "exec",
		map[string]interface{}{"command": "ls"}, execResult)
	if err != nil {
		t.Fatalf("Exec cache set should not fail: %v", err)
	}

	// Should not be cached (exec is not cacheable by default)
	if found := cache.HasCached("exec_key"); found {
		t.Error("Exec command should not be cached")
	}
}

func TestResultCache_KeyGeneration(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	// Test key generation for same tool with same args
	key1 := cache.GenerateKey("web_search", map[string]interface{}{
		"query": "test query",
		"count": 10,
	})

	key2 := cache.GenerateKey("web_search", map[string]interface{}{
		"query": "test query",
		"count": 10,
	})

	if key1 != key2 {
		t.Error("Same tool calls should generate same cache key")
	}

	// Test key generation for different args
	key3 := cache.GenerateKey("web_search", map[string]interface{}{
		"query": "different query",
		"count": 10,
	})

	if key1 == key3 {
		t.Error("Different tool calls should generate different cache keys")
	}

	// Test key generation for different tools
	key4 := cache.GenerateKey("memory_search", map[string]interface{}{
		"query": "test query",
	})

	if key1 == key4 {
		t.Error("Different tools should generate different cache keys")
	}
}

func TestResultCache_Metrics(t *testing.T) {
	storage := NewMemoryStorage()
	cache := NewResultCache(storage, 10)

	ctx := context.Background()

	stepResult := &StepResult{
		StepID:   "metrics_step",
		ToolName: "web_search",
		Success:  true,
		Content:  "Metrics test",
	}

	// Initial metrics
	metrics := cache.GetMetrics()
	initialHits := metrics.Hits
	initialMisses := metrics.Misses

	// Cache miss
	cache.Get(ctx, "nonexistent")
	metrics = cache.GetMetrics()
	if metrics.Misses != initialMisses+1 {
		t.Errorf("Expected %d misses, got %d", initialMisses+1, metrics.Misses)
	}

	// Cache set
	cache.Set(ctx, "metrics_key", "web_search", map[string]interface{}{}, stepResult)

	// Cache hit
	cache.Get(ctx, "metrics_key")
	metrics = cache.GetMetrics()
	if metrics.Hits != initialHits+1 {
		t.Errorf("Expected %d hits, got %d", initialHits+1, metrics.Hits)
	}

	// Check entry count
	if metrics.EntryCount <= 0 {
		t.Error("Entry count should be positive")
	}
}

// TestCachePolicy is a test implementation of CachePolicy
type TestCachePolicy struct {
	toolName string
	ttl      time.Duration
}

func (t *TestCachePolicy) ShouldCache(toolName string, args map[string]interface{}, result *StepResult) bool {
	return toolName == t.toolName && result.Success
}

func (t *TestCachePolicy) TTL(toolName string, args map[string]interface{}) time.Duration {
	if toolName == t.toolName {
		return t.ttl
	}
	return time.Hour // Default
}

func (t *TestCachePolicy) InvalidateOn(toolName string) []string {
	return []string{}
}

func (t *TestCachePolicy) GenerateKey(toolName string, args map[string]interface{}) string {
	if toolName == t.toolName {
		return fmt.Sprintf("%s_%v", toolName, args)
	}
	return ""
}

func (t *TestCachePolicy) Priority(toolName string, args map[string]interface{}) int {
	return 5
}
