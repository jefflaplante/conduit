package core

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// contextCache provides two-tier caching for context data.
// Static data (git repo info, project structure) uses a longer TTL (5 minutes),
// while dynamic data (active status, uncommitted changes) uses a shorter TTL (30 seconds).
type contextCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

// cacheEntry holds a cached value with its expiration time.
type cacheEntry struct {
	value     interface{}
	expiresAt time.Time
}

// cacheTierStatic is the TTL for data that changes infrequently (5 minutes).
const cacheTierStatic = 5 * time.Minute

// cacheTierDynamic is the TTL for data that changes frequently (30 seconds).
const cacheTierDynamic = 30 * time.Second

func newContextCache() *contextCache {
	return &contextCache{
		entries: make(map[string]*cacheEntry),
	}
}

// get retrieves a cached value if it exists and has not expired.
func (c *contextCache) get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.value, true
}

// set stores a value in the cache with the given TTL.
func (c *contextCache) set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &cacheEntry{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// ---------- Caching (.2) ----------

// runGitCached runs a git command and caches the result with the given TTL.
// The cache key is derived from the working directory and the command arguments.
func (t *ContextTool) runGitCached(dir string, ttl time.Duration, gitArgs ...string) (string, error) {
	cacheKey := "git:" + dir + ":" + strings.Join(gitArgs, " ")

	if cached, ok := t.cache.get(cacheKey); ok {
		if result, ok := cached.(string); ok {
			return result, nil
		}
	}

	result, err := t.runGit(dir, gitArgs...)
	if err != nil {
		return "", err
	}

	t.cache.set(cacheKey, result, ttl)
	return result, nil
}

// runGit executes a git command in the given directory and returns trimmed stdout.
func (t *ContextTool) runGit(dir string, gitArgs ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Dir = dir

	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(output)), nil
}
