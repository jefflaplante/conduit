package planning

import (
	"context"
	"sync"
)

// MemoryStorage provides in-memory cache storage implementation
type MemoryStorage struct {
	entries map[string]*CacheEntry
	mu      sync.RWMutex
}

// NewMemoryStorage creates a new in-memory cache storage
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		entries: make(map[string]*CacheEntry),
	}
}

// Get retrieves a cache entry
func (ms *MemoryStorage) Get(ctx context.Context, key string) (*CacheEntry, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	entry, exists := ms.entries[key]
	if !exists {
		return nil, nil
	}

	// Return copy to prevent external modification
	entryCopy := *entry
	return &entryCopy, nil
}

// Set stores a cache entry
func (ms *MemoryStorage) Set(ctx context.Context, key string, entry *CacheEntry) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	// Store copy to prevent external modification
	entryCopy := *entry
	ms.entries[key] = &entryCopy

	return nil
}

// Delete removes a cache entry
func (ms *MemoryStorage) Delete(ctx context.Context, key string) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	delete(ms.entries, key)
	return nil
}

// Clear removes all cache entries
func (ms *MemoryStorage) Clear(ctx context.Context) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.entries = make(map[string]*CacheEntry)
	return nil
}

// Keys returns all cache keys
func (ms *MemoryStorage) Keys(ctx context.Context) ([]string, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	keys := make([]string, 0, len(ms.entries))
	for key := range ms.entries {
		keys = append(keys, key)
	}

	return keys, nil
}

// Size returns the total size of all cached data
func (ms *MemoryStorage) Size(ctx context.Context) (int64, error) {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	var totalSize int64
	for _, entry := range ms.entries {
		totalSize += entry.Size
	}

	return totalSize, nil
}
