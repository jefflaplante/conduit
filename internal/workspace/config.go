package workspace

import "time"

// Config contains workspace context configuration
type Config struct {
	// ContextDir is the workspace directory path
	ContextDir string `json:"context_dir"`

	// Files configuration
	Files FilesConfig `json:"files"`

	// Security configuration
	Security SecurityConfig `json:"security"`

	// Caching configuration
	Caching CachingConfig `json:"caching"`
}

// FilesConfig defines which files to load
type FilesConfig struct {
	// Core workspace files to always look for
	Core []string `json:"core"`

	// Memory files configuration
	Memory MemoryConfig `json:"memory"`

	// Maximum file size in KB
	MaxFileSizeKB int64 `json:"max_file_size_kb"`
}

// MemoryConfig configures memory file loading
type MemoryConfig struct {
	// Enabled controls whether memory files are loaded
	Enabled bool `json:"enabled"`

	// DailyLookbackDays controls how many recent daily memory files to load
	DailyLookbackDays int `json:"daily_lookback_days"`
}

// SecurityConfig controls file access permissions
type SecurityConfig struct {
	// EnforceAccessRules enables/disables access rule enforcement
	EnforceAccessRules bool `json:"enforce_access_rules"`

	// MemoryMainOnly restricts MEMORY.md to main sessions only
	MemoryMainOnly bool `json:"memory_main_only"`

	// StrictMode enables additional security checks
	StrictMode bool `json:"strict_mode"`
}

// CachingConfig controls file caching behavior
type CachingConfig struct {
	// Enabled controls whether caching is active
	Enabled bool `json:"enabled"`

	// TTLSeconds sets cache entry time-to-live
	TTLSeconds int `json:"ttl_seconds"`

	// MaxCacheSizeMB limits total cache memory usage
	MaxCacheSizeMB int64 `json:"max_cache_size_mb"`

	// WatchFileChanges enables file system watching for cache invalidation
	WatchFileChanges bool `json:"watch_file_changes"`
}

// DefaultConfig returns sensible default configuration
func DefaultConfig() *Config {
	return &Config{
		ContextDir: "./workspace", // Default workspace directory
		Files: FilesConfig{
			Core: []string{
				"SOUL.md",
				"USER.md",
				"AGENTS.md",
				"TOOLS.md",
				"MEMORY.md",
				"HEARTBEAT.md",
			},
			Memory: MemoryConfig{
				Enabled:           true,
				DailyLookbackDays: 2, // Today + yesterday
			},
			MaxFileSizeKB: 500, // 500KB per file
		},
		Security: SecurityConfig{
			EnforceAccessRules: true,
			MemoryMainOnly:     true,
			StrictMode:         false,
		},
		Caching: CachingConfig{
			Enabled:          true,
			TTLSeconds:       300,   // 5 minutes
			MaxCacheSizeMB:   50,    // 50MB max cache
			WatchFileChanges: false, // Disabled by default for performance
		},
	}
}

// Validate checks configuration for errors
func (c *Config) Validate() error {
	if c.ContextDir == "" {
		return ErrWorkspaceDirNotFound
	}

	if c.Files.MaxFileSizeKB <= 0 {
		c.Files.MaxFileSizeKB = 500 // Default to 500KB
	}

	if c.Caching.TTLSeconds <= 0 {
		c.Caching.TTLSeconds = 300 // Default to 5 minutes
	}

	if c.Caching.MaxCacheSizeMB <= 0 {
		c.Caching.MaxCacheSizeMB = 50 // Default to 50MB
	}

	if c.Files.Memory.DailyLookbackDays < 0 {
		c.Files.Memory.DailyLookbackDays = 2 // Default to 2 days
	}

	return nil
}

// GetCacheTTL returns cache TTL as time.Duration
func (c *Config) GetCacheTTL() time.Duration {
	return time.Duration(c.Caching.TTLSeconds) * time.Second
}

// GetMaxCacheSize returns max cache size in bytes
func (c *Config) GetMaxCacheSize() int64 {
	return c.Caching.MaxCacheSizeMB * 1024 * 1024
}

// GetMaxFileSize returns max file size in bytes
func (c *Config) GetMaxFileSize() int64 {
	return c.Files.MaxFileSizeKB * 1024
}
