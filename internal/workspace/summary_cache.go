package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SummaryCache manages cached summaries with in-memory and disk persistence
type SummaryCache struct {
	cache    map[string]*SummaryEntry // key: "filename:hash"
	cacheDir string
	ttl      time.Duration
	mu       sync.RWMutex
}

// NewSummaryCache creates a new summary cache
func NewSummaryCache(workspaceDir string, config SummaryConfig) *SummaryCache {
	cacheDir := config.CacheDir
	if cacheDir == "" {
		cacheDir = ".summaries"
	}
	if !filepath.IsAbs(cacheDir) {
		cacheDir = filepath.Join(workspaceDir, cacheDir)
	}

	sc := &SummaryCache{
		cache:    make(map[string]*SummaryEntry),
		cacheDir: cacheDir,
		ttl:      config.GetCacheTTL(),
	}

	// Load persisted cache on startup
	sc.loadFromDisk()

	// Start cleanup goroutine
	go sc.cleanupExpired()

	return sc
}

// Get retrieves a cached summary if valid
func (sc *SummaryCache) Get(filename, hash string) (*SummaryEntry, bool) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	key := sc.makeKey(filename, hash)
	entry, exists := sc.cache[key]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Since(entry.CreatedAt) > sc.ttl {
		// Schedule removal (don't block)
		go func() {
			sc.mu.Lock()
			delete(sc.cache, key)
			sc.mu.Unlock()
		}()
		return nil, false
	}

	// Verify hash matches (paranoid check)
	if entry.SourceHash != hash {
		return nil, false
	}

	return entry, true
}

// Set stores a summary in cache and persists to disk
func (sc *SummaryCache) Set(filename, hash string, entry *SummaryEntry) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	key := sc.makeKey(filename, hash)
	sc.cache[key] = entry

	// Persist to disk asynchronously
	go sc.persistEntry(filename, entry)
}

// makeKey creates a cache key from filename and hash
func (sc *SummaryCache) makeKey(filename, hash string) string {
	return filename + ":" + hash
}

// persistEntry writes a summary entry to disk
func (sc *SummaryCache) persistEntry(filename string, entry *SummaryEntry) {
	if sc.cacheDir == "" {
		return
	}

	// Ensure cache directory exists
	if err := os.MkdirAll(sc.cacheDir, 0755); err != nil {
		return
	}

	// Use safe filename (replace / with _)
	safeFilename := strings.ReplaceAll(filename, "/", "_")
	safeFilename = strings.ReplaceAll(safeFilename, "\\", "_")
	path := filepath.Join(sc.cacheDir, safeFilename+".json")

	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return
	}

	_ = os.WriteFile(path, data, 0644)
}

// loadFromDisk loads persisted summaries into memory
func (sc *SummaryCache) loadFromDisk() {
	if sc.cacheDir == "" {
		return
	}

	entries, err := os.ReadDir(sc.cacheDir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		path := filepath.Join(sc.cacheDir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var entry SummaryEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}

		// Skip expired entries
		if time.Since(entry.CreatedAt) > sc.ttl {
			os.Remove(path)
			continue
		}

		// Reconstruct filename from safe filename
		filename := strings.TrimSuffix(e.Name(), ".json")
		filename = strings.ReplaceAll(filename, "_", "/") // Approximate reverse

		key := sc.makeKey(filename, entry.SourceHash)
		sc.cache[key] = &entry
	}
}

// cleanupExpired removes expired entries periodically
func (sc *SummaryCache) cleanupExpired() {
	ticker := time.NewTicker(sc.ttl / 4)
	defer ticker.Stop()

	for range ticker.C {
		sc.mu.Lock()

		now := time.Now()
		for key, entry := range sc.cache {
			if now.Sub(entry.CreatedAt) > sc.ttl {
				delete(sc.cache, key)
			}
		}

		sc.mu.Unlock()
	}
}

// Clear removes all cached summaries
func (sc *SummaryCache) Clear() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	sc.cache = make(map[string]*SummaryEntry)

	// Also clear disk cache
	if sc.cacheDir != "" {
		os.RemoveAll(sc.cacheDir)
	}
}

// Stats returns cache statistics
func (sc *SummaryCache) Stats() map[string]interface{} {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	return map[string]interface{}{
		"entries":     len(sc.cache),
		"cache_dir":   sc.cacheDir,
		"ttl_hours":   int(sc.ttl.Hours()),
		"disk_cached": sc.diskCacheCount(),
	}
}

// diskCacheCount returns the number of persisted cache files
func (sc *SummaryCache) diskCacheCount() int {
	if sc.cacheDir == "" {
		return 0
	}

	entries, err := os.ReadDir(sc.cacheDir)
	if err != nil {
		return 0
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	return count
}

// ComputeHash calculates SHA256 hash of content for cache invalidation
func ComputeHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}
