package planning

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// ResultCache provides intelligent caching for tool execution results
type ResultCache struct {
	storage     CacheStorage
	policies    []CachePolicy
	metrics     *CacheMetrics
	maxSize     int64
	currentSize int64
	mu          sync.RWMutex
}

// CacheStorage defines the interface for cache storage backends
type CacheStorage interface {
	Get(ctx context.Context, key string) (*CacheEntry, error)
	Set(ctx context.Context, key string, entry *CacheEntry) error
	Delete(ctx context.Context, key string) error
	Clear(ctx context.Context) error
	Keys(ctx context.Context) ([]string, error)
	Size(ctx context.Context) (int64, error)
}

// CacheEntry represents a cached tool result
type CacheEntry struct {
	Key        string                 `json:"key"`
	ToolName   string                 `json:"tool_name"`
	Args       map[string]interface{} `json:"args"`
	Result     *StepResult            `json:"result"`
	CreatedAt  time.Time              `json:"created_at"`
	AccessedAt time.Time              `json:"accessed_at"`
	ExpiresAt  time.Time              `json:"expires_at"`
	TTL        time.Duration          `json:"ttl"`
	HitCount   int                    `json:"hit_count"`
	Size       int64                  `json:"size"`
	Tags       []string               `json:"tags,omitempty"`
}

// CachePolicy defines when and how to cache tool results
type CachePolicy interface {
	ShouldCache(toolName string, args map[string]interface{}, result *StepResult) bool
	TTL(toolName string, args map[string]interface{}) time.Duration
	InvalidateOn(toolName string) []string
	GenerateKey(toolName string, args map[string]interface{}) string
	Priority(toolName string, args map[string]interface{}) int
}

// CacheMetrics tracks cache performance
type CacheMetrics struct {
	Hits          int64     `json:"hits"`
	Misses        int64     `json:"misses"`
	Evictions     int64     `json:"evictions"`
	Invalidations int64     `json:"invalidations"`
	TotalSize     int64     `json:"total_size"`
	EntryCount    int       `json:"entry_count"`
	StartTime     time.Time `json:"start_time"`
	mu            sync.RWMutex
}

// NewResultCache creates a new result cache with policies
func NewResultCache(storage CacheStorage, maxSizeMB int) *ResultCache {
	cache := &ResultCache{
		storage:     storage,
		policies:    []CachePolicy{},
		metrics:     &CacheMetrics{StartTime: time.Now()},
		maxSize:     int64(maxSizeMB) * 1024 * 1024, // Convert MB to bytes
		currentSize: 0,
	}

	// Initialize with default policies
	cache.initializeDefaultPolicies()

	// Start cleanup goroutine
	go cache.startCleanupRoutine()

	return cache
}

// Get retrieves a cached result
func (rc *ResultCache) Get(ctx context.Context, key string) (*StepResult, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	entry, err := rc.storage.Get(ctx, key)
	if err != nil || entry == nil {
		rc.recordMiss()
		return nil, false
	}

	// Check expiration
	if entry.ExpiresAt.Before(time.Now()) {
		// Expired entry
		go rc.storage.Delete(ctx, key) // Async cleanup
		rc.recordMiss()
		return nil, false
	}

	// Update access time and hit count
	entry.AccessedAt = time.Now()
	entry.HitCount++
	go rc.storage.Set(ctx, key, entry) // Async update

	rc.recordHit()
	log.Printf("Cache hit for key: %s (tool: %s, hits: %d)", key, entry.ToolName, entry.HitCount)

	// Mark the result as a cache hit
	result := entry.Result
	result.CacheHit = true

	return result, true
}

// Set stores a result in cache
func (rc *ResultCache) Set(ctx context.Context, key string, toolName string, args map[string]interface{}, result *StepResult) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Check if we should cache this result
	shouldCache := false
	var ttl time.Duration

	for _, policy := range rc.policies {
		if policy.ShouldCache(toolName, args, result) {
			shouldCache = true
			ttl = policy.TTL(toolName, args)
			break
		}
	}

	if !shouldCache {
		return nil // Not caching this result
	}

	// Create cache entry
	now := time.Now()
	entry := &CacheEntry{
		Key:        key,
		ToolName:   toolName,
		Args:       args,
		Result:     result,
		CreatedAt:  now,
		AccessedAt: now,
		ExpiresAt:  now.Add(ttl),
		TTL:        ttl,
		HitCount:   0,
		Size:       rc.calculateEntrySize(result),
		Tags:       rc.generateTags(toolName, args),
	}

	// Check size limits and evict if necessary
	if err := rc.ensureSpace(entry.Size); err != nil {
		return fmt.Errorf("failed to make space for cache entry: %w", err)
	}

	// Store entry
	if err := rc.storage.Set(ctx, key, entry); err != nil {
		return fmt.Errorf("failed to store cache entry: %w", err)
	}

	rc.currentSize += entry.Size
	rc.metrics.EntryCount++

	log.Printf("Cached result for tool %s (key: %s, ttl: %v, size: %d bytes)",
		toolName, key, ttl, entry.Size)

	return nil
}

// HasCached checks if a key exists in cache without retrieving it
func (rc *ResultCache) HasCached(key string) bool {
	ctx := context.Background()
	entry, err := rc.storage.Get(ctx, key)
	if err != nil || entry == nil {
		return false
	}

	// Check if expired
	return entry.ExpiresAt.After(time.Now())
}

// GenerateKey generates a cache key for tool execution
func (rc *ResultCache) GenerateKey(toolName string, args map[string]interface{}) string {
	// Try policy-specific key generation first
	for _, policy := range rc.policies {
		if key := policy.GenerateKey(toolName, args); key != "" {
			return key
		}
	}

	// Default key generation
	return rc.defaultGenerateKey(toolName, args)
}

// Invalidate removes entries based on tags or patterns
func (rc *ResultCache) Invalidate(ctx context.Context, pattern string) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	keys, err := rc.storage.Keys(ctx)
	if err != nil {
		return err
	}

	invalidated := 0
	for _, key := range keys {
		entry, err := rc.storage.Get(ctx, key)
		if err != nil || entry == nil {
			continue
		}

		// Check if this entry should be invalidated
		shouldInvalidate := false

		// Check tags
		for _, tag := range entry.Tags {
			if tag == pattern {
				shouldInvalidate = true
				break
			}
		}

		// Check tool name
		if entry.ToolName == pattern {
			shouldInvalidate = true
		}

		if shouldInvalidate {
			rc.storage.Delete(ctx, key)
			rc.currentSize -= entry.Size
			rc.metrics.EntryCount--
			invalidated++
		}
	}

	rc.recordInvalidations(int64(invalidated))
	log.Printf("Invalidated %d cache entries matching pattern: %s", invalidated, pattern)

	return nil
}

// Clear removes all cache entries
func (rc *ResultCache) Clear(ctx context.Context) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if err := rc.storage.Clear(ctx); err != nil {
		return err
	}

	rc.currentSize = 0
	rc.metrics.EntryCount = 0

	log.Printf("Cache cleared")
	return nil
}

// GetMetrics returns cache performance metrics
func (rc *ResultCache) GetMetrics() *CacheMetrics {
	rc.metrics.mu.RLock()
	defer rc.metrics.mu.RUnlock()

	// Create copy to avoid race conditions
	metrics := &CacheMetrics{
		Hits:          rc.metrics.Hits,
		Misses:        rc.metrics.Misses,
		Evictions:     rc.metrics.Evictions,
		Invalidations: rc.metrics.Invalidations,
		TotalSize:     rc.currentSize,
		EntryCount:    rc.metrics.EntryCount,
		StartTime:     rc.metrics.StartTime,
	}

	return metrics
}

// Helper functions

func (rc *ResultCache) recordHit() {
	rc.metrics.mu.Lock()
	rc.metrics.Hits++
	rc.metrics.mu.Unlock()
}

func (rc *ResultCache) recordMiss() {
	rc.metrics.mu.Lock()
	rc.metrics.Misses++
	rc.metrics.mu.Unlock()
}

func (rc *ResultCache) recordEvictions(count int64) {
	rc.metrics.mu.Lock()
	rc.metrics.Evictions += count
	rc.metrics.mu.Unlock()
}

func (rc *ResultCache) recordInvalidations(count int64) {
	rc.metrics.mu.Lock()
	rc.metrics.Invalidations += count
	rc.metrics.mu.Unlock()
}

func (rc *ResultCache) calculateEntrySize(result *StepResult) int64 {
	// Estimate size based on content length
	size := int64(len(result.Content))

	// Add size of data fields
	if result.Data != nil {
		if data, err := json.Marshal(result.Data); err == nil {
			size += int64(len(data))
		}
	}

	// Add overhead for metadata
	size += 512 // Estimated overhead

	return size
}

func (rc *ResultCache) generateTags(toolName string, args map[string]interface{}) []string {
	tags := []string{toolName}

	// Add domain-specific tags
	switch toolName {
	case "web_search", "web_fetch":
		if url, ok := args["url"].(string); ok && len(url) > 0 {
			// Extract domain from URL
			if domain := rc.extractDomain(url); domain != "" {
				tags = append(tags, "domain:"+domain)
			}
		}
		tags = append(tags, "network")

	case "memory_search":
		tags = append(tags, "memory")

	case "read_file", "write_file", "list_files":
		tags = append(tags, "filesystem")
	}

	return tags
}

func (rc *ResultCache) extractDomain(url string) string {
	// Simple domain extraction
	if len(url) < 8 {
		return ""
	}

	start := 0
	if url[:7] == "http://" {
		start = 7
	} else if url[:8] == "https://" {
		start = 8
	}

	end := start
	for i := start; i < len(url); i++ {
		if url[i] == '/' || url[i] == '?' {
			break
		}
		end = i + 1
	}

	if end > start {
		return url[start:end]
	}

	return ""
}

func (rc *ResultCache) defaultGenerateKey(toolName string, args map[string]interface{}) string {
	// Create deterministic hash from tool name and args
	data := map[string]interface{}{
		"tool": toolName,
		"args": args,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		// Fallback to simple string concatenation
		return fmt.Sprintf("%s_%v", toolName, args)
	}

	hash := md5.Sum(jsonBytes)
	return fmt.Sprintf("%s_%x", toolName, hash)
}

func (rc *ResultCache) ensureSpace(requiredSize int64) error {
	if rc.currentSize+requiredSize <= rc.maxSize {
		return nil // Sufficient space available
	}

	// Need to evict entries
	ctx := context.Background()
	keys, err := rc.storage.Keys(ctx)
	if err != nil {
		return err
	}

	// Sort entries by eviction priority (LRU + size + expiration)
	type evictionCandidate struct {
		key      string
		entry    *CacheEntry
		priority float64
	}

	var candidates []evictionCandidate

	for _, key := range keys {
		entry, err := rc.storage.Get(ctx, key)
		if err != nil || entry == nil {
			continue
		}

		// Calculate eviction priority (higher = evict first)
		priority := rc.calculateEvictionPriority(entry)
		candidates = append(candidates, evictionCandidate{
			key:      key,
			entry:    entry,
			priority: priority,
		})
	}

	// Sort by priority (highest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority > candidates[j].priority
	})

	// Evict entries until we have enough space
	spaceFreed := int64(0)
	evicted := 0

	for _, candidate := range candidates {
		if spaceFreed >= requiredSize {
			break
		}

		rc.storage.Delete(ctx, candidate.key)
		spaceFreed += candidate.entry.Size
		rc.currentSize -= candidate.entry.Size
		rc.metrics.EntryCount--
		evicted++
	}

	rc.recordEvictions(int64(evicted))
	log.Printf("Evicted %d cache entries to free %d bytes", evicted, spaceFreed)

	return nil
}

func (rc *ResultCache) calculateEvictionPriority(entry *CacheEntry) float64 {
	now := time.Now()

	// Time since last access (higher = evict first)
	timeSinceAccess := now.Sub(entry.AccessedAt).Seconds()

	// Time until expiration (lower = evict first)
	timeToExpiration := entry.ExpiresAt.Sub(now).Seconds()

	// Hit count (lower = evict first)
	hitCount := float64(entry.HitCount)

	// Size (higher = evict first for same access patterns)
	size := float64(entry.Size)

	// Combined priority score
	priority := timeSinceAccess*0.4 + (1.0/(timeToExpiration+1))*0.3 +
		(1.0/(hitCount+1))*0.2 + (size/1000000)*0.1

	return priority
}

func (rc *ResultCache) startCleanupRoutine() {
	ticker := time.NewTicker(5 * time.Minute) // Clean every 5 minutes
	defer ticker.Stop()

	for range ticker.C {
		ctx := context.Background()
		rc.cleanupExpiredEntries(ctx)
	}
}

func (rc *ResultCache) cleanupExpiredEntries(ctx context.Context) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	keys, err := rc.storage.Keys(ctx)
	if err != nil {
		log.Printf("Cache cleanup failed: %v", err)
		return
	}

	now := time.Now()
	cleaned := 0

	for _, key := range keys {
		entry, err := rc.storage.Get(ctx, key)
		if err != nil || entry == nil {
			continue
		}

		if entry.ExpiresAt.Before(now) {
			rc.storage.Delete(ctx, key)
			rc.currentSize -= entry.Size
			rc.metrics.EntryCount--
			cleaned++
		}
	}

	if cleaned > 0 {
		log.Printf("Cleaned up %d expired cache entries", cleaned)
	}
}

// initializeDefaultPolicies sets up default caching policies
func (rc *ResultCache) initializeDefaultPolicies() {
	// Web search caching policy
	rc.policies = append(rc.policies, &WebSearchCachePolicy{})

	// Web fetch caching policy
	rc.policies = append(rc.policies, &WebFetchCachePolicy{})

	// Memory search caching policy
	rc.policies = append(rc.policies, &MemorySearchCachePolicy{})

	log.Printf("Initialized result cache with %d policies", len(rc.policies))
}

// Default cache policy implementations

// WebSearchCachePolicy caches web search results
type WebSearchCachePolicy struct{}

func (w *WebSearchCachePolicy) ShouldCache(toolName string, args map[string]interface{}, result *StepResult) bool {
	if toolName != "web_search" {
		return false
	}

	// Cache successful searches with results
	return result.Success && result.Content != ""
}

func (w *WebSearchCachePolicy) TTL(toolName string, args map[string]interface{}) time.Duration {
	// Fresh results for 1 hour, older queries for longer
	if freshness, ok := args["freshness"].(string); ok && freshness != "" {
		return time.Minute * 30 // Shorter TTL for fresh searches
	}
	return time.Hour // Standard TTL
}

func (w *WebSearchCachePolicy) InvalidateOn(toolName string) []string {
	return []string{} // Web search results rarely need invalidation
}

func (w *WebSearchCachePolicy) GenerateKey(toolName string, args map[string]interface{}) string {
	if toolName != "web_search" {
		return ""
	}

	// Create key from query and important parameters
	key := fmt.Sprintf("web_search_%v", args["query"])
	if count, ok := args["count"]; ok {
		key += fmt.Sprintf("_c%v", count)
	}
	if freshness, ok := args["freshness"]; ok {
		key += fmt.Sprintf("_f%v", freshness)
	}

	return key
}

func (w *WebSearchCachePolicy) Priority(toolName string, args map[string]interface{}) int {
	return 5 // Medium priority
}

// WebFetchCachePolicy caches web page content
type WebFetchCachePolicy struct{}

func (w *WebFetchCachePolicy) ShouldCache(toolName string, args map[string]interface{}, result *StepResult) bool {
	if toolName != "web_fetch" {
		return false
	}

	// Cache successful fetches of reasonable size
	return result.Success && len(result.Content) > 100 && len(result.Content) < 500000
}

func (w *WebFetchCachePolicy) TTL(toolName string, args map[string]interface{}) time.Duration {
	// Web content changes, but not too frequently
	return time.Minute * 30
}

func (w *WebFetchCachePolicy) InvalidateOn(toolName string) []string {
	return []string{}
}

func (w *WebFetchCachePolicy) GenerateKey(toolName string, args map[string]interface{}) string {
	if toolName != "web_fetch" {
		return ""
	}

	// Key based on URL and extraction mode
	key := fmt.Sprintf("web_fetch_%v", args["url"])
	if mode, ok := args["extractMode"]; ok {
		key += fmt.Sprintf("_%v", mode)
	}

	return key
}

func (w *WebFetchCachePolicy) Priority(toolName string, args map[string]interface{}) int {
	return 7 // Higher priority for expensive fetches
}

// MemorySearchCachePolicy caches memory search results
type MemorySearchCachePolicy struct{}

func (m *MemorySearchCachePolicy) ShouldCache(toolName string, args map[string]interface{}, result *StepResult) bool {
	if toolName != "memory_search" {
		return false
	}

	// Always cache successful memory searches
	return result.Success
}

func (m *MemorySearchCachePolicy) TTL(toolName string, args map[string]interface{}) time.Duration {
	// Memory content doesn't change frequently
	return time.Minute * 10
}

func (m *MemorySearchCachePolicy) InvalidateOn(toolName string) []string {
	// Invalidate when files change
	return []string{"write_file", "exec"}
}

func (m *MemorySearchCachePolicy) GenerateKey(toolName string, args map[string]interface{}) string {
	if toolName != "memory_search" {
		return ""
	}

	return fmt.Sprintf("memory_search_%v", args["query"])
}

func (m *MemorySearchCachePolicy) Priority(toolName string, args map[string]interface{}) int {
	return 3 // Lower priority for cheap operations
}

// AddPolicy adds a custom cache policy
func (rc *ResultCache) AddPolicy(policy CachePolicy) {
	rc.policies = append(rc.policies, policy)
	log.Printf("Added custom cache policy")
}
