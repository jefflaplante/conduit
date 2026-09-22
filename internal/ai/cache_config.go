package ai

import (
	"sort"
	"strings"
)

// CacheMinTokens maps model prefixes to minimum cacheable tokens
// Based on Anthropic's prompt caching documentation
var CacheMinTokens = map[string]int{
	"claude-opus-4-6":   4096,
	"claude-opus-4-5":   4096,
	"claude-sonnet-4-6": 2048,
	"claude-sonnet-4-5": 1024,
	"claude-sonnet-4":   1024,
	"claude-sonnet-3.7": 1024,
	"claude-haiku-4-5":  4096,
	"claude-haiku-3.5":  2048,
	"claude-haiku-3":    2048,
}

// cacheMinTokensPrefixes is sorted by length (longest first) for correct prefix matching
var cacheMinTokensPrefixes []string

func init() {
	cacheMinTokensPrefixes = make([]string, 0, len(CacheMinTokens))
	for prefix := range CacheMinTokens {
		cacheMinTokensPrefixes = append(cacheMinTokensPrefixes, prefix)
	}
	sort.Slice(cacheMinTokensPrefixes, func(i, j int) bool {
		return len(cacheMinTokensPrefixes[i]) > len(cacheMinTokensPrefixes[j])
	})
}

// DefaultCacheMinTokens for unknown models
const DefaultCacheMinTokens = 2048

// GetCacheMinTokens returns the minimum tokens needed for caching a given model
func GetCacheMinTokens(model string) int {
	for _, prefix := range cacheMinTokensPrefixes {
		if strings.HasPrefix(model, prefix) {
			return CacheMinTokens[prefix]
		}
	}
	return DefaultCacheMinTokens
}

// conduit-3dru: PromptCachingConfig moved to internal/config (canonical home).
// DefaultPromptCachingConfig also lives there now: config.DefaultPromptCachingConfig().
