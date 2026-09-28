package agent

import (
	"context"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// AgentSystem defines the interface for pluggable agent personalities
type AgentSystem interface {
	// Name returns the agent system name
	Name() string

	// BuildSystemPrompt builds the system prompt for a given session
	BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]ai.SystemBlock, error)

	// GetToolDefinitions returns the available tool definitions.
	// When session is non-nil and contains a "skill_filter" context key,
	// only tools from the named skills are returned.
	GetToolDefinitions(session *sessions.Session) []ai.Tool

	// SetTools updates the agent's tool definitions (for deferred initialization)
	SetTools(tools []ai.Tool)

	// ProcessResponse processes an AI response before sending
	ProcessResponse(ctx context.Context, response *ai.GenerateResponse) (*ProcessedResponse, error)
}

// SystemBlock is defined in ai package to avoid type conflicts

// ProcessedResponse represents a processed AI response
type ProcessedResponse struct {
	Content   string        `json:"content"`
	ToolCalls []ai.ToolCall `json:"tool_calls,omitempty"`
	Actions   []AgentAction `json:"actions,omitempty"`
	Silent    bool          `json:"silent,omitempty"`   // Don't send response (HEARTBEAT_OK, NO_REPLY)
	Modified  bool          `json:"modified,omitempty"` // Response was modified by agent
}

// AgentAction represents an action the agent wants to take
type AgentAction struct {
	Type string      `json:"type"` // "tool_call", "memory_update", etc.
	Data interface{} `json:"data"`
}

// IdentityConfig configures the agent identity based on auth type
type IdentityConfig struct {
	OAuthIdentity       string   `json:"oauth_identity"`                 // Identity when using OAuth (Claude Code)
	APIKeyIdentity      string   `json:"api_key_identity"`               // Identity when using API key
	OperatingPrinciples []string `json:"operating_principles,omitempty"` // Override default operating principles
}

// AgentCapabilities defines what the agent can do
type AgentCapabilities struct {
	MemoryRecall      bool `json:"memory_recall"`
	ToolChaining      bool `json:"tool_chaining"`
	SkillsIntegration bool `json:"skills_integration"`
	Heartbeats        bool `json:"heartbeats"`
	SilentReplies     bool `json:"silent_replies"`
}

// AgentConfig holds the complete agent configuration
type AgentConfig struct {
	Name           string                     `json:"name"`
	Personality    string                     `json:"personality"`
	Email          config.AgentEmail          `json:"email,omitempty"`
	Identity       IdentityConfig             `json:"identity"`
	Capabilities   AgentCapabilities          `json:"capabilities"`
	PromptScaling  config.PromptScalingConfig `json:"prompt_scaling,omitempty"`
	Timezone       string                     `json:"timezone,omitempty"`
	RuntimeChannel string                     `json:"runtime_channel,omitempty"` // Active channel (derived from config)
	// QuietHours is the configured quiet window (agent_heartbeat) used for
	// the prompt's "quiet hours" hint; nil = legacy 23:00-08:00. conduit-31jg.60
	QuietHours *config.AgentHeartbeatConfig `json:"-"`
}
