package skills

import (
	"context"
)

// RegistryTool interface matches the tools.Tool interface to avoid circular imports
type RegistryTool interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, args map[string]interface{}) (*RegistryToolResult, error)
}

// RegistryToolResult matches the tools.ToolResult to avoid circular imports
type RegistryToolResult struct {
	Success      bool                   `json:"success"`
	Content      string                 `json:"content"`
	Error        string                 `json:"error,omitempty"`
	Data         map[string]interface{} `json:"data,omitempty"`
	FallbackUsed bool                   `json:"fallback_used,omitempty"`
	CacheHit     bool                   `json:"cache_hit,omitempty"`
	Retries      int                    `json:"retries,omitempty"`
}

// SkillToolAdapter adapts skill tools to the registry Tool interface
// It implements the exact interface signature expected by tools.Tool
type SkillToolAdapter struct {
	skillTool SkillToolInterface
}

// NewSkillToolAdapter creates a new skill tool adapter
func NewSkillToolAdapter(skillTool SkillToolInterface) *SkillToolAdapter {
	return &SkillToolAdapter{
		skillTool: skillTool,
	}
}

// Name returns the tool name
func (sta *SkillToolAdapter) Name() string {
	return sta.skillTool.Name()
}

// Description returns the tool description
func (sta *SkillToolAdapter) Description() string {
	return sta.skillTool.Description()
}

// Parameters returns the tool parameter schema
func (sta *SkillToolAdapter) Parameters() map[string]interface{} {
	return sta.skillTool.Parameters()
}

// Execute executes the skill tool and adapts the result to registry format
// This method signature must match tools.Tool.Execute exactly
func (sta *SkillToolAdapter) Execute(ctx context.Context, args map[string]interface{}) (*RegistryToolResult, error) {
	result, err := sta.skillTool.Execute(ctx, args)
	if err != nil {
		return &RegistryToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	// Convert SkillToolResult to match tools.ToolResult
	return &RegistryToolResult{
		Success:      result.Success,
		Content:      result.Content,
		Error:        result.Error,
		Data:         result.Data,
		FallbackUsed: false, // Skills don't use fallbacks
		CacheHit:     false, // Skills manage their own caching
		Retries:      0,     // Skills handle retries internally
	}, nil
}

// ToolResultCompatible type has been replaced by RegistryToolResult above

// GetSkillSystemContext returns context information for agent prompts
func GetSkillSystemContext(ctx context.Context, manager *Manager) (string, error) {
	if manager == nil || !manager.IsEnabled() {
		return "", nil
	}

	return manager.BuildSystemPromptContext(ctx)
}
