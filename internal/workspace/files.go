package workspace

import (
	"sync"
	"time"
)

// FileCache provides in-memory caching of file contents with TTL
type FileCache struct {
	cache     map[string]*cacheEntry
	mutex     sync.RWMutex
	ttl       time.Duration
	maxSizeMB int64
	currentMB int64
}

// cacheEntry represents a cached file entry
type cacheEntry struct {
	content   string
	timestamp time.Time
	sizeMB    int64
}

// NewFileCache creates a new file cache with TTL and size limits
func NewFileCache(ttl time.Duration, maxSizeBytes int64) *FileCache {
	cache := &FileCache{
		cache:     make(map[string]*cacheEntry),
		ttl:       ttl,
		maxSizeMB: maxSizeBytes / (1024 * 1024), // Convert to MB
	}

	// Start cleanup goroutine
	go cache.cleanupExpired()

	return cache
}

// Get retrieves content from cache if available and not expired
func (fc *FileCache) Get(key string) (string, bool) {
	fc.mutex.RLock()
	defer fc.mutex.RUnlock()

	entry, exists := fc.cache[key]
	if !exists {
		return "", false
	}

	// Check if expired
	if time.Since(entry.timestamp) > fc.ttl {
		// Remove expired entry (we have write lock in cleanup goroutine)
		go func() {
			fc.mutex.Lock()
			delete(fc.cache, key)
			fc.currentMB -= entry.sizeMB
			fc.mutex.Unlock()
		}()
		return "", false
	}

	return entry.content, true
}

// Set stores content in cache
func (fc *FileCache) Set(key, content string) {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	sizeMB := int64(len(content)) / (1024 * 1024)
	if sizeMB == 0 {
		sizeMB = 1 // Minimum 1MB for small files
	}

	// Check if adding this would exceed size limit
	if fc.currentMB+sizeMB > fc.maxSizeMB {
		fc.evictLRU(sizeMB)
	}

	// Remove existing entry if present
	if existing, exists := fc.cache[key]; exists {
		fc.currentMB -= existing.sizeMB
	}

	// Add new entry
	fc.cache[key] = &cacheEntry{
		content:   content,
		timestamp: time.Now(),
		sizeMB:    sizeMB,
	}
	fc.currentMB += sizeMB
}

// Delete removes a specific key from cache
func (fc *FileCache) Delete(key string) {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	if entry, exists := fc.cache[key]; exists {
		fc.currentMB -= entry.sizeMB
		delete(fc.cache, key)
	}
}

// Clear removes all entries from cache
func (fc *FileCache) Clear() {
	fc.mutex.Lock()
	defer fc.mutex.Unlock()

	fc.cache = make(map[string]*cacheEntry)
	fc.currentMB = 0
}

// evictLRU evicts least recently used entries to make space
func (fc *FileCache) evictLRU(neededMB int64) {
	// Find oldest entries
	oldestTime := time.Now()
	var oldestKeys []string

	for key, entry := range fc.cache {
		if entry.timestamp.Before(oldestTime) {
			oldestTime = entry.timestamp
			oldestKeys = []string{key}
		} else if entry.timestamp.Equal(oldestTime) {
			oldestKeys = append(oldestKeys, key)
		}
	}

	// Remove oldest entries until we have enough space
	for _, key := range oldestKeys {
		if fc.currentMB+neededMB <= fc.maxSizeMB {
			break
		}

		if entry, exists := fc.cache[key]; exists {
			fc.currentMB -= entry.sizeMB
			delete(fc.cache, key)
		}
	}
}

// cleanupExpired removes expired entries periodically
func (fc *FileCache) cleanupExpired() {
	ticker := time.NewTicker(fc.ttl / 2) // Cleanup twice per TTL period
	defer ticker.Stop()

	for range ticker.C {
		fc.mutex.Lock()

		now := time.Now()
		for key, entry := range fc.cache {
			if now.Sub(entry.timestamp) > fc.ttl {
				fc.currentMB -= entry.sizeMB
				delete(fc.cache, key)
			}
		}

		fc.mutex.Unlock()
	}
}

// Stats returns cache statistics for monitoring
func (fc *FileCache) Stats() map[string]interface{} {
	fc.mutex.RLock()
	defer fc.mutex.RUnlock()

	return map[string]interface{}{
		"entries":         len(fc.cache),
		"current_size_mb": fc.currentMB,
		"max_size_mb":     fc.maxSizeMB,
		"utilization":     float64(fc.currentMB) / float64(fc.maxSizeMB),
		"ttl_seconds":     int(fc.ttl.Seconds()),
	}
}

// FileSystemWatcher watches for file changes and invalidates cache
type FileSystemWatcher struct {
	workspaceDir string
	cache        *FileCache
	enabled      bool
	mu           sync.RWMutex
}

// NewFileSystemWatcher creates a new file system watcher
func NewFileSystemWatcher(workspaceDir string, cache *FileCache) *FileSystemWatcher {
	return &FileSystemWatcher{
		workspaceDir: workspaceDir,
		cache:        cache,
		enabled:      false, // Disabled by default - can be enabled later
	}
}

// Enable enables file watching
func (fsw *FileSystemWatcher) Enable() {
	fsw.mu.Lock()
	defer fsw.mu.Unlock()
	fsw.enabled = true
	// TODO: Implement actual file watching using fsnotify or similar
}

// Disable disables file watching
func (fsw *FileSystemWatcher) Disable() {
	fsw.mu.Lock()
	defer fsw.mu.Unlock()
	fsw.enabled = false
}

// IsEnabled returns whether file watching is enabled
func (fsw *FileSystemWatcher) IsEnabled() bool {
	fsw.mu.RLock()
	defer fsw.mu.RUnlock()
	return fsw.enabled
}
