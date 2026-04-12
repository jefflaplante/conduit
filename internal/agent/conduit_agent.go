package agent

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
	"conduit/internal/skills"
	"conduit/internal/workspace"
)

// DefaultPromptCacheTTL is the default time-to-live for cached system prompts.
const DefaultPromptCacheTTL = 5 * time.Minute

// promptCacheEntry holds a cached system prompt with its expiration time.
type promptCacheEntry struct {
	blocks    []ai.SystemBlock
	expiresAt time.Time
}

// ConduitAgentWithIntegration implements the Conduit agent system with full integration
type ConduitAgentWithIntegration struct {
	mu               sync.RWMutex
	name             string
	personality      string
	email            config.AgentEmail
	identity         IdentityConfig
	capabilities     AgentCapabilities
	promptScaling    *config.PromptScalingConfig
	timezone         string
	runtimeChannel   string
	tools            []ai.Tool
	workspaceContext *workspace.WorkspaceContext
	summaryManager   *workspace.SummaryManager
	skillsManager    *skills.Manager
	modelAliases     map[string]string
	promptBuilder    *PromptBuilder
	brainService     BrainLister

	// System prompt cache: keyed by "sessionKey:model:isOAuth"
	promptCache    sync.Map
	promptCacheTTL time.Duration
}

// NewConduitAgentWithIntegration creates a new Conduit agent instance with full integration.
// modelAliases maps short names to full model identifiers for the system prompt;
// pass nil to use built-in defaults.
// summaryManager enables AI-powered summarization for small-context models; pass nil to disable.
// brainService is optional; if provided, enables Situation Awareness section.
func NewConduitAgentWithIntegration(
	cfg AgentConfig,
	tools []ai.Tool,
	workspaceContext *workspace.WorkspaceContext,
	summaryManager *workspace.SummaryManager,
	skillsManager *skills.Manager,
	modelAliases map[string]string,
	brainService BrainLister,
) *ConduitAgentWithIntegration {
	agent := &ConduitAgentWithIntegration{
		name:             cfg.Name,
		personality:      cfg.Personality,
		email:            cfg.Email,
		identity:         cfg.Identity,
		capabilities:     cfg.Capabilities,
		promptScaling:    &cfg.PromptScaling,
		timezone:         cfg.Timezone,
		runtimeChannel:   cfg.RuntimeChannel,
		tools:            tools,
		workspaceContext: workspaceContext,
		summaryManager:   summaryManager,
		skillsManager:    skillsManager,
		modelAliases:     modelAliases,
		brainService:     brainService,
		promptCacheTTL:   DefaultPromptCacheTTL,
	}

	agent.promptBuilder = NewPromptBuilder(
		agent.name,
		agent.personality,
		agent.email,
		agent.identity,
		agent.capabilities,
		agent.tools,
		agent.workspaceContext,
		agent.summaryManager,
		agent.skillsManager,
		agent.modelAliases,
		agent.promptScaling,
		agent.timezone,
		agent.runtimeChannel,
		agent.brainService,
	)

	return agent
}

// SetTools updates the agent's tool definitions (used after deferred initialization)
func (a *ConduitAgentWithIntegration) SetTools(tools []ai.Tool) {
	a.mu.Lock()
	a.tools = tools
	// Rebuild prompt builder with new tools
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.email,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.summaryManager,
		a.skillsManager,
		a.modelAliases,
		a.promptScaling,
		a.timezone,
		a.runtimeChannel,
		a.brainService,
	)
	a.mu.Unlock()
	// Invalidate prompt cache since tools affect prompt content
	a.InvalidatePromptCache()
}

// Name returns the agent name
func (a *ConduitAgentWithIntegration) Name() string {
	return a.name
}

// BuildSystemPrompt builds the system prompt for a session with full integration.
// Results are cached per session+model+authType combination with a configurable TTL.
func (a *ConduitAgentWithIntegration) BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]ai.SystemBlock, error) {
	// Initialize skills manager if needed
	a.mu.RLock()
	sm := a.skillsManager
	caps := a.capabilities
	a.mu.RUnlock()

	if sm != nil && caps.SkillsIntegration && !sm.IsInitialized() {
		if err := sm.Initialize(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize skills manager: %w", err)
		}
	}

	// Determine if this is OAuth based on session context or other indicators
	isOAuth := a.detectOAuthFromSession(session)

	// Build cache key from factors that affect prompt content
	cacheKey := a.buildPromptCacheKey(session, isOAuth)

	// Check cache for valid entry
	if cached, ok := a.promptCache.Load(cacheKey); ok {
		entry := cached.(promptCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			// Return a copy to prevent callers from modifying cached data
			return copySystemBlocks(entry.blocks), nil
		}
		// Entry expired, delete it
		a.promptCache.Delete(cacheKey)
	}

	// Build new prompt (read-lock promptBuilder)
	a.mu.RLock()
	pb := a.promptBuilder
	a.mu.RUnlock()

	blocks, err := pb.Build(ctx, session, isOAuth)
	if err != nil {
		return nil, err
	}

	// Cache the result
	a.mu.RLock()
	ttl := a.promptCacheTTL
	a.mu.RUnlock()
	if ttl == 0 {
		ttl = DefaultPromptCacheTTL
	}
	a.promptCache.Store(cacheKey, promptCacheEntry{
		blocks:    copySystemBlocks(blocks),
		expiresAt: time.Now().Add(ttl),
	})

	return blocks, nil
}

// BuildSystemPromptDebug builds the system prompt with full debug info, bypassing cache.
func (a *ConduitAgentWithIntegration) BuildSystemPromptDebug(ctx context.Context, session *sessions.Session) (*PromptDebugInfo, error) {
	// Initialize skills manager if needed
	a.mu.RLock()
	sm := a.skillsManager
	caps := a.capabilities
	a.mu.RUnlock()

	if sm != nil && caps.SkillsIntegration && !sm.IsInitialized() {
		if err := sm.Initialize(ctx); err != nil {
			return nil, fmt.Errorf("failed to initialize skills manager: %w", err)
		}
	}

	isOAuth := a.detectOAuthFromSession(session)

	a.mu.RLock()
	pb := a.promptBuilder
	a.mu.RUnlock()

	return pb.BuildDebug(ctx, session, isOAuth)
}

// buildPromptCacheKey creates a cache key from session and auth state.
// The key includes session key, model, and OAuth status since these affect prompt content.
func (a *ConduitAgentWithIntegration) buildPromptCacheKey(session *sessions.Session, isOAuth bool) string {
	sessionKey := ""
	model := ""
	if session != nil {
		sessionKey = session.Key
		if session.Context != nil {
			model = session.Context["model"]
		}
	}
	oauthStr := "api"
	if isOAuth {
		oauthStr = "oauth"
	}
	return fmt.Sprintf("%s:%s:%s", sessionKey, model, oauthStr)
}

// copySystemBlocks creates a deep copy of system blocks to prevent cache mutation.
func copySystemBlocks(blocks []ai.SystemBlock) []ai.SystemBlock {
	if blocks == nil {
		return nil
	}
	copied := make([]ai.SystemBlock, len(blocks))
	for i, block := range blocks {
		copied[i] = ai.SystemBlock{
			Type: block.Type,
			Text: block.Text,
			Meta: block.Meta, // Note: Meta is interface{}, shallow copy only
		}
	}
	return copied
}

// InvalidatePromptCache clears all cached system prompts.
// Call this when tools, configuration, or other prompt-affecting state changes.
func (a *ConduitAgentWithIntegration) InvalidatePromptCache() {
	a.promptCache.Range(func(key, _ interface{}) bool {
		a.promptCache.Delete(key)
		return true
	})
}

// InvalidatePromptCacheForSession clears cached system prompt for a specific session.
// Call this when session-specific state changes (e.g., model switch).
func (a *ConduitAgentWithIntegration) InvalidatePromptCacheForSession(sessionKey string) {
	a.promptCache.Range(func(key, _ interface{}) bool {
		if keyStr, ok := key.(string); ok && strings.HasPrefix(keyStr, sessionKey+":") {
			a.promptCache.Delete(key)
		}
		return true
	})
}

// SetPromptCacheTTL sets the time-to-live for cached system prompts.
func (a *ConduitAgentWithIntegration) SetPromptCacheTTL(ttl time.Duration) {
	a.promptCacheTTL = ttl
}

// GetToolDefinitions returns available tool definitions including skills-generated tools.
// When session contains a "skill_filter" context key (comma-separated skill names),
// only tools from those skills are included. Pass nil for all tools.
func (a *ConduitAgentWithIntegration) GetToolDefinitions(session *sessions.Session) []ai.Tool {
	a.mu.RLock()
	allTools := make([]ai.Tool, len(a.tools))
	copy(allTools, a.tools)
	sm := a.skillsManager
	caps := a.capabilities
	a.mu.RUnlock()

	// Parse skill filter from session context
	var skillFilter map[string]bool
	if session != nil && session.Context != nil {
		if filterStr := session.Context["skill_filter"]; filterStr != "" {
			skillFilter = make(map[string]bool)
			for _, name := range strings.Split(filterStr, ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					skillFilter[trimmed] = true
				}
			}
		}
	}

	// Add skills-generated tools if skills integration is enabled
	if sm != nil && caps.SkillsIntegration && sm.IsEnabled() {
		ctx := context.Background()
		var skillTools []skills.SkillToolInterface
		var err error
		if len(skillFilter) > 0 {
			skillTools, err = sm.GenerateToolsFiltered(ctx, skillFilter)
		} else {
			skillTools, err = sm.GenerateTools(ctx)
		}
		if err == nil {
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

	// Check for silent response tokens. Exact match after trimming, or
	// contains-match only for short responses (≤40 chars) to tolerate minor
	// LLM wrapping. Long responses that merely reference the token are not suppressed.
	upper := strings.ToUpper(strings.TrimSpace(response.Content))

	silent := upper == SilentReplyToken || upper == HeartbeatOKToken
	if !silent && len(upper) <= 40 {
		silent = strings.Contains(upper, SilentReplyToken) || strings.Contains(upper, HeartbeatOKToken)
	}
	if silent {
		processed.Silent = true
		processed.Content = ""
		processed.Modified = true
	} else if a.capabilities.SilentReplies && a.isHeartbeatResponse(strings.TrimSpace(response.Content)) {
		// Check for heartbeat-style responses that should be silent
		processed.Silent = true
		processed.Content = ""
		processed.Modified = true
	}

	// Detect spawn claims without actual SessionsSpawn tool call.
	// The LLM sometimes generates text claiming it spawned a sub-agent
	// without actually emitting the tool_use block.
	if !processed.Silent && looksLikeSpawnClaim(response.Content) && !hasToolCall(response.ToolCalls, "SessionsSpawn") {
		processed.Content += "\n\n[System: No SessionsSpawn tool call was detected. The sub-agent was NOT spawned. You must call SessionsSpawn to delegate work.]"
		processed.Modified = true
		log.Printf("[Agent] WARNING: spawn claim detected without SessionsSpawn tool call")
	}

	return processed, nil
}

// looksLikeSpawnClaim checks if text contains language suggesting a sub-agent was spawned.
func looksLikeSpawnClaim(content string) bool {
	lower := strings.ToLower(content)
	spawnIndicators := []string{
		"sub-agent spawned",
		"sub-agent started",
		"spawned a sub-agent",
		"spawning sub-agent",
		"delegated to sub-agent",
		"spinning up sub-agent",
		"launched sub-agent",
		"kicked off sub-agent",
	}
	for _, indicator := range spawnIndicators {
		if strings.Contains(lower, indicator) {
			return true
		}
	}
	return false
}

// hasToolCall checks if the tool call list contains a call to the named tool.
func hasToolCall(toolCalls []ai.ToolCall, name string) bool {
	for _, tc := range toolCalls {
		if tc.Name == name {
			return true
		}
	}
	return false
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

// isHeartbeatResponse checks if a short response looks like a "nothing to report" heartbeat ack.
// Only applies to responses under 100 characters to avoid suppressing legitimate content
// that happens to contain completion phrases.
func (a *ConduitAgentWithIntegration) isHeartbeatResponse(content string) bool {
	content = strings.ToLower(strings.TrimSpace(content))

	// Only apply heuristic to short responses — long responses are real content
	if len(content) >= 100 {
		return false
	}

	// Narrow set of completion phrases indicating "nothing to report".
	// Active-verb patterns (checking, monitoring, scanning, reviewing) are
	// intentionally excluded — they describe ongoing work, not idle status.
	silentPatterns := []string{
		"all clear",
		"no updates",
		"nothing urgent",
		"nothing to report",
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
	a.mu.Lock()
	a.name = cfg.Name
	a.personality = cfg.Personality
	a.email = cfg.Email
	a.identity = cfg.Identity
	a.capabilities = cfg.Capabilities
	a.promptScaling = &cfg.PromptScaling
	a.timezone = cfg.Timezone
	a.runtimeChannel = cfg.RuntimeChannel

	// Rebuild prompt builder with new configuration
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.email,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.summaryManager,
		a.skillsManager,
		a.modelAliases,
		a.promptScaling,
		a.timezone,
		a.runtimeChannel,
		a.brainService,
	)
	a.mu.Unlock()

	// Invalidate prompt cache since configuration affects prompt content
	a.InvalidatePromptCache()

	return nil
}

// UpdateTools updates the available tools
func (a *ConduitAgentWithIntegration) UpdateTools(tools []ai.Tool) error {
	a.tools = tools

	// Rebuild prompt builder with new tools
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.email,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.summaryManager,
		a.skillsManager,
		a.modelAliases,
		a.promptScaling,
		a.timezone,
		a.runtimeChannel,
		a.brainService,
	)

	// Invalidate prompt cache since tools affect prompt content
	a.InvalidatePromptCache()

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

// SetSummaryManager sets the summary manager for AI-powered workspace summarization.
// This is called after the AI router is available to create the summary executor.
func (a *ConduitAgentWithIntegration) SetSummaryManager(sm *workspace.SummaryManager) {
	a.mu.Lock()
	a.summaryManager = sm

	// Rebuild prompt builder with summary manager
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.email,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.summaryManager,
		a.skillsManager,
		a.modelAliases,
		a.promptScaling,
		a.timezone,
		a.runtimeChannel,
		a.brainService,
	)
	a.mu.Unlock()

	// Invalidate prompt cache since summarization affects prompt content
	a.InvalidatePromptCache()
}

// GetSummaryManager returns the summary manager (for external access if needed)
func (a *ConduitAgentWithIntegration) GetSummaryManager() *workspace.SummaryManager {
	return a.summaryManager
}

// GetSkillsManager returns the skills manager (for external access if needed)
func (a *ConduitAgentWithIntegration) GetSkillsManager() *skills.Manager {
	return a.skillsManager
}

// SetBrainService sets the brain service for Situation Awareness prompt section.
// This is called after the brain is available to provide reflection data.
func (a *ConduitAgentWithIntegration) SetBrainService(bs BrainLister) {
	a.mu.Lock()
	a.brainService = bs

	// Rebuild prompt builder with brain service
	a.promptBuilder = NewPromptBuilder(
		a.name,
		a.personality,
		a.email,
		a.identity,
		a.capabilities,
		a.tools,
		a.workspaceContext,
		a.summaryManager,
		a.skillsManager,
		a.modelAliases,
		a.promptScaling,
		a.timezone,
		a.runtimeChannel,
		a.brainService,
	)
	a.mu.Unlock()

	// Invalidate prompt cache since brain data affects prompt content
	a.InvalidatePromptCache()
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
