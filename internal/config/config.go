package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/reflection"
	"conduit/internal/skills"
)

// Config represents the gateway configuration
type Config struct {
	Port           int                  `json:"port"`
	Timezone       string               `json:"timezone,omitempty"`
	DataDir        string               `json:"data_dir,omitempty" cfg:"env,path"`
	SecretsFile    string               `json:"secrets_file,omitempty" cfg:"env,path"`
	AllowedOrigins []string             `json:"allowed_origins,omitempty"` // WebSocket allowed origins (empty = same-origin + localhost only)
	WebSocket      WebSocketConfig      `json:"websocket,omitempty"`
	Database       DatabaseConfig       `json:"database"`
	Search         SearchDatabaseConfig `json:"search,omitempty"`
	AI             AIConfig             `json:"ai"`
	Agent          AgentConfig          `json:"agent"`
	Workspace      WorkspaceConfig      `json:"workspace,omitempty"`
	Skills         skills.SkillsConfig  `json:"skills,omitempty"`
	Tools          ToolsConfig          `json:"tools"`
	Channels       []ChannelConfig      `json:"channels"`
	Debug          DebugConfig          `json:"debug,omitempty"`
	RateLimiting   RateLimitingConfig   `json:"rateLimiting,omitempty"`
	Diagnostics    DiagnosticsConfig    `json:"diagnostics,omitempty"`
	Heartbeat      HeartbeatConfig      `json:"heartbeat,omitempty"`
	AgentHeartbeat AgentHeartbeatConfig `json:"agent_heartbeat,omitempty"`
	SSH            SSHServerConfig      `json:"ssh,omitempty"`
	TUI            TUIConfig            `json:"tui,omitempty"`
	Vector         VectorConfig         `json:"vector,omitempty"`
	RemoteSSH      RemoteSSHConfig      `json:"remote_ssh,omitempty"`
	MQTT           MQTTConfig           `json:"mqtt,omitempty"`
	Kubernetes     KubernetesConfig     `json:"kubernetes,omitempty"`
	PagerDuty      PagerDutyConfig      `json:"pagerduty,omitempty"`
	Datadog        DatadogConfig        `json:"datadog,omitempty"`
	Auth           AuthTokenConfig      `json:"auth,omitempty"`
	Logging        LoggingConfig        `json:"logging,omitempty"`
	Brain          BrainConfig                `json:"brain,omitempty"`
	Reflection     *reflection.ReflectionConfig `json:"reflection,omitempty"`
	STT            STTConfig                  `json:"stt,omitempty"`
}

// AuthTokenConfig holds configuration for the token authentication system
type AuthTokenConfig struct {
	// TokenSecret is the HMAC key used for hashing tokens (hex-encoded, 32 bytes).
	// If empty, a random key is generated at startup (tokens won't survive restarts).
	// Supports ${ENV_VAR} expansion.
	TokenSecret string `json:"token_secret,omitempty" cfg:"env"`
}

// LoggingConfig holds structured logging configuration
type LoggingConfig struct {
	// Level is the minimum log level: debug, info, warn, error (default: info)
	Level string `json:"level,omitempty"`
	// Format is the output format: text, json (default: text)
	Format string `json:"format,omitempty"`
}

// DefaultLoggingConfig returns sensible defaults for logging
func DefaultLoggingConfig() LoggingConfig {
	return LoggingConfig{
		Level:  "info",
		Format: "text",
	}
}

// GetLevel returns the configured level or default
func (c *LoggingConfig) GetLevel() string {
	if c.Level == "" {
		return "info"
	}
	return c.Level
}

// GetFormat returns the configured format or default
func (c *LoggingConfig) GetFormat() string {
	if c.Format == "" {
		return "text"
	}
	return c.Format
}

// VectorConfig holds configuration for the optional vector/semantic search service.
type VectorConfig struct {
	Enabled       bool               `json:"enabled"`
	Path          string             `json:"path,omitempty"`           // Path to vector DB file (derived from gateway DB if empty)
	ChunkSize     int                `json:"chunk_size,omitempty"`     // Max tokens per chunk (default 500)
	EmbedDims     int                `json:"embed_dims,omitempty"`     // Embedding dimensions (0 = use embedder default: 768 Ollama, 1536 OpenAI)
	EmbedProvider string             `json:"embed_provider,omitempty"` // "" or "auto" (default), "ollama", "openai"
	EmbedTimeout  int                `json:"embed_timeout,omitempty"`  // Per-file embedding timeout in seconds (default 300)
	EmbedPacing   int                `json:"embed_pacing,omitempty"`   // Delay between embedding calls in seconds (default 2)
	OpenAI        *OpenAIEmbedConfig `json:"openai,omitempty"`
	Ollama        *OllamaEmbedConfig `json:"ollama,omitempty"`
}

// STTConfig holds configuration for speech-to-text transcription.
type STTConfig struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"` // "whisper" (default)
	APIKey   string `json:"api_key,omitempty" cfg:"env"`
	Model    string `json:"model,omitempty"` // Default: "whisper-1"
}

// OpenAIEmbedConfig holds configuration for OpenAI embedding provider.
type OpenAIEmbedConfig struct {
	APIKey string `json:"api_key,omitempty" cfg:"env"` // Supports ${ENV_VAR} expansion
	Model  string `json:"model,omitempty"`             // Default: "text-embedding-3-small"
}

// OllamaEmbedConfig holds configuration for the Ollama embedding provider.
type OllamaEmbedConfig struct {
	Host  string `json:"host,omitempty"`  // Default: OLLAMA_HOST env or http://localhost:11434
	Model string `json:"model,omitempty"` // Default: nomic-embed-text
}

// DeriveVectorDBPath returns a vector DB path derived from the gateway DB path.
// For example, "gateway.db" becomes "gateway.vector.db".
func DeriveVectorDBPath(gatewayDBPath string) string {
	ext := filepath.Ext(gatewayDBPath)
	base := strings.TrimSuffix(gatewayDBPath, ext)
	if ext == "" {
		ext = ".db"
	}
	return base + ".vector" + ext
}

// DeriveBrainDBPath returns a brain DB path derived from the gateway DB path.
// For example, "gateway.db" becomes "gateway.brain.db".
func DeriveBrainDBPath(gatewayDBPath string) string {
	ext := filepath.Ext(gatewayDBPath)
	base := strings.TrimSuffix(gatewayDBPath, ext)
	if ext == "" {
		ext = ".db"
	}
	return base + ".brain" + ext
}

// BrainConfig holds configuration for the tiered memory (Brain) subsystem.
type BrainConfig struct {
	Enabled              bool    `json:"enabled"`
	Path                 string  `json:"path,omitempty"`                    // Path to brain DB file (derived from gateway DB if empty)
	MaxLTMEntries        int     `json:"max_ltm_entries,omitempty"`         // Maximum long-term memory entries (default 10000)
	WMGracePeriodSeconds int     `json:"wm_grace_period_seconds,omitempty"` // Seconds to keep WM after session end (default 300)
	AutoFlushSeconds     int     `json:"auto_flush_seconds,omitempty"`      // Auto-flush interval in seconds (default 600)
	ConsolidateThreshold float64 `json:"consolidate_threshold,omitempty"`   // Salience threshold for auto-promote (default 0.6)
	EvictThreshold       float64 `json:"evict_threshold,omitempty"`         // Salience threshold for eviction (default 0.1)
	AutoPromote          bool    `json:"auto_promote,omitempty"`            // Auto-promote high-salience WM keys on consolidation

	// Salience formula weights (must sum to 1.0)
	AccessWeight  float64 `json:"access_weight,omitempty"`  // default 0.4
	RecencyWeight float64 `json:"recency_weight,omitempty"` // default 0.4
	TierWeight    float64 `json:"tier_weight,omitempty"`    // default 0.2

	// Recency decay rate: 1/(1 + hours * decay_rate). Higher = faster decay
	RecencyDecayRate float64 `json:"recency_decay_rate,omitempty"` // default 1.0

	// Access count normalization cap
	AccessCountCap int `json:"access_count_cap,omitempty"` // default 100

	// REM Sleep configuration
	REMEnabled           bool    `json:"rem_enabled,omitempty"`
	REMSchedule          string  `json:"rem_schedule,omitempty"`
	REMIntegrationDay    int     `json:"rem_integration_day,omitempty"`
	REMPruneAgeDays      int     `json:"rem_prune_age_days,omitempty"`
	REMSalienceDecayRate float64 `json:"rem_salience_decay_rate,omitempty"`
	REMGroomWithLLM      bool    `json:"rem_groom_with_llm,omitempty"`
	REMLogPath           string  `json:"rem_log_path,omitempty"`
}

// DefaultBrainConfig returns sensible defaults for the brain subsystem.
func DefaultBrainConfig() BrainConfig {
	return BrainConfig{
		Enabled:              false,
		MaxLTMEntries:        10000,
		WMGracePeriodSeconds: 300,
		AutoFlushSeconds:     600,
		ConsolidateThreshold: 0.6,
		EvictThreshold:       0.1,
		AutoPromote:          true,
		AccessWeight:         0.4,
		RecencyWeight:        0.4,
		TierWeight:           0.2,
		RecencyDecayRate:     1.0,
		AccessCountCap:       100,
		REMEnabled:           true,
		REMSchedule:          "0 2 * * *",
		REMIntegrationDay:    0,
		REMPruneAgeDays:      30,
		REMSalienceDecayRate: 0.1,
		REMGroomWithLLM:      true,
		REMLogPath:           "memory/rem-log",
	}
}

// ApplyDefaults fills in zero-valued fields with sensible defaults.
// Called before Validate to handle omitempty JSON fields that weren't specified.
func (b *BrainConfig) ApplyDefaults() {
	defaults := DefaultBrainConfig()
	if b.MaxLTMEntries == 0 {
		b.MaxLTMEntries = defaults.MaxLTMEntries
	}
	if b.WMGracePeriodSeconds == 0 {
		b.WMGracePeriodSeconds = defaults.WMGracePeriodSeconds
	}
	if b.AutoFlushSeconds == 0 {
		b.AutoFlushSeconds = defaults.AutoFlushSeconds
	}
	if b.ConsolidateThreshold == 0 {
		b.ConsolidateThreshold = defaults.ConsolidateThreshold
	}
	if b.EvictThreshold == 0 {
		b.EvictThreshold = defaults.EvictThreshold
	}
	if b.AccessWeight == 0 && b.RecencyWeight == 0 && b.TierWeight == 0 {
		b.AccessWeight = defaults.AccessWeight
		b.RecencyWeight = defaults.RecencyWeight
		b.TierWeight = defaults.TierWeight
	}
	if b.RecencyDecayRate == 0 {
		b.RecencyDecayRate = defaults.RecencyDecayRate
	}
	if b.AccessCountCap == 0 {
		b.AccessCountCap = defaults.AccessCountCap
	}
	if b.REMSchedule == "" {
		b.REMSchedule = defaults.REMSchedule
	}
	if b.REMIntegrationDay == 0 {
		b.REMIntegrationDay = defaults.REMIntegrationDay
	}
	if b.REMPruneAgeDays == 0 {
		b.REMPruneAgeDays = defaults.REMPruneAgeDays
	}
	if b.REMSalienceDecayRate == 0 {
		b.REMSalienceDecayRate = defaults.REMSalienceDecayRate
	}
	if b.REMLogPath == "" {
		b.REMLogPath = defaults.REMLogPath
	}
}

// Validate checks the brain configuration for errors.
func (b *BrainConfig) Validate() error {
	if !b.Enabled {
		return nil
	}
	b.ApplyDefaults()
	if b.MaxLTMEntries < 0 {
		return fmt.Errorf("max_ltm_entries must be non-negative")
	}
	if b.ConsolidateThreshold < 0 || b.ConsolidateThreshold > 1 {
		return fmt.Errorf("consolidate_threshold must be between 0 and 1")
	}
	if b.EvictThreshold < 0 || b.EvictThreshold > 1 {
		return fmt.Errorf("evict_threshold must be between 0 and 1")
	}
	if b.AccessWeight > 0 || b.RecencyWeight > 0 || b.TierWeight > 0 {
		sum := b.AccessWeight + b.RecencyWeight + b.TierWeight
		if sum < 0.99 || sum > 1.01 {
			return fmt.Errorf("brain salience weights must sum to 1.0 (got %.2f)", sum)
		}
	}
	if b.RecencyDecayRate < 0 {
		return fmt.Errorf("brain recency_decay_rate must be non-negative")
	}
	if b.AccessCountCap < 1 {
		return fmt.Errorf("brain access_count_cap must be >= 1")
	}
	return nil
}

// SSHServerConfig holds configuration for the integrated SSH server
type SSHServerConfig struct {
	Enabled            bool   `json:"enabled"`
	ListenAddr         string `json:"listen_addr,omitempty"`
	HostKeyPath        string `json:"host_key_path,omitempty" cfg:"path"`
	AuthorizedKeysPath string `json:"authorized_keys_path,omitempty" cfg:"path"`
}

// WebSocketConfig holds configuration for WebSocket connections
type WebSocketConfig struct {
	// MaxMessageSize is the maximum size in bytes of incoming WebSocket messages.
	// Messages exceeding this limit will be rejected with a close error.
	// Default: 1048576 (1MB). Set to 0 to use default.
	MaxMessageSize int64 `json:"max_message_size,omitempty"`
}

// DefaultWebSocketConfig returns sensible defaults for WebSocket configuration
func DefaultWebSocketConfig() WebSocketConfig {
	return WebSocketConfig{
		MaxMessageSize: 1048576, // 1MB default
	}
}

// GetMaxMessageSize returns the configured max message size, or the default if not set
func (c *WebSocketConfig) GetMaxMessageSize() int64 {
	if c.MaxMessageSize <= 0 {
		return 1048576 // 1MB default
	}
	return c.MaxMessageSize
}

// TUIConfig holds configuration for the TUI shell escape feature
type TUIConfig struct {
	ShellEscape ShellEscapeConfig `json:"shell_escape,omitempty"`
}

// ShellEscapeConfig controls the shell escape (! prefix) feature in the TUI
type ShellEscapeConfig struct {
	// Enabled controls whether shell escape is available (default: true for local TUI, false for SSH)
	Enabled *bool `json:"enabled,omitempty"`

	// AllowSSH controls whether shell escape is allowed over SSH connections (default: false)
	AllowSSH bool `json:"allow_ssh,omitempty"`

	// CommandAllowlist, if non-empty, restricts shell commands to only those matching these prefixes.
	// Example: ["git ", "ls", "cat "] allows git commands, ls, and cat.
	CommandAllowlist []string `json:"command_allowlist,omitempty"`

	// CommandBlocklist blocks commands matching these prefixes. Applied after allowlist.
	// Default includes dangerous commands like "rm -rf", "sudo", "su ", etc.
	CommandBlocklist []string `json:"command_blocklist,omitempty"`

	// UseDefaultBlocklist includes the default blocklist of dangerous commands (default: true)
	UseDefaultBlocklist *bool `json:"use_default_blocklist,omitempty"`
}

// DefaultShellBlocklist returns the default list of blocked command prefixes
func DefaultShellBlocklist() []string {
	return []string{
		"rm -rf /",
		"rm -rf ~",
		"rm -rf .",
		"sudo ",
		"su ",
		"chmod 777",
		"dd if=",
		"mkfs",
		"> /dev/",
		":(){ :|:& };:", // fork bomb
		"curl | sh",
		"curl | bash",
		"wget | sh",
		"wget | bash",
	}
}

// IsShellEscapeEnabled returns whether shell escape is enabled, with defaults based on context
func (c *ShellEscapeConfig) IsShellEscapeEnabled(isSSH bool) bool {
	// SSH has shell escape disabled by default unless explicitly allowed
	if isSSH {
		return c.AllowSSH
	}

	// Local TUI has shell escape enabled by default
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// ShouldUseDefaultBlocklist returns whether to use the default blocklist
func (c *ShellEscapeConfig) ShouldUseDefaultBlocklist() bool {
	if c.UseDefaultBlocklist == nil {
		return true
	}
	return *c.UseDefaultBlocklist
}

// GetEffectiveBlocklist returns the combined blocklist (default + custom)
func (c *ShellEscapeConfig) GetEffectiveBlocklist() []string {
	var result []string
	if c.ShouldUseDefaultBlocklist() {
		result = append(result, DefaultShellBlocklist()...)
	}
	result = append(result, c.CommandBlocklist...)
	return result
}

// DebugConfig contains debugging and logging settings
type DebugConfig struct {
	LogMessageContent bool `json:"log_message_content,omitempty"` // Enable logging of message content (privacy risk!)
	VerboseLogging    bool `json:"verbose_logging,omitempty"`     // Enable verbose debug logging
}

// DatabaseConfig contains database settings
type DatabaseConfig struct {
	Path string `json:"path" cfg:"path"`
}

// SearchDatabaseConfig contains settings for the dedicated search database.
// The search database (search.db) holds FTS5 indices and optional vector storage,
// separated from the main gateway.db for independent index management.
type SearchDatabaseConfig struct {
	// Path to the search database file. If empty, derives from gateway.db path
	// (e.g., gateway.db → gateway.search.db)
	Path string `json:"path,omitempty"`

	// BeadsDir is the directory containing .beads/issues.jsonl for beads indexing.
	// Defaults to ".beads" relative to the workspace or current directory.
	BeadsDir string `json:"beads_dir,omitempty"`

	// Enabled controls whether the search database is used. Defaults to true.
	// When disabled, search falls back to grep-based search.
	Enabled *bool `json:"enabled,omitempty"`
}

// IsEnabled returns whether the search database is enabled.
// Defaults to true if not explicitly set.
func (s *SearchDatabaseConfig) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// AIConfig contains AI provider settings
type AIConfig struct {
	DefaultProvider string               `json:"default_provider"`
	Providers       []ProviderConfig     `json:"providers"`
	ModelAliases    map[string]string    `json:"model_aliases,omitempty"`
	SmartRouting    *SmartRoutingConfig  `json:"smart_routing,omitempty"`
	Compaction      *CompactionConfig    `json:"compaction,omitempty"`
	PromptCaching   PromptCachingConfig  `json:"prompt_caching,omitempty"`
}

// PromptCachingConfig holds configuration for Anthropic prompt caching
type PromptCachingConfig struct {
	Enabled                   bool `json:"enabled"`                     // Master switch for prompt caching
	ExtendedTTL               bool `json:"extended_ttl"`                // Use 1-hour TTL vs 5-minute default
	CacheTools                bool `json:"cache_tools"`                 // Cache tool definitions
	CacheSystem               bool `json:"cache_system"`                // Cache system prompt
	CacheHistory              bool `json:"cache_history"`               // Cache conversation history
	HistoryBreakpointInterval int  `json:"history_breakpoint_interval"` // Messages between history breakpoints
}

// CompactionConfig configures automatic context compaction for long sessions.
// When the context window usage exceeds the threshold, older messages are
// summarized and replaced with a compact summary to free up context space.
type CompactionConfig struct {
	// Enabled controls whether compaction is available.
	Enabled bool `json:"enabled"`

	// Threshold is the fraction of context window usage (0.0-1.0) that triggers
	// compaction. Default: 0.70 (70% of context window).
	Threshold float64 `json:"threshold,omitempty"`

	// Model is the model used for generating summaries. Default: "claude-haiku-4-5-20251001"
	// A smaller, faster model is preferred since summarization is a simpler task.
	Model string `json:"model,omitempty"`

	// RecentMessagesToKeep is the number of most recent messages to preserve
	// without summarization. Default: 10 (approximately 5 user/assistant exchanges).
	RecentMessagesToKeep int `json:"recent_messages_to_keep,omitempty"`
}

// DefaultCompactionConfig returns sensible defaults for context compaction.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Enabled:              false,
		Threshold:            0.70,
		Model:                "claude-haiku-4-5-20251001",
		RecentMessagesToKeep: 10,
	}
}

// SmartRoutingConfig holds configuration for intelligent model routing.
// Phase 1: Usage tracking foundation. Future phases add routing strategies.
type SmartRoutingConfig struct {
	Enabled          bool                       `json:"enabled"`
	TrackUsage       bool                       `json:"track_usage"`
	CostBudgetDaily  float64                    `json:"cost_budget_daily,omitempty"`
	PricingOverrides map[string]PricingOverride `json:"pricing_overrides,omitempty"`
}

// PricingOverride allows overriding default pricing for a model.
type PricingOverride struct {
	InputPerMToken  float64 `json:"input_per_m_token"`
	OutputPerMToken float64 `json:"output_per_m_token"`
}

// DefaultModelAliases returns the built-in model alias map. This is the single
// source of truth for alias defaults used by config, gateway, and prompt builder.
func DefaultModelAliases() map[string]string {
	return map[string]string{
		"haiku":   "claude-haiku-4-5-20251001",
		"sonnet":  "claude-sonnet-4-6",
		"opus":    "claude-opus-4-6",
		"default": "claude-haiku-4-5-20251001",
	}
}

// ProviderConfig contains settings for a specific AI provider
type ProviderConfig struct {
	Name          string            `json:"name"`
	Type          string            `json:"type"`               // "anthropic", "openai", "ollama", "claude-code", etc.
	APIKey        string            `json:"api_key,omitempty" cfg:"env"`  // Legacy API key
	BaseURL       string            `json:"base_url,omitempty" cfg:"env"` // Custom API base URL (for local/compatible servers)
	Model         string            `json:"model"`
	Auth          *AuthConfig       `json:"auth,omitempty"`           // OAuth configuration
	ContextWindow int               `json:"context_window,omitempty"` // Override context window size (tokens); 0 = auto-detect from model name
	ClaudeCode    *ClaudeCodeConfig `json:"claude_code,omitempty"`    // Settings for type="claude-code"
}

// ClaudeCodeConfig holds settings for the claude-code provider type.
// When Type is "claude-code", these fields configure how Conduit
// shells out to the Claude Code CLI.
type ClaudeCodeConfig struct {
	ClaudePath     string   `json:"claude_path"`      // Path to claude binary; default: "claude"
	MCPPort        int      `json:"mcp_port"`         // Conduit's MCP server port; default: 18790
	AllowedTools   []string `json:"allowed_tools"`    // Claude Code native tools to enable
	PermissionMode string   `json:"permission_mode"`  // default: "acceptEdits"
	MaxTurns       int      `json:"max_turns"`        // default: 25
	TimeoutSeconds int      `json:"timeout_seconds"`  // default: 300
	WorkingDir     string   `json:"working_dir"`      // where claude -p runs
}

// DefaultClaudeCodeConfig returns sensible defaults for the claude-code provider.
func DefaultClaudeCodeConfig() ClaudeCodeConfig {
	return ClaudeCodeConfig{
		ClaudePath:     "claude",
		MCPPort:        18790,
		AllowedTools:   []string{"Read", "Edit", "Bash", "Glob", "Grep", "Write"},
		PermissionMode: "acceptEdits",
		MaxTurns:       25,
		TimeoutSeconds: 300,
	}
}

// ClaudeCodeOrDefault returns the ClaudeCode config, falling back to defaults
// for any zero-valued fields.
func (p ProviderConfig) ClaudeCodeOrDefault() ClaudeCodeConfig {
	if p.ClaudeCode != nil {
		cfg := *p.ClaudeCode
		if cfg.ClaudePath == "" {
			cfg.ClaudePath = "claude"
		}
		if cfg.MCPPort == 0 {
			cfg.MCPPort = 18790
		}
		if cfg.PermissionMode == "" {
			cfg.PermissionMode = "acceptEdits"
		}
		if cfg.MaxTurns == 0 {
			cfg.MaxTurns = 25
		}
		if cfg.TimeoutSeconds == 0 {
			cfg.TimeoutSeconds = 300
		}
		if len(cfg.AllowedTools) == 0 {
			cfg.AllowedTools = []string{"Read", "Edit", "Bash", "Glob", "Grep", "Write"}
		}
		return cfg
	}
	return DefaultClaudeCodeConfig()
}

// AuthConfig contains OAuth authentication settings
type AuthConfig struct {
	Type         string `json:"type"` // "oauth" or "api_key"
	OAuthToken   string `json:"oauth_token,omitempty" cfg:"env"`
	RefreshToken string `json:"refresh_token,omitempty" cfg:"env"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ClientID     string `json:"client_id,omitempty" cfg:"env"`
	ClientSecret string `json:"client_secret,omitempty" cfg:"env"`
}

// AgentConfig contains agent system settings
type AgentConfig struct {
	Name          string              `json:"name"`
	Personality   string              `json:"personality"`
	Email         AgentEmail          `json:"email,omitempty"`
	Identity      AgentIdentity       `json:"identity"`
	Capabilities  AgentCapabilities   `json:"capabilities"`
	History       HistoryConfig       `json:"history,omitempty"`
	PromptScaling PromptScalingConfig `json:"prompt_scaling,omitempty"`
}

// HistoryConfig controls conversation history retrieval
type HistoryConfig struct {
	// MaxTokens is the target token budget for conversation history.
	// Messages are retrieved newest-first until this budget is reached.
	// Default: 16000 tokens (~64KB of text)
	MaxTokens int `json:"max_tokens,omitempty"`

	// MinMessages ensures at least this many recent messages are included,
	// even if they exceed the token budget. Default: 4
	MinMessages int `json:"min_messages,omitempty"`

	// MaxMessages is an absolute cap on messages regardless of token budget.
	// Default: 100 (prevents runaway in edge cases)
	MaxMessages int `json:"max_messages,omitempty"`

	// CharsPerToken is the estimated characters per token for budgeting.
	// Default: 4 (reasonable for English text)
	CharsPerToken int `json:"chars_per_token,omitempty"`
}

// DefaultHistoryConfig returns sensible defaults for history retrieval
func DefaultHistoryConfig() HistoryConfig {
	return HistoryConfig{
		MaxTokens:     16000,
		MinMessages:   4,
		MaxMessages:   100,
		CharsPerToken: 4,
	}
}

// AgentIdentity configures agent identity based on auth type
type AgentIdentity struct {
	OAuthIdentity       string   `json:"oauth_identity"`
	APIKeyIdentity      string   `json:"api_key_identity"`
	OperatingPrinciples []string `json:"operating_principles,omitempty"` // Override default operating principles
}

// AgentEmail configures the agent's email identity for sending/receiving email
type AgentEmail struct {
	Address     string   `json:"address"`
	Aliases     []string `json:"aliases,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
}

// AgentCapabilities defines what the agent can do
type AgentCapabilities struct {
	MemoryRecall      bool `json:"memory_recall"`
	ToolChaining      bool `json:"tool_chaining"`
	SkillsIntegration bool `json:"skills_integration"`
	Heartbeats        bool `json:"heartbeats"`
	SilentReplies     bool `json:"silent_replies"`
}

// PromptScalingConfig controls dynamic system prompt scaling for small-context models
type PromptScalingConfig struct {
	// LargeContextThreshold is the context window size (tokens) above which
	// all prompt sections are included without budget constraints. Default: 128000
	LargeContextThreshold int `json:"large_context_threshold,omitempty"`

	// PromptBudgetPercent is the percentage of context window allocated to
	// the system prompt for small-context models. Default: 15
	PromptBudgetPercent int `json:"prompt_budget_percent,omitempty"`

	// CharsPerToken is the estimated characters per token for budget math.
	// Default: 4 (reasonable for English text)
	CharsPerToken int `json:"chars_per_token,omitempty"`
}

// DefaultPromptScalingConfig returns sensible defaults for prompt scaling
func DefaultPromptScalingConfig() PromptScalingConfig {
	return PromptScalingConfig{
		LargeContextThreshold: 128000,
		PromptBudgetPercent:   15,
		CharsPerToken:         4,
	}
}

// ToolsConfig contains tool execution settings
type ToolsConfig struct {
	EnabledTools       []string                          `json:"enabled_tools"`
	MaxToolChains      int                               `json:"max_tool_chains,omitempty"`       // Maximum tool calls in a chain before stopping
	MaxToolResultChars int                               `json:"max_tool_result_chars,omitempty"` // Maximum chars in tool result content (default 8192)
	Sandbox            SandboxConfig                     `json:"sandbox"`
	Services           map[string]map[string]interface{} `json:"services,omitempty"`
}

// SandboxConfig contains sandboxing settings for tool execution
type SandboxConfig struct {
	WorkspaceDir    string   `json:"workspace_dir"`
	AllowedPaths    []string `json:"allowed_paths"`
	CommandDenylist []string `json:"command_denylist,omitempty"`
}

// ChannelConfig contains settings for channel adapters
type ChannelConfig struct {
	Name    string                 `json:"name"`
	Type    string                 `json:"type"`
	Enabled bool                   `json:"enabled"`
	Config  map[string]interface{} `json:"config"`
}

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

// DefaultWorkspaceSummaryConfig returns sensible defaults
func DefaultWorkspaceSummaryConfig() WorkspaceSummaryConfig {
	fallbackTrue := true
	return WorkspaceSummaryConfig{
		Enabled:            false,
		Model:              "claude-haiku-4-5-20251001",
		TargetRatio:        0.25,
		CacheDir:           ".summaries",
		CacheTTLHours:      168,
		FallbackToTruncate: &fallbackTrue,
		FileConfigs: map[string]SummaryFileConfig{
			"SOUL.md":   {Ratio: 0.40, PreserveKeys: []string{"personality", "tone", "voice"}},
			"USER.md":   {Ratio: 0.30, PreserveKeys: []string{"preferences", "constraints"}},
			"AGENTS.md": {Ratio: 0.25, PreserveKeys: []string{"rules", "restrictions"}},
			"TOOLS.md":  {Ratio: 0.20, PreserveKeys: []string{"usage", "commands"}},
		},
	}
}

// WorkspaceFilesConfig defines which files to load
type WorkspaceFilesConfig struct {
	Core   []string              `json:"core"`
	Memory WorkspaceMemoryConfig `json:"memory"`
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

// RateLimitingConfig contains rate limiting settings
type RateLimitingConfig struct {
	Enabled                bool                `json:"enabled"`
	Anonymous              RateLimitTierConfig `json:"anonymous"`
	Authenticated          RateLimitTierConfig `json:"authenticated"`
	CleanupIntervalSeconds int                 `json:"cleanupIntervalSeconds"`
	// TrustProxy controls how X-Forwarded-For headers are handled for IP extraction.
	// When false (default), only the direct connection IP (RemoteAddr) is used.
	// When true, the rightmost non-private IP from X-Forwarded-For is used.
	// Only enable this when running behind a trusted reverse proxy (nginx, Cloudflare, etc).
	TrustProxy bool `json:"trustProxy"`
}

// RateLimitTierConfig defines rate limiting for a specific tier (anonymous vs authenticated)
type RateLimitTierConfig struct {
	WindowSeconds int `json:"windowSeconds"`
	MaxRequests   int `json:"maxRequests"`
}

// DiagnosticsConfig contains settings for diagnostic endpoints security
type DiagnosticsConfig struct {
	// RequireAuth controls whether diagnostic endpoints require authentication.
	// When true (default), /metrics, /diagnostics, /prometheus require auth.
	// The /health endpoint has its own HealthPublic setting.
	RequireAuth bool `json:"require_auth"`

	// HealthPublic controls whether /health is accessible without authentication.
	// Default: true (public) for load balancer compatibility.
	// Set to false to require auth for /health as well.
	HealthPublic *bool `json:"health_public,omitempty"`
}

// IsHealthPublic returns whether the /health endpoint should be public.
// Defaults to true for load balancer compatibility.
func (d *DiagnosticsConfig) IsHealthPublic() bool {
	if d.HealthPublic == nil {
		return true
	}
	return *d.HealthPublic
}

// DefaultDiagnosticsConfig returns secure defaults for diagnostics config
func DefaultDiagnosticsConfig() DiagnosticsConfig {
	return DiagnosticsConfig{
		RequireAuth:  true, // Require auth for /metrics, /diagnostics, /prometheus
		HealthPublic: nil,  // nil means default to true (public /health)
	}
}

// SkillsConfig is imported from skills package

// Default returns a default configuration
func Default() *Config {
	return &Config{
		Port:      18789,
		WebSocket: DefaultWebSocketConfig(),
		Database: DatabaseConfig{
			Path: "gateway.db",
		},
		AI: AIConfig{
			DefaultProvider: "anthropic",
			Providers: []ProviderConfig{
				{
					Name:   "anthropic",
					Type:   "anthropic",
					APIKey: "${ANTHROPIC_API_KEY}", // Fallback
					Model:  "claude-3-5-sonnet-20241022",
					Auth: &AuthConfig{
						Type:       "oauth",
						OAuthToken: "${ANTHROPIC_OAUTH_TOKEN}",
					},
				},
				{
					Name:   "openai",
					Type:   "openai",
					APIKey: "${OPENAI_API_KEY}",
					Model:  "gpt-4",
				},
			},
			ModelAliases: DefaultModelAliases(),
		},
		Agent: AgentConfig{
			Name:        "Conduit",
			Personality: "conduit",
			Identity: AgentIdentity{
				OAuthIdentity:  "You are Claude Code, Anthropic's official CLI for Claude.",
				APIKeyIdentity: "You are Conduit, an AI assistant powered by Claude.",
			},
			Capabilities: AgentCapabilities{
				MemoryRecall:      true,
				ToolChaining:      true,
				SkillsIntegration: true,
				Heartbeats:        true,
				SilentReplies:     true,
			},
			History:       DefaultHistoryConfig(),
			PromptScaling: DefaultPromptScalingConfig(),
		},
		Tools: ToolsConfig{
			EnabledTools:  []string{"read", "write", "exec", "web_search"},
			MaxToolChains: 25, // Allow complex workflows, configurable per deployment
			Sandbox: SandboxConfig{
				WorkspaceDir: "./workspace",
				AllowedPaths: []string{"./workspace", "/tmp"},
			},
		},
		Debug: DebugConfig{
			LogMessageContent: false, // Privacy-safe by default
			VerboseLogging:    false,
		},
		RateLimiting: RateLimitingConfig{
			Enabled: true,
			Anonymous: RateLimitTierConfig{
				WindowSeconds: 60,  // 1 minute window
				MaxRequests:   100, // 100 requests per minute for anonymous (per IP)
			},
			Authenticated: RateLimitTierConfig{
				WindowSeconds: 60,   // 1 minute window
				MaxRequests:   1000, // 1000 requests per minute for authenticated (per client)
			},
			CleanupIntervalSeconds: 300, // Clean up expired buckets every 5 minutes
		},
		Diagnostics:    DefaultDiagnosticsConfig(),
		Heartbeat:      DefaultHeartbeatConfig(),
		AgentHeartbeat: DefaultAgentHeartbeatConfig(),
		RemoteSSH:      DefaultRemoteSSHConfig(),
		MQTT:           DefaultMQTTConfig(),
		Kubernetes:     DefaultKubernetesConfig(),
		PagerDuty:      DefaultPagerDutyConfig(),
		Datadog:        DefaultDatadogConfig(),
		Logging:        DefaultLoggingConfig(),
		Brain:          DefaultBrainConfig(),
		Channels: []ChannelConfig{
			{
				Name:    "telegram",
				Type:    "telegram",
				Enabled: false,
				Config: map[string]interface{}{
					"bot_token": "${TELEGRAM_BOT_TOKEN}",
				},
			},
			{
				Name:    "whatsapp",
				Type:    "whatsapp",
				Enabled: false,
				Config: map[string]interface{}{
					"session_dir": "./sessions/whatsapp",
				},
			},
		},
	}
}

// Load loads configuration from a file
func Load(path string) (*Config, error) {
	// Check if file exists, create default if not
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := Default()
		if err := cfg.Save(path); err != nil {
			return nil, fmt.Errorf("failed to save default config: %w", err)
		}
		fmt.Printf("Created default configuration at %s\n", path)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Expand tilde in path fields before anything else so that
	// secrets_file can reference ~/... paths.
	cfg.expandTilde()

	// Load secrets file (KEY=VALUE) into the environment before
	// expanding ${ENV_VAR} placeholders in the config.
	if err := cfg.loadSecretsFile(); err != nil {
		return nil, fmt.Errorf("failed to load secrets file: %w", err)
	}

	// Expand environment variables
	if err := cfg.expandEnvVars(); err != nil {
		return nil, fmt.Errorf("failed to expand environment variables: %w", err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return &cfg, nil
}

// Save saves the configuration to a file
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// expandEnvVars expands ${ENV_VAR} placeholders in configuration values.
// Struct fields tagged with cfg:"env" are expanded automatically via reflection.
// map[string]interface{} fields (channels, tool services) are expanded manually
// since map values can't carry struct tags.
func (c *Config) expandEnvVars() error {
	c.expandEnvTagged()
	c.expandEnvMaps()
	return nil
}

// Validate validates the entire configuration
func (c *Config) Validate() error {
	// Validate heartbeat configuration
	if err := c.Heartbeat.Validate(); err != nil {
		return fmt.Errorf("invalid heartbeat configuration: %w", err)
	}

	// Validate agent heartbeat configuration
	if err := c.AgentHeartbeat.Validate(); err != nil {
		return fmt.Errorf("invalid agent heartbeat configuration: %w", err)
	}

	// Validate rate limiting configuration
	if c.RateLimiting.Enabled {
		if c.RateLimiting.Anonymous.WindowSeconds <= 0 || c.RateLimiting.Anonymous.MaxRequests <= 0 {
			return fmt.Errorf("invalid anonymous rate limiting configuration")
		}
		if c.RateLimiting.Authenticated.WindowSeconds <= 0 || c.RateLimiting.Authenticated.MaxRequests <= 0 {
			return fmt.Errorf("invalid authenticated rate limiting configuration")
		}
	}

	// Validate tools configuration
	if c.Tools.MaxToolChains <= 0 {
		return fmt.Errorf("max_tool_chains must be greater than 0")
	}
	if c.Tools.MaxToolChains < 10 {
		fmt.Printf("WARNING: max_tool_chains is set to %d, which may be too low for complex tasks. Consider using 25 or higher.\n", c.Tools.MaxToolChains)
	}

	// Validate timezone if set
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			return fmt.Errorf("invalid timezone '%s': %w", c.Timezone, err)
		}
	}

	// Validate remote SSH configuration
	if err := c.RemoteSSH.Validate(); err != nil {
		return fmt.Errorf("invalid remote SSH configuration: %w", err)
	}

	// Validate MQTT configuration
	if err := c.MQTT.Validate(); err != nil {
		return fmt.Errorf("invalid MQTT configuration: %w", err)
	}

	// Validate Kubernetes configuration
	if err := c.Kubernetes.Validate(); err != nil {
		return fmt.Errorf("invalid kubernetes configuration: %w", err)
	}

	// Validate PagerDuty configuration
	if err := c.PagerDuty.Validate(); err != nil {
		return fmt.Errorf("invalid PagerDuty configuration: %w", err)
	}

	// Validate Datadog configuration
	if err := c.Datadog.Validate(); err != nil {
		return fmt.Errorf("invalid Datadog configuration: %w", err)
	}

	// Validate Brain configuration
	if err := c.Brain.Validate(); err != nil {
		return fmt.Errorf("invalid brain configuration: %w", err)
	}

	return nil
}

// GetLocation returns the configured timezone as a *time.Location.
// Falls back to AgentHeartbeat.Timezone if the top-level timezone is empty,
// then to time.Local.
func (c *Config) GetLocation() *time.Location {
	tz := c.Timezone
	if tz == "" {
		tz = c.AgentHeartbeat.Timezone
	}
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

// expandTilde replaces a leading "~/" with the user's home directory in
// all string fields tagged with cfg:"path". Called before env-var expansion
// so that both "~/foo" and "${SOME_PATH}" work.
func (c *Config) expandTilde() {
	c.expandTildeTagged()
}

// loadSecretsFile reads a KEY=VALUE file into the process environment.
// Blank lines and lines starting with '#' are ignored.
// Existing environment variables are NOT overridden (shell/systemd wins).
// If SecretsFile is empty or the file doesn't exist, this is a no-op.
func (c *Config) loadSecretsFile() error {
	if c.SecretsFile == "" {
		return nil
	}

	f, err := os.Open(c.SecretsFile)
	if os.IsNotExist(err) {
		return nil // missing file is fine
	}
	if err != nil {
		return fmt.Errorf("cannot open secrets file %s: %w", c.SecretsFile, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		// Strip optional surrounding quotes from value
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		// Don't override existing env vars
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}
