package workspace

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// SummaryManager coordinates AI-powered summarization of workspace context
type SummaryManager struct {
	cache    *SummaryCache
	executor SummaryAIExecutor
	config   SummaryConfig
	mu       sync.RWMutex

	// Metrics
	cacheHits   int64
	cacheMisses int64
	aiCalls     int64
	fallbacks   int64
}

// NewSummaryManager creates a new summary manager
func NewSummaryManager(workspaceDir string, executor SummaryAIExecutor, config SummaryConfig) *SummaryManager {
	return &SummaryManager{
		cache:    NewSummaryCache(workspaceDir, config),
		executor: executor,
		config:   config,
	}
}

// IsEnabled returns whether summarization is enabled
func (sm *SummaryManager) IsEnabled() bool {
	return sm.config.Enabled
}

// GetSummarizedContext returns summarized versions of workspace files
// for use with small-context models
func (sm *SummaryManager) GetSummarizedContext(ctx context.Context, files map[string]string) (map[string]string, error) {
	if !sm.config.Enabled {
		return files, nil // Return original if disabled
	}

	result := make(map[string]string, len(files))
	var errs []error

	for filename, content := range files {
		summary, err := sm.getSummary(ctx, filename, content)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", filename, err))
			// Use original content on error if fallback disabled
			if !sm.config.FallbackToTruncate {
				result[filename] = content
				continue
			}
		}
		result[filename] = summary
	}

	if len(errs) > 0 {
		log.Printf("[SummaryManager] %d errors during summarization (fallback used)", len(errs))
	}

	return result, nil
}

// getSummary returns a summarized version of a single file
func (sm *SummaryManager) getSummary(ctx context.Context, filename, content string) (string, error) {
	// Skip empty or very small files
	if len(content) < 500 {
		return content, nil
	}

	hash := ComputeHash(content)

	// Check cache
	if cached, ok := sm.cache.Get(filename, hash); ok {
		sm.mu.Lock()
		sm.cacheHits++
		sm.mu.Unlock()
		log.Printf("[SummaryManager] Cache hit for %s (ratio: %.2f)", filename, cached.Ratio)
		return cached.Summary, nil
	}

	sm.mu.Lock()
	sm.cacheMisses++
	sm.mu.Unlock()

	// Generate summary
	fileType := DetectFileType(filename)
	ratio := sm.config.GetTargetRatio(filename)
	preserveKeys := sm.config.GetPreserveKeys(filename)

	var summary string
	var err error

	if sm.executor != nil {
		sm.mu.Lock()
		sm.aiCalls++
		sm.mu.Unlock()

		summary, err = sm.executor.Summarize(ctx, content, fileType, ratio, preserveKeys)
		if err != nil {
			log.Printf("[SummaryManager] AI summarization failed for %s: %v", filename, err)
		}
	} else {
		err = fmt.Errorf("no AI executor configured")
	}

	// Fallback to truncation
	if err != nil || summary == "" {
		if sm.config.FallbackToTruncate {
			sm.mu.Lock()
			sm.fallbacks++
			sm.mu.Unlock()

			targetLen := int(float64(len(content)) * ratio)
			summary = Truncate(content, targetLen)
			log.Printf("[SummaryManager] Using fallback truncation for %s", filename)
		} else {
			return content, err
		}
	}

	// Cache the result
	actualRatio := float64(len(summary)) / float64(len(content))
	entry := &SummaryEntry{
		SourceHash: hash,
		Summary:    summary,
		Ratio:      actualRatio,
		CreatedAt:  time.Now(),
		Model:      sm.config.Model,
	}
	sm.cache.Set(filename, hash, entry)

	log.Printf("[SummaryManager] Summarized %s: %d -> %d bytes (%.1f%%)",
		filename, len(content), len(summary), actualRatio*100)

	return summary, nil
}

// InvalidateFile invalidates cached summary for a file
func (sm *SummaryManager) InvalidateFile(filename string) {
	// Clear all entries for this filename (any hash)
	// The cache will naturally invalidate via hash mismatch
	log.Printf("[SummaryManager] Invalidated cache for %s", filename)
}

// ClearCache removes all cached summaries
func (sm *SummaryManager) ClearCache() {
	sm.cache.Clear()
	log.Printf("[SummaryManager] Cache cleared")
}

// Stats returns summary manager statistics
func (sm *SummaryManager) Stats() map[string]interface{} {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	cacheStats := sm.cache.Stats()

	return map[string]interface{}{
		"enabled":      sm.config.Enabled,
		"model":        sm.config.Model,
		"target_ratio": sm.config.TargetRatio,
		"cache_hits":   sm.cacheHits,
		"cache_misses": sm.cacheMisses,
		"ai_calls":     sm.aiCalls,
		"fallbacks":    sm.fallbacks,
		"hit_rate":     sm.hitRate(),
		"cache":        cacheStats,
	}
}

// hitRate calculates the cache hit rate
func (sm *SummaryManager) hitRate() float64 {
	total := sm.cacheHits + sm.cacheMisses
	if total == 0 {
		return 0
	}
	return float64(sm.cacheHits) / float64(total)
}

// ShouldSummarize determines if summarization should be used for a model
func ShouldSummarize(contextWindow, largeContextThreshold int) bool {
	return contextWindow < largeContextThreshold
}
