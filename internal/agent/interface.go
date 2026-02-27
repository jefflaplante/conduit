package agent

import (
	"context"

	"conduit/internal/ai"
	"conduit/internal/sessions"
)

// AgentSystem defines the interface for pluggable agent personalities
type AgentSystem interface {
	// Name returns the agent system name
	Name() string

	// BuildSystemPrompt builds the system prompt for a given session
	BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]ai.SystemBlock, error)

	// GetToolDefinitions returns the available tool definitions
	GetToolDefinitions() []ai.Tool

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
	OAuthIdentity  string `json:"oauth_identity"`   // Identity when using OAuth (Claude Code)
	APIKeyIdentity string `json:"api_key_identity"` // Identity when using API key
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
	Name         string            `json:"name"`
	Personality  string            `json:"personality"`
	Identity     IdentityConfig    `json:"identity"`
	Capabilities AgentCapabilities `json:"capabilities"`
}

// SessionStateManager provides utilities for managing session state during agent operations
type SessionStateManager struct {
	store *sessions.Store
}

// NewSessionStateManager creates a new session state manager
func NewSessionStateManager(store *sessions.Store) *SessionStateManager {
	return &SessionStateManager{store: store}
}

// BeginProcessing marks a session as processing and records the start of request handling
func (sm *SessionStateManager) BeginProcessing(sessionKey string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["action"] = "begin_processing"
	return sm.store.UpdateSessionState(sessionKey, sessions.SessionStateProcessing, metadata)
}

// BeginWaiting marks a session as waiting for external resources
func (sm *SessionStateManager) BeginWaiting(sessionKey string, waitingFor string) error {
	metadata := map[string]interface{}{
		"action":      "begin_waiting",
		"waiting_for": waitingFor,
	}
	return sm.store.UpdateSessionState(sessionKey, sessions.SessionStateWaiting, metadata)
}

// CompleteProcessing marks a session as idle after completing processing
func (sm *SessionStateManager) CompleteProcessing(sessionKey string, metadata map[string]interface{}) error {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["action"] = "complete_processing"
	return sm.store.UpdateSessionState(sessionKey, sessions.SessionStateIdle, metadata)
}

// RecordError marks a session as having encountered an error
func (sm *SessionStateManager) RecordError(sessionKey string, err error) error {
	metadata := map[string]interface{}{
		"action": "error_occurred",
		"error":  err.Error(),
	}
	return sm.store.UpdateSessionState(sessionKey, sessions.SessionStateError, metadata)
}

// MarkActivity records activity without changing the session state
func (sm *SessionStateManager) MarkActivity(sessionKey string) {
	sm.store.MarkSessionActivity(sessionKey)
}
