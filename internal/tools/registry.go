package tools

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"conduit/internal/config"
	"conduit/internal/tools/communication"
	"conduit/internal/tools/core"
	"conduit/internal/tools/scheduling"
	"conduit/internal/tools/schema"
	"conduit/internal/tools/types"
	"conduit/internal/tools/vision"
	"conduit/internal/tools/web"
)

// Registry manages available tools and their execution
type Registry struct {
	tools        map[string]types.Tool
	sandboxCfg   config.SandboxConfig
	enabledTools map[string]bool
	services     *types.ToolServices
}

// Type aliases for backward compatibility
type ChannelSender = types.ChannelSender
type ToolServices = types.ToolServices
type Tool = types.Tool
type ToolResult = types.ToolResult

// NewRegistry creates a new tools registry with service dependencies
func NewRegistry(cfg config.ToolsConfig) *Registry {
	registry := &Registry{
		tools:        make(map[string]types.Tool),
		sandboxCfg:   cfg.Sandbox,
		enabledTools: make(map[string]bool),
		services:     &types.ToolServices{}, // Initialize empty services
	}

	// Mark enabled tools
	for _, toolName := range cfg.EnabledTools {
		registry.enabledTools[toolName] = true
	}

	// Don't register tools here - wait for services to be set

	return registry
}

// SetServices sets the service dependencies and registers tools
func (r *Registry) SetServices(services *types.ToolServices) {
	r.services = services

	// Initialize schema builder with discovery providers
	r.initializeSchemaBuilder()

	r.registerAllTools()
}

// initializeSchemaBuilder creates and configures the schema builder with discovery providers
func (r *Registry) initializeSchemaBuilder() {
	if r.services == nil {
		return
	}

	providers := make(map[string]schema.DiscoveryProvider)

	// Channel discovery provider - needs channel manager status
	if r.services.Gateway != nil {
		// Create adapter for gateway channel status
		statusAdapter := schema.NewStatusProviderAdapter(func() map[string]interface{} {
			if status, err := r.services.Gateway.GetChannelStatus(); err == nil && status != nil {
				return status
			}
			return make(map[string]interface{})
		})
		providers["channels"] = schema.NewChannelDiscoveryProvider(statusAdapter)
	}

	// Workspace discovery provider
	if r.services.ConfigMgr != nil {
		workspaceDir := r.sandboxCfg.WorkspaceDir
		if r.services.ConfigMgr.Workspace.ContextDir != "" {
			workspaceDir = r.services.ConfigMgr.Workspace.ContextDir
		}
		providers["workspace_paths"] = schema.NewWorkspaceDiscoveryProvider(
			workspaceDir,
			r.sandboxCfg.AllowedPaths,
		)
	}

	// Initialize schema builder with providers
	r.services.SchemaBuilder = schema.NewBuilder(providers)
}

// registerAllTools registers all available tools from all categories
func (r *Registry) registerAllTools() {
	var allTools []types.Tool

	// Core system tools (enhanced file operations + new memory/session tools)
	allTools = append(allTools, []types.Tool{
		&ReadFileTool{registry: r},
		&WriteFileTool{registry: r},
		core.NewEditTool(r.services),
		&ExecTool{registry: r},
		&ListFilesTool{registry: r},
		// Memory tools (MemorySearch only - use Read for direct file access)
		core.NewMemorySearchTool(r.services, r.sandboxCfg),
		// Session tools
		core.NewSessionsListTool(r.services),
		core.NewSessionsSendTool(r.services),
		core.NewSessionsSpawnTool(r.services),
		core.NewSessionStatusTool(r.services),
		// Gateway tool
		core.NewGatewayTool(r.services),
		// Context tool
		core.NewContextTool(r.services),
		// Find tool (universal search)
		core.NewFindTool(r.services),
		// Facts tool (structured fact extraction from memory)
		core.NewFactsTool(r.services, r.sandboxCfg),
		// Chain tool (multi-tool workflow execution)
		core.NewChainTool(r.services, r.sandboxCfg, r),
	}...)

	// Web integration tools
	allTools = append(allTools, []types.Tool{
		web.NewWebSearchTool(r.services),
		web.NewWebFetchTool(r.services),
	}...)

	// Communication tools
	allTools = append(allTools, []types.Tool{
		communication.NewMessageTool(r.services),
		communication.NewTTSTool(r.services),
	}...)

	// Scheduling tools
	allTools = append(allTools, []types.Tool{
		scheduling.NewCronTool(r.services),
	}...)

	// Vision tools
	allTools = append(allTools, []types.Tool{
		vision.NewImageTool(r.services),
	}...)

	// Infrastructure/Network tools
	allTools = append(allTools, []types.Tool{
		&UniFiTool{registry: r},
	}...)

	// TODO: Fix skills integration after core tools are working
	// Skills-based tools (if skills system is available and enabled)
	// if r.services.SkillsManager != nil && r.services.SkillsManager.IsEnabled() {
	//	skillAdapters, err := skills.GenerateToolAdapters(context.Background(), r.services.SkillsManager)
	//	if err != nil {
	//		log.Printf("Failed to register skill tools: %v", err)
	//	} else {
	//		// Add skill adapters directly - they implement the Tool interface
	//		for _, adapter := range skillAdapters {
	//			allTools = append(allTools, adapter)
	//		}
	//		log.Printf("Successfully registered %d skill-based tools", len(skillAdapters))
	//	}
	// }

	// Register all tools
	for _, tool := range allTools {
		if tool != nil {
			r.tools[tool.Name()] = tool
			log.Printf("Registered tool: %s", tool.Name())
		}
	}
}

// ExecuteTool executes a tool by name with the given arguments, including validation
func (r *Registry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
	// Check if tool is enabled
	if !r.enabledTools[name] {
		return types.NewErrorResult("tool_disabled", fmt.Sprintf("tool '%s' is not enabled", name)), nil
	}

	// Get tool
	tool, exists := r.tools[name]
	if !exists {
		return types.NewErrorResult("tool_not_found", fmt.Sprintf("tool '%s' not found", name)), nil
	}

	// Validate parameters if tool supports validation
	if validator, ok := tool.(types.ParameterValidator); ok {
		validationResult := validator.ValidateParameters(ctx, args)
		if !validationResult.Valid {
			return r.createValidationErrorResult(name, validationResult), nil
		}
	}

	// Execute tool
	result, err := tool.Execute(ctx, args)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool execution error: %v", err),
		}, err
	}

	return result, nil
}

// createValidationErrorResult creates a rich error result from validation failures
func (r *Registry) createValidationErrorResult(toolName string, validation *types.ValidationResult) *types.ToolResult {
	if len(validation.Errors) == 0 {
		return types.NewErrorResult("validation_failed", "Parameter validation failed")
	}

	// Use the first error as the primary error message
	primaryError := validation.Errors[0]
	message := fmt.Sprintf("Parameter '%s': %s", primaryError.Parameter, primaryError.Message)

	result := types.NewErrorResult("invalid_parameter", message).
		WithParameter(primaryError.Parameter, primaryError.ProvidedValue)

	if len(primaryError.AvailableValues) > 0 {
		result.WithAvailableValues(primaryError.AvailableValues)
	}

	if len(primaryError.Examples) > 0 {
		var examples []string
		for _, example := range primaryError.Examples {
			examples = append(examples, fmt.Sprintf("%v", example))
		}
		result.WithExamples(examples)
	}

	// Add suggestions from validation result
	if len(validation.Suggestions) > 0 {
		result.WithSuggestions(validation.Suggestions)
	}

	// Add discovery hint if available
	if primaryError.DiscoveryHint != "" {
		result.WithSuggestions(append(result.ErrorDetails.Suggestions, primaryError.DiscoveryHint))
	}

	// Add context about all validation errors
	context := map[string]interface{}{
		"tool":              toolName,
		"validation_errors": len(validation.Errors),
		"total_parameters":  len(validation.Errors),
	}

	if len(validation.Errors) > 1 {
		var allErrors []string
		for _, err := range validation.Errors {
			allErrors = append(allErrors, fmt.Sprintf("%s: %s", err.Parameter, err.Message))
		}
		context["all_errors"] = allErrors
	}

	return result.WithContext(context)
}

// GetAvailableTools returns a list of available tools
func (r *Registry) GetAvailableTools() map[string]types.Tool {
	available := make(map[string]types.Tool)
	for name, tool := range r.tools {
		if r.enabledTools[name] {
			available[name] = tool
		}
	}
	return available
}

// GetToolSchemas returns JSON schemas for all available tools
func (r *Registry) GetToolSchemas() []map[string]interface{} {
	return r.GetToolSchemasWithContext(context.Background())
}

// GetToolSchemasWithContext returns JSON schemas for all available tools, enhanced with discovery data
func (r *Registry) GetToolSchemasWithContext(ctx context.Context) []map[string]interface{} {
	var schemas []map[string]interface{}

	for _, tool := range r.GetAvailableTools() {
		params := tool.Parameters()

		// Check if tool provides schema hints and we have a schema builder
		if r.services != nil && r.services.SchemaBuilder != nil {
			if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
				hints := enhancedTool.GetSchemaHints()
				if hints != nil && len(hints) > 0 {
					params = r.services.SchemaBuilder.EnhanceSchema(ctx, params, hints)
				}
			}
		}

		toolSchema := map[string]interface{}{
			"name":        tool.Name(),
			"description": tool.Description(),
			"parameters":  params,
		}
		schemas = append(schemas, toolSchema)
	}

	return schemas
}

// GetToolHelp returns comprehensive help information for a specific tool including examples
func (r *Registry) GetToolHelp(toolName string) map[string]interface{} {
	tool, exists := r.tools[toolName]
	if !exists || !r.enabledTools[toolName] {
		return map[string]interface{}{
			"error": fmt.Sprintf("Tool '%s' not found or not enabled", toolName),
		}
	}

	help := map[string]interface{}{
		"name":        tool.Name(),
		"description": tool.Description(),
		"parameters":  tool.Parameters(),
		"enabled":     r.enabledTools[toolName],
	}

	// Add schema hints if available
	if r.services != nil && r.services.SchemaBuilder != nil {
		if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
			hints := enhancedTool.GetSchemaHints()
			if hints != nil && len(hints) > 0 {
				help["schema_hints"] = hints
			}
		}
	}

	// Add usage examples if available
	if exampleProvider, ok := tool.(types.UsageExampleProvider); ok {
		examples := exampleProvider.GetUsageExamples()
		if len(examples) > 0 {
			help["examples"] = examples
		}
	}

	// Add validation capabilities info
	if _, ok := tool.(types.ParameterValidator); ok {
		help["supports_validation"] = true
	}

	if _, ok := tool.(types.ParameterDiscoverer); ok {
		help["supports_discovery"] = true
	}

	return help
}

// GetAllToolsHelp returns help information for all available tools
func (r *Registry) GetAllToolsHelp() map[string]interface{} {
	toolsHelp := make(map[string]interface{})

	for name := range r.GetAvailableTools() {
		toolsHelp[name] = r.GetToolHelp(name)
	}

	return map[string]interface{}{
		"tools": toolsHelp,
		"count": len(toolsHelp),
		"categories": map[string][]string{
			"file_operations": {"Read", "Write", "Edit", "Glob"},
			"system":          {"Bash"},
			"memory":          {"MemorySearch"},
			"communication":   {"Message", "Tts"},
			"web":             {"WebSearch", "WebFetch"},
			"sessions":        {"SessionsList", "SessionsSend", "SessionsSpawn", "SessionStatus"},
			"scheduling":      {"Cron"},
			"gateway":         {"Gateway"},
			"vision":          {"Image"},
		},
	}
}

// isPathAllowed checks if a file path is allowed within the sandbox
func (r *Registry) isPathAllowed(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	for _, allowedPath := range r.sandboxCfg.AllowedPaths {
		if strings.HasPrefix(absPath, allowedPath) {
			return true
		}
	}

	return false
}

// Helper functions for argument parsing
func getStringArg(args map[string]interface{}, key, defaultVal string) string {
	if val, ok := args[key].(string); ok {
		return val
	}
	return defaultVal
}

func getIntArg(args map[string]interface{}, key string, defaultVal int) int {
	if val, ok := args[key].(float64); ok {
		return int(val)
	}
	if val, ok := args[key].(int); ok {
		return val
	}
	if val, ok := args[key].(string); ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func getFloatArg(args map[string]interface{}, key string, defaultVal float64) float64 {
	if val, ok := args[key].(float64); ok {
		return val
	}
	if val, ok := args[key].(string); ok {
		if parsed, err := strconv.ParseFloat(val, 64); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func getBoolArg(args map[string]interface{}, key string, defaultVal bool) bool {
	if val, ok := args[key].(bool); ok {
		return val
	}
	return defaultVal
}
