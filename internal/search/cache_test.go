package search

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewSearchCache(t *testing.T) {
	config := CacheConfig{
		TTLMinutes: 10,
		Enabled:    true,
	}

	cache := NewSearchCache(config)

	assert.NotNil(t, cache)
	assert.Equal(t, config, cache.config)
	assert.NotNil(t, cache.entries)
}

func TestSearchCache_GetSet(t *testing.T) {
	config := CacheConfig{
		TTLMinutes: 10,
		Enabled:    true,
	}
	cache := NewSearchCache(config)

	params := SearchParameters{
		Query:   "test query",
		Count:   5,
		Country: "US",
	}

	// Test cache miss
	result, found := cache.Get(params)
	assert.False(t, found)
	assert.Nil(t, result)

	// Set cache entry
	response := &SearchResponse{
		Results: []SearchResult{
			{Title: "Test Result", URL: "https://example.com", Description: "Test description"},
		},
		Query:     "test query",
		Total:     1,
		Provider:  "brave",
		Cached:    false,
		Timestamp: time.Now(),
	}

	cache.Set(params, response)

	// Test cache hit
	result, found = cache.Get(params)
	assert.True(t, found)
	assert.NotNil(t, result)
	assert.True(t, result.Cached) // Should be marked as cached
	assert.Equal(t, response.Query, result.Query)
	assert.Equal(t, response.Total, result.Total)
	assert.Len(t, result.Results, 1)
}

func TestSearchCache_DisabledCache(t *testing.T) {
	config := CacheConfig{
		TTLMinutes: 10,
		Enabled:    false, // Disabled
	}
	cache := NewSearchCache(config)

	params := SearchParameters{Query: "test query"}
	response := &SearchResponse{
		Query: "test query",
		Total: 1,
	}

	// Setting should be ignored when disabled
	cache.Set(params, response)

	// Getting should always return cache miss when disabled
	result, found := cache.Get(params)
	assert.False(t, found)
	assert.Nil(t, result)
}

func TestSearchCache_Expiration(t *testing.T) {
	config := CacheConfig{
		TTLMinutes: 0, // Immediately expired for testing
		Enabled:    true,
	}
	cache := NewSearchCache(config)

	params := SearchParameters{Query: "test query"}
	response := &SearchResponse{
		Query: "test query",
		Total: 1,
	}

	// Set entry
	cache.Set(params, response)

	// Wait a bit to ensure expiration
	time.Sleep(10 * time.Millisecond)

	// Should be expired and return cache miss
	result, found := cache.Get(params)
	assert.False(t, found)
	assert.Nil(t, result)
}

func TestSearchCache_KeyGeneration(t *testing.T) {
	config := CacheConfig{TTLMinutes: 10, Enabled: true}
	cache := NewSearchCache(config)

	// Same parameters should generate same key
	params1 := SearchParameters{Query: "test", Count: 5, Country: "US"}
	params2 := SearchParameters{Query: "test", Count: 5, Country: "US"}

	key1 := cache.generateKey(params1)
	key2 := cache.generateKey(params2)

	assert.Equal(t, key1, key2)
	assert.NotEmpty(t, key1)

	// Different parameters should generate different keys
	params3 := SearchParameters{Query: "different", Count: 5, Country: "US"}
	key3 := cache.generateKey(params3)

	assert.NotEqual(t, key1, key3)

	// Order of setting parameters shouldn't matter for same logical parameters
	params4 := SearchParameters{Country: "US", Query: "test", Count: 5}
	key4 := cache.generateKey(params4)

	assert.Equal(t, key1, key4)
}

func TestSearchCache_Clear(t *testing.T) {
	config := CacheConfig{TTLMinutes: 10, Enabled: true}
	cache := NewSearchCache(config)

	// Add some entries
	params1 := SearchParameters{Query: "query1"}
	params2 := SearchParameters{Query: "query2"}
	response := &SearchResponse{Total: 1}

	cache.Set(params1, response)
	cache.Set(params2, response)

	// Verify entries exist
	_, found1 := cache.Get(params1)
	_, found2 := cache.Get(params2)
	assert.True(t, found1)
	assert.True(t, found2)

	// Clear cache
	cache.Clear()

	// Verify entries are gone
	_, found1 = cache.Get(params1)
	_, found2 = cache.Get(params2)
	assert.False(t, found1)
	assert.False(t, found2)
}

func TestSearchCache_Stats(t *testing.T) {
	config := CacheConfig{TTLMinutes: 10, Enabled: true}
	cache := NewSearchCache(config)

	// Initial stats
	stats := cache.Stats()
	assert.Equal(t, true, stats["enabled"])
	assert.Equal(t, 10, stats["ttl_minutes"])
	assert.Equal(t, 0, stats["total_entries"])
	assert.Equal(t, 0, stats["active"])
	assert.Equal(t, 0, stats["expired"])

	// Add some entries
	params1 := SearchParameters{Query: "query1"}
	params2 := SearchParameters{Query: "query2"}
	response := &SearchResponse{Total: 1}

	cache.Set(params1, response)
	cache.Set(params2, response)

	// Check updated stats
	stats = cache.Stats()
	assert.Equal(t, 2, stats["total_entries"])
	assert.Equal(t, 2, stats["active"])
	assert.Equal(t, 0, stats["expired"])
}

func TestCacheEntry_IsExpired(t *testing.T) {
	// Test non-expired entry
	entry := &CacheEntry{
		Response:  &SearchResponse{},
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	assert.False(t, entry.IsExpired())

	// Test expired entry
	entry.ExpiresAt = time.Now().Add(-10 * time.Minute)
	assert.True(t, entry.IsExpired())
}

func TestSearchCache_ConcurrentAccess(t *testing.T) {
	config := CacheConfig{TTLMinutes: 10, Enabled: true}
	cache := NewSearchCache(config)

	params := SearchParameters{Query: "concurrent test"}
	response := &SearchResponse{Query: "concurrent test", Total: 1}

	// Start multiple goroutines to test concurrent access
	done := make(chan bool, 20)

	// Writers
	for i := 0; i < 10; i++ {
		go func(i int) {
			testParams := SearchParameters{Query: fmt.Sprintf("query%d", i)}
			cache.Set(testParams, response)
			done <- true
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		go func(i int) {
			testParams := SearchParameters{Query: fmt.Sprintf("query%d", i)}
			cache.Get(testParams)
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < 20; i++ {
		<-done
	}

	// Test that cache still works after concurrent access
	cache.Set(params, response)
	result, found := cache.Get(params)
	assert.True(t, found)
	assert.NotNil(t, result)
}
