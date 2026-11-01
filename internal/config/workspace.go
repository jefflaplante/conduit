package config

// WorkspaceConfig contains workspace context settings
type WorkspaceConfig struct {
	ContextDir string                  `json:"context_dir"`
	Files      WorkspaceFilesConfig    `json:"files"`
	Security   WorkspaceSecurityConfig `json:"security"`
	Caching    WorkspaceCacheConfig    `json:"caching"`
	Summary    WorkspaceSummaryConfig  `json:"summary,omitempty"`
}

// WorkspaceSummaryConfig configures AI-powered summarization for small-context models
type WorkspaceSummaryConfig struct {
	// Enabled controls whether summarization is active (default: false)
	Enabled bool `json:"enabled"`

	// Model is the AI model for summarization (default: claude-haiku-4-5-20251001)
	Model string `json:"model,omitempty"`

	// TargetRatio is the default compression ratio (default: 0.25 = keep 25%)
	TargetRatio float64 `json:"target_ratio,omitempty"`

	// CacheDir is the directory for persisted summaries (default: .summaries)
	CacheDir string `json:"cache_dir,omitempty"`

	// CacheTTLHours is how long cached summaries are valid (default: 168 = 7 days)
	CacheTTLHours int `json:"cache_ttl_hours,omitempty"`

	// FallbackToTruncate uses simple truncation if AI fails (default: true)
	FallbackToTruncate *bool `json:"fallback_to_truncate,omitempty"`

	// FileConfigs provides per-file override settings
	FileConfigs map[string]SummaryFileConfig `json:"file_configs,omitempty"`
}

// SummaryFileConfig provides file-specific summarization settings
type SummaryFileConfig struct {
	Ratio        float64  `json:"ratio,omitempty"`
	PreserveKeys []string `json:"preserve_keys,omitempty"`
}

// DefaultWorkspaceStateFile is the default session state file (bd-3ia),
// relative to workspace.context_dir.
const DefaultWorkspaceStateFile = "memory/jules-state.json"

// WorkspaceFilesConfig defines which files to load
type WorkspaceFilesConfig struct {
	Core   []string              `json:"core"`
	Memory WorkspaceMemoryConfig `json:"memory"`

	// StateFile is the session state file, relative to the workspace
	// context dir (bd-3ia). Empty = DefaultWorkspaceStateFile.
	StateFile string `json:"state_file,omitempty"`

	// MemoryMDInject controls MEMORY.md injection into the prompt;
	// nil = true (bd-3ia).
	MemoryMDInject *bool `json:"memory_md_inject,omitempty"`
}

// StateFilePath returns the configured state file or the default (bd-3ia).
func (f WorkspaceFilesConfig) StateFilePath() string {
	if f.StateFile == "" {
		return DefaultWorkspaceStateFile
	}
	return f.StateFile
}

// MemoryMDInjectEnabled reports whether MEMORY.md is injected; nil = true.
func (f WorkspaceFilesConfig) MemoryMDInjectEnabled() bool {
	return f.MemoryMDInject == nil || *f.MemoryMDInject
}

// WorkspaceMemoryConfig defines memory file handling
type WorkspaceMemoryConfig struct {
	Enabled           bool `json:"enabled"`
	DailyLookbackDays int  `json:"daily_lookback_days"`
	MaxFileSizeKB     int  `json:"max_file_size_kb"`
}

// WorkspaceSecurityConfig defines security settings
type WorkspaceSecurityConfig struct {
	EnforceAccessRules bool   `json:"enforce_access_rules"`
	MemoryMainOnly     bool   `json:"memory_main_only"`
	DefaultPolicy      string `json:"default_policy,omitempty"` // "allow" or "deny" for unmatched files (default: "deny")
}

// WorkspaceCacheConfig defines caching settings
type WorkspaceCacheConfig struct {
	Enabled        bool `json:"enabled"`
	TTLSeconds     int  `json:"ttl_seconds"`
	MaxCacheSizeMB int  `json:"max_cache_size_mb"`
}
