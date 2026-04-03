package ai

import "strings"

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

// DefaultCacheMinTokens for unknown models
const DefaultCacheMinTokens = 2048

// GetCacheMinTokens returns the minimum tokens needed for caching a given model
func GetCacheMinTokens(model string) int {
	for prefix, minTokens := range CacheMinTokens {
		if strings.HasPrefix(model, prefix) {
			return minTokens
		}
	}
	return DefaultCacheMinTokens
}

// PromptCachingConfig holds configuration for prompt caching behavior
type PromptCachingConfig struct {
	Enabled                   bool `json:"enabled"`                     // Master switch for prompt caching
	ExtendedTTL               bool `json:"extended_ttl"`                // Use 1-hour TTL (2x write cost) vs 5-minute default
	CacheTools                bool `json:"cache_tools"`                 // Cache tool definitions
	CacheSystem               bool `json:"cache_system"`                // Cache system prompt
	CacheHistory              bool `json:"cache_history"`               // Cache conversation history
	HistoryBreakpointInterval int  `json:"history_breakpoint_interval"` // Messages between history breakpoints
}

// DefaultPromptCachingConfig returns sensible defaults for prompt caching
func DefaultPromptCachingConfig() PromptCachingConfig {
	return PromptCachingConfig{
		Enabled:                   true,
		ExtendedTTL:               false, // 5-minute default TTL
		CacheTools:                true,
		CacheSystem:               true,
		CacheHistory:              true,
		HistoryBreakpointInterval: 15,
	}
}
