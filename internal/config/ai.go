package config

// STTConfig holds configuration for speech-to-text transcription.
type STTConfig struct {
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider,omitempty"` // "whisper" (default)
	APIKey   string `json:"api_key,omitempty" cfg:"env"`
	Model    string `json:"model,omitempty"` // Default: "whisper-1"
}

// AIConfig contains AI provider settings
type AIConfig struct {
	DefaultProvider      string              `json:"default_provider"`
	Providers            []ProviderConfig    `json:"providers"`
	ModelAliases         map[string]string   `json:"model_aliases,omitempty"`
	SubagentDefaultModel string              `json:"subagent_default_model,omitempty"` // Default model for sub-agents spawned without an explicit model (overrides gateway default when set)
	SmartRouting         *SmartRoutingConfig `json:"smart_routing,omitempty"`          // Deprecated: ignored except pricing_overrides (conduit-2avx)
	Compaction           *CompactionConfig   `json:"compaction,omitempty"`
	PromptCaching        PromptCachingConfig `json:"prompt_caching,omitempty"`
	// MaxTokens caps generated output per LLM round trip (bd-1k3o). 0 = default 4000.
	// Long report-writing turns (e.g., sub-agent deliverables) benefit from a
	// higher cap; the finish_reason parser now makes "length" truncation
	// recoverable, but avoiding it outright is cheaper than continuing.
	MaxTokens int `json:"max_tokens,omitempty"`
	// PricingOverrides maps model IDs (bare "glm-5.3" or provider-prefixed
	// "openrouter/deepseek/deepseek-v4.1-flash") to per-MTok prices; they win
	// over the built-in matrix. conduit-31jg.57 (was silently ignored).
	PricingOverrides map[string]PricingOverride `json:"pricing_overrides,omitempty"`
	// CallLog is the persistent per-provider-call JSONL log (conduit-2lzv).
	CallLog CallLogConfig `json:"call_log,omitempty"`
}

// PromptCachingConfig holds configuration for Anthropic prompt caching.
// Canonical home: config package (conduit-3dru). Providers inherit the
// ai.prompt_caching block unless a provider-level block overrides it.
type PromptCachingConfig struct {
	Enabled                   bool `json:"enabled"`                     // Master switch for prompt caching
	ExtendedTTL               bool `json:"extended_ttl"`                // Use 1-hour TTL (2x write cost) vs 5-minute default
	CacheTools                bool `json:"cache_tools"`                 // Cache tool definitions
	CacheSystem               bool `json:"cache_system"`                // Cache system prompt
	CacheHistory              bool `json:"cache_history"`               // Cache conversation history
	HistoryBreakpointInterval int  `json:"history_breakpoint_interval"` // Messages between history breakpoints
}

// DefaultPromptCachingConfig returns sensible defaults for prompt caching.
// Breakpoint interval 6 mirrors the historical >5 messages heuristic.
func DefaultPromptCachingConfig() PromptCachingConfig {
	return PromptCachingConfig{
		Enabled:                   true,
		ExtendedTTL:               false, // 5-minute default TTL
		CacheTools:                true,
		CacheSystem:               true,
		CacheHistory:              true,
		HistoryBreakpointInterval: 6,
	}
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

// SmartRoutingConfig is the deprecated ai.smart_routing block.
//
// Deprecated: smart routing was removed (conduit-2avx); it had never been
// wired, so it was a no-op. The block is still parsed so existing configs
// load (with a one-time warning) and round-trip through Save unchanged.
// Enabled, TrackUsage and CostBudgetDaily are ignored; usage is always
// tracked, and the budget hard-stop is tracked in conduit-23aw.
type SmartRoutingConfig struct {
	Enabled         bool    `json:"enabled"`
	TrackUsage      bool    `json:"track_usage"`
	CostBudgetDaily float64 `json:"cost_budget_daily,omitempty"`
	// Deprecated: use ai.pricing_overrides. Still honored — merged into
	// AIConfig.PricingOverrides with a warning (conduit-31jg.57).
	PricingOverrides map[string]PricingOverride `json:"pricing_overrides,omitempty"`
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

// ThinkingConfig controls extended-thinking behavior on OpenAI-compatible
// providers that expose it (conduit-15gt). Probed live 2026-09-15 against
// z.ai: "disabled" fully suppresses reasoning; "enabled" with budget_tokens
// bounds the reasoning phase (z.ai honors the budget as guidance, not a hard
// cap — observed 50 → 125 reasoning tokens; visible-output headroom is what
// actually prevents empty responses).
type ThinkingConfig struct {
	Type         string `json:"type"`          // "enabled" | "disabled" (required)
	BudgetTokens int    `json:"budget_tokens"` // with type=enabled: reasoning token budget; 0 = provider default
}

// ProviderConfig contains settings for a specific AI provider
type ProviderConfig struct {
	Name           string               `json:"name"`
	Type           string               `json:"type"`                         // "anthropic", "openai", "ollama", "claude-code", etc.
	APIKey         string               `json:"api_key,omitempty" cfg:"env"`  // Legacy API key
	BaseURL        string               `json:"base_url,omitempty" cfg:"env"` // Custom API base URL (for local/compatible servers)
	Model          string               `json:"model"`
	Auth           *AuthConfig          `json:"auth,omitempty"`            // OAuth configuration
	ContextWindow  int                  `json:"context_window,omitempty"`  // Override context window size (tokens); 0 = auto-detect from model name
	FallbackModel  string               `json:"fallback_model,omitempty"`  // Fallback model for quota/auth errors (default: "z-ai/glm-5.3")
	Thinking       *ThinkingConfig      `json:"thinking,omitempty"`        // conduit-15gt: reasoning control for OpenAI-compatible providers (z.ai etc.)
	PromptCaching  *PromptCachingConfig `json:"prompt_caching,omitempty"`  // conduit-3dru: Anthropic prompt caching; nil = inherit ai.prompt_caching, which defaults to enabled
	TimeoutSeconds int                  `json:"timeout_seconds,omitempty"` // HTTP client timeout in seconds (default: 300); bd-29i
	ClaudeCode     *ClaudeCodeConfig    `json:"claude_code,omitempty"`     // Settings for type="claude-code"

	// MaxConcurrent caps the provider calls in flight at once across all of
	// this provider's models (conduit-38cz). 0/omitted = unlimited. A call
	// over the cap waits for a slot (bounded by its turn deadline).
	MaxConcurrent int `json:"max_concurrent,omitempty"`
	// ModelMaxConcurrent caps calls per model, keyed by model name (matched
	// case-insensitively, with or without a "provider/" prefix). A model
	// listed here gets its own pool INSTEAD of the provider-wide
	// max_concurrent; 0 = explicitly unlimited (disables a built-in default,
	// e.g. z.ai's glm-5.3=5 / glm-5.3-flash=50). conduit-38cz.
	ModelMaxConcurrent map[string]int `json:"model_max_concurrent,omitempty"`
}

// ClaudeCodeConfig holds settings for the claude-code provider type.
// When Type is "claude-code", these fields configure how Conduit
// shells out to the Claude Code CLI.
type ClaudeCodeConfig struct {
	ClaudePath     string   `json:"claude_path"`     // Path to claude binary; default: "claude"
	MCPPort        int      `json:"mcp_port"`        // Conduit's MCP server port; default: 18790
	AllowedTools   []string `json:"allowed_tools"`   // Claude Code native tools to enable
	PermissionMode string   `json:"permission_mode"` // default: "acceptEdits"
	MaxTurns       int      `json:"max_turns"`       // default: 25
	TimeoutSeconds int      `json:"timeout_seconds"` // default: 300
	WorkingDir     string   `json:"working_dir"`     // where claude -p runs
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
