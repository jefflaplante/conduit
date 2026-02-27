package agent

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/ai"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

// ConduitAgentWithIntegration implements the Conduit agent system with full integration
type ConduitAgentWithIntegration struct {
	name             string
	personality      string
	identity         IdentityConfig
	capabilities     AgentCapabilities
	tools            []ai.Tool
	workspaceContext *workspace.WorkspaceContext
	skillsManager    *skills.Manager
	modelAliases     map[string]string
	promptBuilder    *PromptBuilder
}

// NewConduitAgentWithIntegration creates a new Conduit agent instance with full integration.
// modelAliases maps short names to full model identifiers for the system prompt;
// pass nil to use built-in defaults.
func NewConduitAgentWithIntegration(
	cfg AgentConfig,
	tools []ai.Tool,
	workspaceContext *workspace.WorkspaceContext,
	skillsManager *skills.Manager,
	modelAliases map[string]string,
) *ConduitAgentWithIntegration {
	agent := &ConduitAgentWithIntegration{
		name:             cfg.Name,
		personality:      cfg.Personality,
		identity:         cfg.Identity,
		capabilities:     cfg.Capabilities,
		tools:            tools,
		workspaceContext: workspaceContext,
		skillsManager:    skillsManager,
		modelAliases:     modelAliases,
	}

	agent.promptBuilder = NewPromptBuilder(
		agent.name,
		agent.personality,
		agent.identity,
		agent.capabilities,
		agent.tools,
		agent.workspaceContext,
		agent.skillsManager,
		agent.modelAliases,
	)

	return agent
}

// SetTools updates the agent's tool definitions (used after deferred initialization)
func (a *ConduitAgentWithIntegration) SetTools(tools []ai.Tool) {
	a.tools = tools
	// Rebuild prompt builder with new tools
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.skillsManager,
		a.modelAliases,
	)
}

// Name returns the agent name
func (a *ConduitAgentWithIntegration) Name() string {
	return a.name
}

// BuildSystemPrompt builds the system prompt for a session with full integration
func (a *ConduitAgentWithIntegration) BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]ai.SystemBlock, error) {
	// Initialize skills manager if needed
	if a.skillsManager != nil && a.capabilities.SkillsIntegration && !a.skillsManager.IsInitialized() {
		if err := a.skillsManager.Initialize(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize skills manager: %w", err)
		}
	}

	// Determine if this is OAuth based on session context or other indicators
	isOAuth := a.detectOAuthFromSession(session)

	return a.promptBuilder.Build(ctx, session, isOAuth)
}

// GetToolDefinitions returns available tool definitions including skills-generated tools
func (a *ConduitAgentWithIntegration) GetToolDefinitions() []ai.Tool {
	allTools := make([]ai.Tool, len(a.tools))
	copy(allTools, a.tools)

	// Add skills-generated tools if skills integration is enabled
	if a.skillsManager != nil && a.capabilities.SkillsIntegration && a.skillsManager.IsEnabled() {
		ctx := context.Background()
		if skillTools, err := a.skillsManager.GenerateTools(ctx); err == nil {
			// Convert skill tools to ai.Tool format
			for _, skillTool := range skillTools {
				aiTool := ai.Tool{
					Name:        skillTool.Name(),
					Description: skillTool.Description(),
					Parameters:  skillTool.Parameters(),
				}
				allTools = append(allTools, aiTool)
			}
		}
	}

	return allTools
}

// ProcessResponse processes an AI response and determines actions
func (a *ConduitAgentWithIntegration) ProcessResponse(ctx context.Context, response *ai.GenerateResponse) (*ai.AgentProcessedResponse, error) {
	processed := &ai.AgentProcessedResponse{
		Content:   response.Content,
		ToolCalls: response.ToolCalls,
		Silent:    false,
		Modified:  false,
	}

	// Check for special Conduit response patterns using contains check,
	// because the LLM sometimes wraps tokens in surrounding text.
	upper := strings.ToUpper(strings.TrimSpace(response.Content))

	if strings.Contains(upper, "HEARTBEAT_OK") || strings.Contains(upper, "NO_REPLY") {
		processed.Silent = true
		processed.Content = ""
		processed.Modified = true
	} else if a.capabilities.SilentReplies && a.isHeartbeatResponse(strings.TrimSpace(response.Content)) {
		// Check for heartbeat-style responses that should be silent
		processed.Silent = true
		processed.Content = ""
		processed.Modified = true
	}

	return processed, nil
}

// detectOAuthFromSession determines if the session is using OAuth
func (a *ConduitAgentWithIntegration) detectOAuthFromSession(session *sessions.Session) bool {
	if session == nil || session.Context == nil {
		return false
	}

	// Check session context for OAuth indicator
	if authType, exists := session.Context["auth_type"]; exists {
		return authType == "oauth"
	}

	// Default to OAuth if we can't determine (Claude Code is default for OAuth)
	return true
}

// isHeartbeatResponse checks if a response looks like a heartbeat-style response
func (a *ConduitAgentWithIntegration) isHeartbeatResponse(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))

	// Patterns that indicate this should be a silent response
	silentPatterns := []string{
		"checking",
		"monitoring",
		"scanning",
		"reviewing",
		"all clear",
		"no updates",
		"nothing urgent",
		"status: ok",
		"systems normal",
	}

	for _, pattern := range silentPatterns {
		if strings.Contains(content, pattern) {
			return true
		}
	}

	return false
}

// UpdateConfiguration updates the agent configuration
func (a *ConduitAgentWithIntegration) UpdateConfiguration(cfg AgentConfig) error {
	a.name = cfg.Name
	a.personality = cfg.Personality
	a.identity = cfg.Identity
	a.capabilities = cfg.Capabilities

	// Rebuild prompt builder with new configuration
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.skillsManager,
		a.modelAliases,
	)

	return nil
}

// UpdateTools updates the available tools
func (a *ConduitAgentWithIntegration) UpdateTools(tools []ai.Tool) error {
	a.tools = tools

	// Rebuild prompt builder with new tools
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.skillsManager,
		a.modelAliases,
	)

	return nil
}

// GetCapabilities returns the agent's capabilities
func (a *ConduitAgentWithIntegration) GetCapabilities() AgentCapabilities {
	return a.capabilities
}

// GetIdentity returns the agent's identity configuration
func (a *ConduitAgentWithIntegration) GetIdentity() IdentityConfig {
	return a.identity
}

// SetOAuthMode configures the agent for OAuth or API key mode
func (a *ConduitAgentWithIntegration) SetOAuthMode(isOAuth bool, session *sessions.Session) error {
	if session == nil {
		return fmt.Errorf("session is required to set OAuth mode")
	}

	if session.Context == nil {
		session.Context = make(map[string]string)
	}

	if isOAuth {
		session.Context["auth_type"] = "oauth"
	} else {
		session.Context["auth_type"] = "api_key"
	}

	return nil
}

// GetWorkspaceContext returns the workspace context manager (for external access if needed)
func (a *ConduitAgentWithIntegration) GetWorkspaceContext() *workspace.WorkspaceContext {
	return a.workspaceContext
}

// GetSkillsManager returns the skills manager (for external access if needed)
func (a *ConduitAgentWithIntegration) GetSkillsManager() *skills.Manager {
	return a.skillsManager
}

// Initialize performs any needed initialization for the agent and its components
func (a *ConduitAgentWithIntegration) Initialize(ctx context.Context) error {
	// Initialize skills manager if present and enabled
	if a.skillsManager != nil && a.capabilities.SkillsIntegration {
		if err := a.skillsManager.Initialize(ctx); err != nil {
			return fmt.Errorf("failed to initialize skills manager: %w", err)
		}
	}

	return nil
}
