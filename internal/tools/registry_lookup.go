package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"conduit/internal/tools/types"
)

// HasTool returns true if a tool is registered (compiled in and instantiated).
// Used by optional tools to check for dependencies (e.g., SRE checks for Datadog).
func (r *Registry) HasTool(name string) bool {
	_, exists := r.getTool(name)
	return exists
}

// getTool looks up a registered tool by exact name under the read lock.
func (r *Registry) getTool(name string) (types.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, exists := r.tools[name]
	return tool, exists
}

// GetAvailableTools returns a list of available tools
func (r *Registry) GetAvailableTools() map[string]types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	available := make(map[string]types.Tool)
	for name, tool := range r.tools {
		if r.isToolEnabledLocked(name) {
			available[name] = tool
		}
	}
	return available
}

// findSimilarEnabledTools returns enabled tool names that share a prefix or substring with the given name.
func (r *Registry) findSimilarEnabledTools(name string) []string {
	lower := strings.ToLower(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var similar []string
	for toolName := range r.tools {
		if r.isToolEnabledLocked(toolName) && strings.Contains(strings.ToLower(toolName), lower[:min(len(lower), 3)]) {
			similar = append(similar, toolName)
		}
	}
	sort.Strings(similar) // model-visible (tool_disabled available values)
	return similar
}

// findClosestToolName returns the enabled tool name most similar to the given name, or "".
func (r *Registry) findClosestToolName(name string) string {
	lower := strings.ToLower(name)
	best := ""
	bestScore := 0
	r.mu.RLock()
	defer r.mu.RUnlock()
	for toolName := range r.tools {
		if r.isToolEnabledLocked(toolName) {
			score := commonPrefixLen(lower, strings.ToLower(toolName))
			if score > bestScore {
				bestScore = score
				best = toolName
			}
		}
	}
	if bestScore >= 2 {
		return best
	}
	return ""
}

// commonPrefixLen returns the length of the common prefix between two strings.
func commonPrefixLen(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// GetToolSchemas returns JSON schemas for all available tools
func (r *Registry) GetToolSchemas() []map[string]interface{} {
	return r.GetToolSchemasWithContext(context.Background())
}

// GetToolSchemasWithContext returns JSON schemas for all available tools, enhanced with discovery data
func (r *Registry) GetToolSchemasWithContext(ctx context.Context) []map[string]interface{} {
	var schemas []map[string]interface{}

	available := r.GetAvailableTools()
	names := make([]string, 0, len(available))
	for name := range available {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := available[name]
		params := tool.Parameters()

		// Check if tool provides schema hints and we have a schema builder
		if r.services != nil && r.services.SchemaBuilder != nil {
			if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
				hints := enhancedTool.GetSchemaHints()
				if len(hints) > 0 {
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
	r.mu.RLock()
	tool, exists := r.tools[toolName]
	enabled := r.isToolEnabledLocked(toolName)
	r.mu.RUnlock()
	if !exists || !enabled {
		return map[string]interface{}{
			"error": fmt.Sprintf("Tool '%s' not found or not enabled", toolName),
		}
	}

	help := map[string]interface{}{
		"name":        tool.Name(),
		"description": tool.Description(),
		"parameters":  tool.Parameters(),
		"enabled":     enabled,
	}

	// Add schema hints if available
	if r.services != nil && r.services.SchemaBuilder != nil {
		if enhancedTool, ok := tool.(types.EnhancedSchemaProvider); ok {
			hints := enhancedTool.GetSchemaHints()
			if len(hints) > 0 {
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

	if _, ok := tool.(types.SelfTester); ok {
		help["supports_selftest"] = true
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
			"sessions":        {"SessionsList", "SessionsSend", "SessionsSpawn", "SessionsCancel", "SessionStatus"},
			"scheduling":      {"Cron"},
			"gateway":         {"Gateway"},
			"vision":          {"Image"},
		},
	}
}
