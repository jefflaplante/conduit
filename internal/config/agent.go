package config

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

	// LowWaterFraction is the share of MaxTokens / MaxMessages that history
	// is cut down to once it exceeds the budget. Cutting in large steps keeps
	// the history prefix byte-stable between trims so provider prompt-cache
	// breakpoints keep hitting (conduit-31jg.63). Default (0): 0.75.
	LowWaterFraction float64 `json:"low_water_fraction,omitempty"`
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

	// LowWaterFraction is the share of MaxTokens / MaxMessages that history
	// is cut down to once it exceeds the budget. Cutting in large steps keeps
	// the history prefix byte-stable between trims so provider prompt-cache
	// breakpoints keep hitting (conduit-31jg.63). Default (0): 0.75.
	LowWaterFraction float64 `json:"low_water_fraction,omitempty"`
}

// DefaultPromptScalingConfig returns sensible defaults for prompt scaling
func DefaultPromptScalingConfig() PromptScalingConfig {
	return PromptScalingConfig{
		LargeContextThreshold: 128000,
		PromptBudgetPercent:   15,
		CharsPerToken:         4,
	}
}
