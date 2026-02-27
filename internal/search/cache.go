package search

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// CacheConfig configures the search result cache
type CacheConfig struct {
	TTLMinutes int  `json:"ttl_minutes"`
	Enabled    bool `json:"enabled"`
}

// DefaultCacheConfig returns the default cache configuration
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		TTLMinutes: 15, // 15 minutes like TS Conduit
		Enabled:    true,
	}
}

// CacheEntry represents a cached search result
type CacheEntry struct {
	Response  *SearchResponse `json:"response"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// IsExpired checks if the cache entry is expired
func (e *CacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// SearchCache provides in-memory caching for search results
type SearchCache struct {
	config  CacheConfig
	entries map[string]*CacheEntry
	mutex   sync.RWMutex
}

// NewSearchCache creates a new search cache
func NewSearchCache(config CacheConfig) *SearchCache {
	cache := &SearchCache{
		config:  config,
		entries: make(map[string]*CacheEntry),
	}

	// Start cleanup goroutine
	go cache.startCleanup()

	return cache
}

// Get retrieves a cached search result
func (c *SearchCache) Get(params SearchParameters) (*SearchResponse, bool) {
	if !c.config.Enabled {
		return nil, false
	}

	key := c.generateKey(params)

	c.mutex.RLock()
	entry, exists := c.entries[key]
	c.mutex.RUnlock()

	if !exists || entry.IsExpired() {
		if exists {
			// Remove expired entry
			c.mutex.Lock()
			delete(c.entries, key)
			c.mutex.Unlock()
		}
		return nil, false
	}

	// Mark as cached and return
	response := *entry.Response // Copy the response
	response.Cached = true
	return &response, true
}

// Set stores a search result in the cache
func (c *SearchCache) Set(params SearchParameters, response *SearchResponse) {
	if !c.config.Enabled {
		return
	}

	key := c.generateKey(params)
	entry := &CacheEntry{
		Response:  response,
		ExpiresAt: time.Now().Add(time.Duration(c.config.TTLMinutes) * time.Minute),
	}

	c.mutex.Lock()
	c.entries[key] = entry
	c.mutex.Unlock()
}

// Clear removes all entries from the cache
func (c *SearchCache) Clear() {
	c.mutex.Lock()
	c.entries = make(map[string]*CacheEntry)
	c.mutex.Unlock()
}

// Stats returns cache statistics
func (c *SearchCache) Stats() map[string]interface{} {
	c.mutex.RLock()
	totalEntries := len(c.entries)

	expiredCount := 0
	for _, entry := range c.entries {
		if entry.IsExpired() {
			expiredCount++
		}
	}
	c.mutex.RUnlock()

	return map[string]interface{}{
		"enabled":       c.config.Enabled,
		"ttl_minutes":   c.config.TTLMinutes,
		"total_entries": totalEntries,
		"expired":       expiredCount,
		"active":        totalEntries - expiredCount,
	}
}

// generateKey creates a cache key from search parameters
func (c *SearchCache) generateKey(params SearchParameters) string {
	// Create a normalized representation of the parameters
	normalized := struct {
		Query      string `json:"query"`
		Count      int    `json:"count"`
		Country    string `json:"country"`
		SearchLang string `json:"search_lang"`
		UILang     string `json:"ui_lang"`
		Freshness  string `json:"freshness"`
	}{
		Query:      params.Query,
		Count:      params.Count,
		Country:    params.Country,
		SearchLang: params.SearchLang,
		UILang:     params.UILang,
		Freshness:  params.Freshness,
	}

	// Convert to JSON for consistent key generation
	jsonBytes, _ := json.Marshal(normalized)

	// Generate SHA256 hash
	hash := sha256.Sum256(jsonBytes)
	return hex.EncodeToString(hash[:])
}

// startCleanup starts a background goroutine to clean up expired entries
func (c *SearchCache) startCleanup() {
	ticker := time.NewTicker(5 * time.Minute) // Cleanup every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		c.cleanupExpired()
	}
}

// cleanupExpired removes expired entries from the cache
func (c *SearchCache) cleanupExpired() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	for key, entry := range c.entries {
		if entry.IsExpired() {
			delete(c.entries, key)
		}
	}
}
