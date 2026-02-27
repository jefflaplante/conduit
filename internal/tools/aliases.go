package tools

import (
	"fmt"
	"os"
	"strings"

	"conduit/internal/tools/types"
)

// ToolConfig represents the configuration for a resolved tool.
type ToolConfig struct {
	Name            string     `json:"name"`         // The resolved tool name (versioned)
	Alias           string     `json:"alias"`        // The original alias used to look up the tool
	Version         string     `json:"version"`      // The version part of the tool name
	IsAnthropicTool bool       `json:"is_anthropic"` // Whether this is an Anthropic versioned tool
	Tool            types.Tool `json:"-"`            // The actual tool instance (not serialized)
}

// GetAnthropicTool resolves a tool alias to its current versioned implementation.
// This is the main function for getting Anthropic tools by their unversioned names.
//
// Example:
//
//	config := GetAnthropicTool("web_search")
//	// Returns ToolConfig with Name="web_search_20250305", Alias="web_search"
//
// Environment override: Set ANTHROPIC_TOOL_<NAME>_VERSION to override the default version.
// Example: ANTHROPIC_TOOL_WEB_SEARCH_VERSION=web_search_20250401
func GetAnthropicTool(alias string) (*ToolConfig, error) {
	// Check for environment override first
	if overrideTool := getToolVersionOverride(alias); overrideTool != "" {
		version := extractVersionFromToolName(overrideTool)
		return &ToolConfig{
			Name:            overrideTool,
			Alias:           alias,
			Version:         version,
			IsAnthropicTool: true,
		}, nil
	}

	// Look up the default version from our mapping
	versionedName, exists := AnthropicToolVersions[alias]
	if !exists {
		return nil, fmt.Errorf("unknown Anthropic tool alias: %s", alias)
	}

	version := extractVersionFromToolName(versionedName)
	return &ToolConfig{
		Name:            versionedName,
		Alias:           alias,
		Version:         version,
		IsAnthropicTool: true,
	}, nil
}

// GetAnthropicToolWithInstance resolves a tool alias and sets the actual tool instance.
// This is used when you have access to the registry and want the complete tool config.
func GetAnthropicToolWithInstance(alias string, registry *Registry) (*ToolConfig, error) {
	config, err := GetAnthropicTool(alias)
	if err != nil {
		return nil, err
	}

	// Try to get the actual tool instance from registry
	// Note: The registry uses internal names, not Anthropic versioned names
	// So we need to map back to the internal tool name
	internalName := getInternalToolName(alias)
	if tool, exists := registry.tools[internalName]; exists {
		config.Tool = tool
	}

	return config, nil
}

// GetAllAnthropicTools returns configurations for all available Anthropic tools.
func GetAllAnthropicTools() map[string]*ToolConfig {
	configs := make(map[string]*ToolConfig)

	for alias := range AnthropicToolVersions {
		if config, err := GetAnthropicTool(alias); err == nil {
			configs[alias] = config
		}
	}

	return configs
}

// IsAnthropicTool checks if a given tool name is an Anthropic versioned tool.
func IsAnthropicTool(toolName string) bool {
	// Check if it's a versioned tool name
	for _, versionedName := range AnthropicToolVersions {
		if toolName == versionedName {
			return true
		}
	}

	// Check if it's an alias
	_, exists := AnthropicToolVersions[toolName]
	return exists
}

// GetToolVersionOverride checks for environment variable overrides.
// Environment variable format: ANTHROPIC_TOOL_<UPPERCASE_ALIAS>_VERSION
// Example: ANTHROPIC_TOOL_WEB_SEARCH_VERSION=web_search_20250401
func getToolVersionOverride(alias string) string {
	// Convert alias to uppercase and replace special chars
	envKey := "ANTHROPIC_TOOL_" + strings.ToUpper(strings.ReplaceAll(alias, "_", "_")) + "_VERSION"
	return os.Getenv(envKey)
}

// extractVersionFromToolName extracts the version suffix from a versioned tool name.
// Example: "web_search_20250305" -> "20250305"
// Only returns the last part if it looks like a version (8 digits for YYYYMMDD format)
func extractVersionFromToolName(toolName string) string {
	parts := strings.Split(toolName, "_")
	if len(parts) >= 2 {
		lastPart := parts[len(parts)-1]
		// Check if the last part looks like a date version (8 digits)
		if len(lastPart) == 8 && isNumeric(lastPart) {
			return lastPart
		}
	}
	return ""
}

// isNumeric checks if a string contains only numeric characters
func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// getInternalToolName maps Anthropic tool aliases to Conduit internal tool names.
// This is needed because our internal registry uses different names than Anthropic's API.
func getInternalToolName(alias string) string {
	switch alias {
	case "web_search":
		return "WebSearch"
	case "web_fetch":
		return "WebFetch"
	case "computer_use":
		return "Computer" // Future implementation
	case "text_editor":
		return "TextEditor" // Future implementation
	case "bash":
		return "Bash" // We have this but it's called "Bash" internally
	default:
		// Default to the alias if no mapping is found
		return alias
	}
}

// ValidateToolConfig checks if a tool configuration is valid and properly resolved.
func ValidateToolConfig(config *ToolConfig) error {
	if config == nil {
		return fmt.Errorf("tool config is nil")
	}

	if config.Name == "" {
		return fmt.Errorf("tool name is empty")
	}

	if config.Alias == "" {
		return fmt.Errorf("tool alias is empty")
	}

	if config.IsAnthropicTool && config.Version == "" {
		return fmt.Errorf("Anthropic tool missing version")
	}

	return nil
}

// GetToolVersionHistory returns the version history for a given tool alias.
func GetToolVersionHistory(alias string) ([]ToolVersionInfo, error) {
	history, exists := ToolVersionHistory[alias]
	if !exists {
		return nil, fmt.Errorf("no version history found for tool: %s", alias)
	}

	return history, nil
}

// GetCurrentToolVersion returns the current version string for a tool alias.
func GetCurrentToolVersion(alias string) (string, error) {
	config, err := GetAnthropicTool(alias)
	if err != nil {
		return "", err
	}

	return config.Version, nil
}

// ListAvailableAliases returns a list of all available tool aliases.
func ListAvailableAliases() []string {
	aliases := make([]string, 0, len(AnthropicToolVersions))
	for alias := range AnthropicToolVersions {
		aliases = append(aliases, alias)
	}
	return aliases
}
