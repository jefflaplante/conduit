package workspace

import "time"

// SummaryConfig configures AI-powered workspace context summarization
type SummaryConfig struct {
	// Enabled controls whether summarization is active
	Enabled bool `json:"enabled"`

	// Model is the AI model to use for summarization (default: claude-haiku-4-5-20251001)
	Model string `json:"model"`

	// TargetRatio is the default compression ratio (0.25 = keep 25% of content)
	TargetRatio float64 `json:"target_ratio"`

	// CacheDir is the directory for persisted summaries (default: .summaries)
	CacheDir string `json:"cache_dir"`

	// CacheTTLHours is how long cached summaries are valid (default: 168 = 7 days)
	CacheTTLHours int `json:"cache_ttl_hours"`

	// FallbackToTruncate uses simple truncation if AI summarization fails
	FallbackToTruncate bool `json:"fallback_to_truncate"`

	// FileConfigs provides per-file override settings
	FileConfigs map[string]SummaryFileConfig `json:"file_configs,omitempty"`
}

// SummaryFileConfig provides file-specific summarization settings
type SummaryFileConfig struct {
	// Ratio overrides TargetRatio for this file
	Ratio float64 `json:"ratio,omitempty"`

	// PreserveKeys are concepts/keywords to emphasize when summarizing
	PreserveKeys []string `json:"preserve_keys,omitempty"`
}

// SummaryEntry represents a cached summary
type SummaryEntry struct {
	// SourceHash is SHA256 of the original content for cache invalidation
	SourceHash string `json:"source_hash"`

	// Summary is the compressed content
	Summary string `json:"summary"`

	// Ratio is the actual compression achieved (len(summary)/len(original))
	Ratio float64 `json:"ratio"`

	// CreatedAt is when the summary was generated
	CreatedAt time.Time `json:"created_at"`

	// Model is which AI model generated this summary
	Model string `json:"model"`
}

// DefaultSummaryConfig returns sensible defaults for summarization
func DefaultSummaryConfig() SummaryConfig {
	return SummaryConfig{
		Enabled:            false, // Opt-in feature
		Model:              "claude-haiku-4-5-20251001",
		TargetRatio:        0.25,
		CacheDir:           ".summaries",
		CacheTTLHours:      168, // 7 days
		FallbackToTruncate: true,
		FileConfigs: map[string]SummaryFileConfig{
			"SOUL.md": {
				Ratio:        0.40,
				PreserveKeys: []string{"personality", "tone", "voice", "style"},
			},
			"USER.md": {
				Ratio:        0.30,
				PreserveKeys: []string{"preferences", "constraints", "requirements"},
			},
			"AGENTS.md": {
				Ratio:        0.25,
				PreserveKeys: []string{"rules", "restrictions", "mandatory", "never"},
			},
			"TOOLS.md": {
				Ratio:        0.20,
				PreserveKeys: []string{"usage", "commands", "examples"},
			},
		},
	}
}

// GetTargetRatio returns the compression ratio for a file
func (c *SummaryConfig) GetTargetRatio(filename string) float64 {
	if fc, ok := c.FileConfigs[filename]; ok && fc.Ratio > 0 {
		return fc.Ratio
	}
	if c.TargetRatio > 0 {
		return c.TargetRatio
	}
	return 0.25
}

// GetPreserveKeys returns keywords to emphasize for a file
func (c *SummaryConfig) GetPreserveKeys(filename string) []string {
	if fc, ok := c.FileConfigs[filename]; ok {
		return fc.PreserveKeys
	}
	return nil
}

// GetCacheTTL returns the cache TTL as a duration
func (c *SummaryConfig) GetCacheTTL() time.Duration {
	if c.CacheTTLHours > 0 {
		return time.Duration(c.CacheTTLHours) * time.Hour
	}
	return 168 * time.Hour // 7 days default
}
