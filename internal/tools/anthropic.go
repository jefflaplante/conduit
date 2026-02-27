package tools

// Anthropic tool constants with version suffixes.
// These constants centralize the versioned tool names that Anthropic uses in their API.
// When Anthropic releases new tool versions, we only need to update these constants.

const (
	// Web search tools
	// AnthropicWebSearchTool represents the web search tool with version.
	// Introduced: 2025-03-05 - Brave Search integration with regional support
	AnthropicWebSearchTool = "web_search_20250305"

	// Web fetch tools
	// AnthropicWebFetchTool represents the web content fetching tool with version.
	// Introduced: 2025-03-05 - HTML content extraction and markdown conversion
	AnthropicWebFetchTool = "web_fetch_20250305"

	// Computer use tools (for future use)
	// AnthropicComputerUseTool represents the computer use tool with version.
	// Note: Not yet implemented in Conduit, placeholder for future integration
	// AnthropicComputerUseTool = "computer_20241022"

	// Text editor tools (for future use)
	// AnthropicTextEditorTool represents the text editor tool with version.
	// Note: Not yet implemented in Conduit, placeholder for future integration
	// AnthropicTextEditorTool = "text_editor_20241022"

	// Bash tools (for future use)
	// AnthropicBashTool represents the bash execution tool with version.
	// Note: Not yet implemented in Conduit, placeholder for future integration
	// AnthropicBashTool = "bash_20241022"
)

// Legacy tool names for backward compatibility.
// These are the unversioned names that might be used in configuration files
// or by external systems that haven't been updated to use the aliasing system.
const (
	LegacyWebSearchTool  = "web_search"
	LegacyWebFetchTool   = "web_fetch"
	LegacyComputerTool   = "computer_use"
	LegacyTextEditorTool = "text_editor"
	LegacyBashTool       = "bash"
)

// AnthropicToolVersions maps tool aliases to their current versioned names.
// This map is used by the aliasing system to resolve unversioned tool names
// to their current Anthropic API versions.
var AnthropicToolVersions = map[string]string{
	"web_search": AnthropicWebSearchTool,
	"web_fetch":  AnthropicWebFetchTool,
	// Future tools - uncomment when implemented
	// "computer_use": AnthropicComputerUseTool,
	// "text_editor":  AnthropicTextEditorTool,
	// "bash":         AnthropicBashTool,
}

// ToolVersionHistory tracks the evolution of Anthropic tool versions.
// This helps with debugging and understanding which version was introduced when.
var ToolVersionHistory = map[string][]ToolVersionInfo{
	"web_search": {
		{
			Version:     "web_search_20250305",
			Introduced:  "2025-03-05",
			Description: "Initial web search implementation with Brave Search API and regional support",
			Changes:     []string{"Brave Search integration", "Country/region filtering", "Freshness filtering", "Language support"},
		},
	},
	"web_fetch": {
		{
			Version:     "web_fetch_20250305",
			Introduced:  "2025-03-05",
			Description: "Initial web content fetching with HTML to markdown conversion",
			Changes:     []string{"URL content extraction", "Markdown conversion", "Content filtering"},
		},
	},
}

// ToolVersionInfo represents metadata about a specific tool version.
type ToolVersionInfo struct {
	Version     string   `json:"version"`     // Full versioned tool name
	Introduced  string   `json:"introduced"`  // Date when this version was introduced (YYYY-MM-DD)
	Description string   `json:"description"` // Human-readable description of this version
	Changes     []string `json:"changes"`     // List of changes/features in this version
}
