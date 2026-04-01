package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"conduit/internal/skills"
)

// Config represents the gateway configuration
type Config struct {
	Port           int                  `json:"port"`
	Timezone       string               `json:"timezone,omitempty"`
	DataDir        string               `json:"data_dir,omitempty"`
	SecretsFile    string               `json:"secrets_file,omitempty"`
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
	Auth           AuthTokenConfig      `json:"auth,omitempty"`
}

// AuthTokenConfig holds configuration for the token authentication system
type AuthTokenConfig struct {
	// TokenSecret is the HMAC key used for hashing tokens (hex-encoded, 32 bytes).
	// If empty, a random key is generated at startup (tokens won't survive restarts).
	// Supports ${ENV_VAR} expansion.
	TokenSecret string `json:"token_secret,omitempty"`
}

// VectorConfig holds configuration for the optional vector/semantic search service.
type VectorConfig struct {
	Enabled       bool               `json:"enabled"`
	Path          string             `json:"path,omitempty"`           // Path to vector DB file (derived from gateway DB if empty)
	ChunkSize     int                `json:"chunk_size,omitempty"`     // Max tokens per chunk (default 500)
	EmbedDims     int                `json:"embed_dims,omitempty"`     // Embedding dimensions (default 4096 for TF-IDF, 1536 for OpenAI)
	EmbedProvider string             `json:"embed_provider,omitempty"` // "tfidf" (default), "openai"
	OpenAI        *OpenAIEmbedConfig `json:"openai,omitempty"`
}

// OpenAIEmbedConfig holds configuration for OpenAI embedding provider.
type OpenAIEmbedConfig struct {
	APIKey string `json:"api_key,omitempty"` // Supports ${ENV_VAR} expansion
	Model  string `json:"model,omitempty"`   // Default: "text-embedding-3-small"
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

// SSHServerConfig holds configuration for the integrated SSH server
type SSHServerConfig struct {
	Enabled            bool   `json:"enabled"`
	ListenAddr         string `json:"listen_addr,omitempty"`
	HostKeyPath        string `json:"host_key_path,omitempty"`
	AuthorizedKeysPath string `json:"authorized_keys_path,omitempty"`
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
	Path string `json:"path"`
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
	DefaultProvider string              `json:"default_provider"`
	Providers       []ProviderConfig    `json:"providers"`
	ModelAliases    map[string]string   `json:"model_aliases,omitempty"`
	SmartRouting    *SmartRoutingConfig `json:"smart_routing,omitempty"`
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
	Name          string      `json:"name"`
	Type          string      `json:"type"`                     // "anthropic", "openai", "ollama", etc.
	APIKey        string      `json:"api_key,omitempty"`        // Legacy API key
	BaseURL       string      `json:"base_url,omitempty"`       // Custom API base URL (for local/compatible servers)
	Model         string      `json:"model"`
	Auth          *AuthConfig `json:"auth,omitempty"`           // OAuth configuration
	ContextWindow int         `json:"context_window,omitempty"` // Override context window size (tokens); 0 = auto-detect from model name
}

// AuthConfig contains OAuth authentication settings
type AuthConfig struct {
	Type         string `json:"type"` // "oauth" or "api_key"
	OAuthToken   string `json:"oauth_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
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

// expandEnvVars expands environment variables in configuration values
func (c *Config) expandEnvVars() error {
	// Expand top-level path fields
	c.DataDir = os.ExpandEnv(c.DataDir)
	c.SecretsFile = os.ExpandEnv(c.SecretsFile)

	// Expand AI provider settings
	for i := range c.AI.Providers {
		c.AI.Providers[i].APIKey = os.ExpandEnv(c.AI.Providers[i].APIKey)
		c.AI.Providers[i].BaseURL = os.ExpandEnv(c.AI.Providers[i].BaseURL)

		// Expand OAuth configuration if present
		if c.AI.Providers[i].Auth != nil {
			c.AI.Providers[i].Auth.OAuthToken = os.ExpandEnv(c.AI.Providers[i].Auth.OAuthToken)
			c.AI.Providers[i].Auth.RefreshToken = os.ExpandEnv(c.AI.Providers[i].Auth.RefreshToken)
			c.AI.Providers[i].Auth.ClientID = os.ExpandEnv(c.AI.Providers[i].Auth.ClientID)
			c.AI.Providers[i].Auth.ClientSecret = os.ExpandEnv(c.AI.Providers[i].Auth.ClientSecret)
		}
	}

	// Expand channel configuration
	for i := range c.Channels {
		for key, value := range c.Channels[i].Config {
			if strVal, ok := value.(string); ok {
				c.Channels[i].Config[key] = os.ExpandEnv(strVal)
			}
		}
	}

	// Expand tools services configuration
	for _, serviceConfig := range c.Tools.Services {
		for key, value := range serviceConfig {
			if strVal, ok := value.(string); ok {
				serviceConfig[key] = os.ExpandEnv(strVal)
			}
		}
	}

	// Expand vector/embedding configuration
	if c.Vector.OpenAI != nil {
		c.Vector.OpenAI.APIKey = os.ExpandEnv(c.Vector.OpenAI.APIKey)
	}

	// Expand MQTT configuration
	c.MQTT.BrokerURL = os.ExpandEnv(c.MQTT.BrokerURL)
	c.MQTT.Username = os.ExpandEnv(c.MQTT.Username)
	c.MQTT.Password = os.ExpandEnv(c.MQTT.Password)

	// Expand auth configuration
	c.Auth.TokenSecret = os.ExpandEnv(c.Auth.TokenSecret)

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
// path-valued config fields. Called before env-var expansion so that
// both "~/foo" and "${SOME_PATH}" work.
func (c *Config) expandTilde() {
	home, err := os.UserHomeDir()
	if err != nil {
		return // can't expand, leave as-is
	}
	expand := func(p string) string {
		if p == "~" {
			return home
		}
		if strings.HasPrefix(p, "~/") {
			return filepath.Join(home, p[2:])
		}
		return p
	}

	c.DataDir = expand(c.DataDir)
	c.SecretsFile = expand(c.SecretsFile)
	c.Database.Path = expand(c.Database.Path)
	c.SSH.HostKeyPath = expand(c.SSH.HostKeyPath)
	c.SSH.AuthorizedKeysPath = expand(c.SSH.AuthorizedKeysPath)

	// Expand tilde in RemoteSSH config
	c.RemoteSSH.Audit.LogPath = expand(c.RemoteSSH.Audit.LogPath)
	c.RemoteSSH.Pool.KnownHostsFile = expand(c.RemoteSSH.Pool.KnownHostsFile)
	c.RemoteSSH.Defaults.IdentityFile = expand(c.RemoteSSH.Defaults.IdentityFile)
	for i := range c.RemoteSSH.Hosts {
		c.RemoteSSH.Hosts[i].IdentityFile = expand(c.RemoteSSH.Hosts[i].IdentityFile)
	}
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
