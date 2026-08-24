package skills

import (
	"time"
)

// Skill represents a discovered and parsed Conduit skill
type Skill struct {
	Name         string            `json:"name" yaml:"name"`
	Description  string            `json:"description" yaml:"description"`
	Location     string            `json:"location"`
	Content      string            `json:"content"`
	Scripts      []SkillScript     `json:"scripts"`
	References   []SkillReference  `json:"references"`
	Dependencies []SkillDependency `json:"dependencies,omitempty"`
	Metadata     SkillMetadata     `json:"metadata"`
}

// SkillDependency represents a reference file auto-loaded with the skill.
// Paths are declared in the skill's Dependencies section (e.g. "reference/ha-entities.md")
// and resolved relative to the skill directory at load time.
type SkillDependency struct {
	// Path is the dependency path exactly as declared in SKILL.md (relative to skill dir).
	Path string `json:"path"`
	// ResolvedPath is the absolute filesystem path after resolving against skill location.
	ResolvedPath string `json:"resolved_path,omitempty"`
	// Content is the file contents (empty when Missing or Skipped is true).
	Content string `json:"-"`
	// Size is the file size in bytes (0 if missing).
	Size int64 `json:"size,omitempty"`
	// Missing is true when the dep file could not be found at resolve time.
	Missing bool `json:"missing,omitempty"`
	// Skipped is true when the dep was skipped (e.g. too large, unsafe path).
	Skipped bool `json:"skipped,omitempty"`
	// SkipReason describes why the dep was skipped.
	SkipReason string `json:"skip_reason,omitempty"`
}

// SkillMetadata contains Conduit-specific skill configuration
type SkillMetadata struct {
	Conduit SkillConduitMeta `json:"conduit" yaml:"conduit"`
}

// SkillConduitMeta contains skill requirements and configuration
type SkillConduitMeta struct {
	Emoji    string            `json:"emoji" yaml:"emoji"`
	Requires SkillRequirements `json:"requires" yaml:"requires"`
	Produces []string          `json:"produces" yaml:"produces"` // Brain keys this skill produces (e.g., "solar.production", "weather.temp")
}

// SkillRequirements defines what a skill needs to function
type SkillRequirements struct {
	AnyBins []string `json:"anyBins" yaml:"anyBins"`
	AllBins []string `json:"allBins" yaml:"allBins"`
	Files   []string `json:"files" yaml:"files"`
	Env     []string `json:"env" yaml:"env"`
}

// SkillScript represents an executable script within a skill
type SkillScript struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Language string `json:"language"`
}

// SkillReference represents a supporting file for a skill
type SkillReference struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// ExecutionMethod defines how a skill should be executed
type ExecutionMethod string

const (
	ExecutionMethodSubprocess ExecutionMethod = "subprocess"
	ExecutionMethodScript     ExecutionMethod = "script"
	ExecutionMethodAPI        ExecutionMethod = "api"
)

// ExecutionResult contains the result of skill execution
type ExecutionResult struct {
	Success bool                   `json:"success"`
	Output  string                 `json:"output"`
	Error   string                 `json:"error,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// SkillsConfig defines configuration for the skills system
type SkillsConfig struct {
	Enabled     bool            `json:"enabled"`
	SearchPaths []string        `json:"search_paths"`
	Execution   ExecutionConfig `json:"execution"`
	Cache       CacheConfig     `json:"cache"`
	// InlineDependencies controls whether declared reference dependencies are
	// auto-inlined into the system prompt. When false, dependency paths are
	// listed only (agent reads them on demand). Default: true (legacy behavior).
	InlineDependencies *bool `json:"inline_dependencies,omitempty"`
	// MaxDependencyChars caps the inline content per dependency (0 = unlimited).
	MaxDependencyChars int `json:"max_dependency_chars,omitempty"`
}

// DependencyInlineMode reports the effective dependency inlining mode.
func (c *SkillsConfig) DependencyInlineMode() (inline bool, maxChars int) {
	if c == nil || c.InlineDependencies == nil {
		return true, 0 // legacy default: inline everything
	}
	return *c.InlineDependencies, c.MaxDependencyChars
}

// ExecutionConfig defines execution-specific settings
type ExecutionConfig struct {
	TimeoutSeconds int                 `json:"timeout_seconds"`
	Environment    map[string]string   `json:"environment"`
	AllowedActions map[string][]string `json:"allowed_actions"`
}

// CacheConfig defines caching behavior
type CacheConfig struct {
	TTLSeconds int  `json:"ttl_seconds"`
	Enabled    bool `json:"enabled"`
}

// SkillCache represents a cached skill with expiry
type SkillCache struct {
	Skills []Skill   `json:"skills"`
	Expiry time.Time `json:"expiry"`
}
