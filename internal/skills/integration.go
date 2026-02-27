package skills

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
)

// SkillIntegrator handles integration of skills with the tool system
type SkillIntegrator struct {
	executor *Executor
	loader   *SkillLoader
}

// NewSkillIntegrator creates a new skill integrator
func NewSkillIntegrator(executor *Executor) *SkillIntegrator {
	return &SkillIntegrator{
		executor: executor,
		loader:   NewSkillLoader(),
	}
}

// GenerateToolsFromSkills creates tool instances for each available skill
func (i *SkillIntegrator) GenerateToolsFromSkills(skills []Skill) []SkillToolInterface {
	var generatedTools []SkillToolInterface

	for _, skill := range skills {
		// Create primary skill tool
		skillTool := &SkillTool{
			skill:    skill,
			executor: i.executor,
			actions:  i.extractAvailableActions(skill),
		}

		generatedTools = append(generatedTools, skillTool)

		// Create action-specific tools for common actions
		actionTools := i.generateActionTools(skill)
		generatedTools = append(generatedTools, actionTools...)
	}

	log.Printf("Generated %d tools from %d skills", len(generatedTools), len(skills))
	return generatedTools
}

// generateActionTools creates specialized tools for common skill actions
func (i *SkillIntegrator) generateActionTools(skill Skill) []SkillToolInterface {
	var actionTools []SkillToolInterface

	// Extract actions from skill content
	actions := i.extractAvailableActions(skill)

	// Create action-specific tools for key actions
	for _, action := range actions {
		if i.shouldCreateActionTool(action) {
			actionTool := &SkillActionTool{
				skill:    skill,
				action:   action,
				executor: i.executor,
			}
			actionTools = append(actionTools, actionTool)
		}
	}

	return actionTools
}

// extractAvailableActions gets available actions from skill content
func (i *SkillIntegrator) extractAvailableActions(skill Skill) []string {
	actions := i.loader.ExtractActionsFromContent(skill.Content)

	// Add common actions based on skill name/type
	commonActions := i.getCommonActionsForSkill(skill.Name)
	actions = append(actions, commonActions...)

	return removeDuplicates(actions)
}

// getCommonActionsForSkill returns commonly used actions for known skill types
func (i *SkillIntegrator) getCommonActionsForSkill(skillName string) []string {
	skillActions := map[string][]string{
		"email":    {"search", "read", "send", "cleanup", "list"},
		"ha":       {"status", "control", "list", "get_state", "call_service"},
		"weather":  {"current", "forecast"},
		"solar":    {"status", "daily_report", "current"},
		"unifi":    {"status", "devices", "clients"},
		"briefing": {"generate", "morning", "evening"},
		"bujo":     {"add", "list", "migrate", "weekly_rollup"},
		"research": {"search", "summarize"},
	}

	if actions, exists := skillActions[skillName]; exists {
		return actions
	}

	return []string{"status", "help"}
}

// shouldCreateActionTool determines if an action warrants its own tool
func (i *SkillIntegrator) shouldCreateActionTool(action string) bool {
	// Only create separate tools for key actions to avoid tool proliferation
	keyActions := []string{
		"search", "status", "current", "forecast", "cleanup", "briefing",
		"generate", "daily_report", "monitor", "list",
	}

	for _, keyAction := range keyActions {
		if action == keyAction {
			return true
		}
	}

	return false
}

// SkillToolInterface defines the interface for skill-based tools
type SkillToolInterface interface {
	Name() string
	Description() string
	Parameters() map[string]interface{}
	Execute(ctx context.Context, args map[string]interface{}) (*SkillToolResult, error)
}

// SkillToolResult represents the result of skill tool execution
type SkillToolResult struct {
	Success bool                   `json:"success"`
	Content string                 `json:"content"`
	Error   string                 `json:"error,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// SkillTool represents a general-purpose tool for a skill
type SkillTool struct {
	skill    Skill
	executor *Executor
	actions  []string
}

// Name returns the tool name
func (st *SkillTool) Name() string {
	return fmt.Sprintf("skill_%s", st.skill.Name)
}

// Description returns the tool description
func (st *SkillTool) Description() string {
	emoji := st.skill.Metadata.Conduit.Emoji
	if emoji == "" {
		emoji = "🔧"
	}
	return fmt.Sprintf("%s %s (Available actions: %s)", emoji, st.skill.Description,
		formatActionsList(st.actions))
}

// Parameters returns the tool parameter schema
func (st *SkillTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type": "string",
				"description": fmt.Sprintf("Action to perform. Available: %s",
					formatActionsList(st.actions)),
				"enum": st.actions,
			},
			"args": map[string]interface{}{
				"type":        "object",
				"description": "Arguments for the skill action",
			},
		},
		"required": []string{"action"},
	}
}

// Execute runs the skill with the given parameters
func (st *SkillTool) Execute(ctx context.Context, args map[string]interface{}) (*SkillToolResult, error) {
	action, ok := args["action"].(string)
	if !ok {
		return &SkillToolResult{
			Success: false,
			Error:   "action parameter is required",
		}, nil
	}

	skillArgs, _ := args["args"].(map[string]interface{})
	if skillArgs == nil {
		skillArgs = make(map[string]interface{})
	}

	result, err := st.executor.ExecuteSkill(ctx, st.skill, action, skillArgs)
	if err != nil {
		return &SkillToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &SkillToolResult{
		Success: result.Success,
		Content: result.Output,
		Error:   result.Error,
		Data:    result.Data,
	}, nil
}

// SkillActionTool represents a tool for a specific skill action
type SkillActionTool struct {
	skill    Skill
	action   string
	executor *Executor
}

// Name returns the action-specific tool name
func (sat *SkillActionTool) Name() string {
	return fmt.Sprintf("%s_%s", sat.skill.Name, sat.action)
}

// Description returns the action-specific tool description
func (sat *SkillActionTool) Description() string {
	emoji := sat.skill.Metadata.Conduit.Emoji
	if emoji == "" {
		emoji = "🔧"
	}
	return fmt.Sprintf("%s %s - %s action", emoji, sat.skill.Name, sat.action)
}

// Parameters returns the action-specific parameter schema
func (sat *SkillActionTool) Parameters() map[string]interface{} {
	// Customize parameters based on action type
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"args": map[string]interface{}{
				"type":        "object",
				"description": fmt.Sprintf("Arguments for %s action", sat.action),
			},
		},
	}

	// Add action-specific parameters
	switch sat.action {
	case "search":
		params["properties"].(map[string]interface{})["query"] = map[string]interface{}{
			"type":        "string",
			"description": "Search query",
		}
	case "status":
		// No additional parameters needed
	case "forecast", "current":
		params["properties"].(map[string]interface{})["location"] = map[string]interface{}{
			"type":        "string",
			"description": "Location for weather data",
		}
	}

	return params
}

// Execute runs the specific action
func (sat *SkillActionTool) Execute(ctx context.Context, args map[string]interface{}) (*SkillToolResult, error) {
	skillArgs, _ := args["args"].(map[string]interface{})
	if skillArgs == nil {
		skillArgs = make(map[string]interface{})
	}

	// Add action-specific arguments to skill args
	for key, value := range args {
		if key != "args" {
			skillArgs[key] = value
		}
	}

	result, err := sat.executor.ExecuteSkill(ctx, sat.skill, sat.action, skillArgs)
	if err != nil {
		return &SkillToolResult{
			Success: false,
			Error:   err.Error(),
		}, nil
	}

	return &SkillToolResult{
		Success: result.Success,
		Content: result.Output,
		Error:   result.Error,
		Data:    result.Data,
	}, nil
}

// Helper functions

// formatActionsList creates a readable list of actions
func formatActionsList(actions []string) string {
	if len(actions) == 0 {
		return "none"
	}
	if len(actions) <= 3 {
		return fmt.Sprintf("%v", actions)
	}
	return fmt.Sprintf("%s and %d more", actions[:3], len(actions)-3)
}

// removeDuplicates removes duplicate strings from a slice
func removeDuplicates(slice []string) []string {
	keys := make(map[string]bool)
	var result []string

	for _, item := range slice {
		if !keys[item] {
			keys[item] = true
			result = append(result, item)
		}
	}

	return result
}

// GenerateToolAdapters creates skill tool adapters that implement the tools.Tool interface
// This is the main integration point called from the tools registry
func GenerateToolAdapters(ctx context.Context, manager *Manager) ([]*SkillToolAdapter, error) {
	if manager == nil || !manager.IsEnabled() {
		log.Println("Skills manager not available or disabled, no skill tools generated")
		return []*SkillToolAdapter{}, nil
	}

	skillTools, err := manager.GenerateTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate skill tools: %w", err)
	}

	var adapters []*SkillToolAdapter
	for _, skillTool := range skillTools {
		adapter := NewSkillToolAdapter(skillTool)
		adapters = append(adapters, adapter)
	}

	log.Printf("Generated %d skill tool adapters", len(adapters))
	return adapters, nil
}

// BuildSkillsContext creates contextual information about skills for agent prompts
func (i *SkillIntegrator) BuildSkillsContext(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var context []string
	context = append(context, "## Available Skills\n")
	context = append(context, "The following specialized skills are available:\n")

	for _, skill := range skills {
		emoji := skill.Metadata.Conduit.Emoji
		if emoji == "" {
			emoji = "🔧"
		}

		actions := i.extractAvailableActions(skill)
		skillInfo := fmt.Sprintf("### %s %s\n%s\n**Tool:** `skill_%s` **Actions:** %s\n**Location:** %s\n",
			emoji, skill.Name, skill.Description, skill.Name,
			formatActionsList(actions), filepath.Base(skill.Location))

		context = append(context, skillInfo)
	}

	return fmt.Sprintf("%s\n", context)
}
