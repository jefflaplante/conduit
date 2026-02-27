package skills

import (
	"time"
)

// Skill represents a discovered and parsed Conduit skill
type Skill struct {
	Name        string           `json:"name" yaml:"name"`
	Description string           `json:"description" yaml:"description"`
	Location    string           `json:"location"`
	Content     string           `json:"content"`
	Scripts     []SkillScript    `json:"scripts"`
	References  []SkillReference `json:"references"`
	Metadata    SkillMetadata    `json:"metadata"`
}

// SkillMetadata contains Conduit-specific skill configuration
type SkillMetadata struct {
	Conduit SkillConduitMeta `json:"conduit" yaml:"conduit"`
}

// SkillConduitMeta contains skill requirements and configuration
type SkillConduitMeta struct {
	Emoji    string            `json:"emoji" yaml:"emoji"`
	Requires SkillRequirements `json:"requires" yaml:"requires"`
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
