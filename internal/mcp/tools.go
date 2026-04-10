package mcp

import (
	"encoding/json"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpExcludedTools lists tools that should not be exposed over MCP.
// These are Conduit-internal tools that do not make sense for external callers.
var mcpExcludedTools = map[string]bool{
	"Chain":         true, // Internal orchestration
	"DebugLog":      true, // Internal debug
	"Gateway":       true, // Internal gateway control
	"SessionsList":  true, // Internal session management
	"SessionsSend":  true, // Internal session management
	"SessionsSpawn": true, // Internal session management
	"SessionStatus": true, // Internal session management
	"StatusUpdate":  true, // Internal status updates
	"Context":       true, // Internal context/prompt management
}

// FilterToolsForMCP returns the subset of registry tools suitable for MCP exposure.
// It excludes tools that are Conduit-internal and should not be called by external clients.
func FilterToolsForMCP(registry types.ToolRegistry) map[string]types.Tool {
	all := registry.GetAvailableTools()
	filtered := make(map[string]types.Tool, len(all))
	for name, tool := range all {
		if !mcpExcludedTools[name] {
			filtered[name] = tool
		}
	}
	return filtered
}

// AdaptToolToMCP converts a Conduit tool definition to an MCP SDK Tool.
// The resulting tool has its InputSchema set from the Conduit tool's Parameters().
func AdaptToolToMCP(tool types.Tool) *sdkmcp.Tool {
	params := tool.Parameters()

	// Ensure the schema has "type": "object" as required by the MCP SDK.
	if params == nil {
		params = map[string]interface{}{"type": "object"}
	}
	if _, ok := params["type"]; !ok {
		params["type"] = "object"
	}

	schemaJSON, err := json.Marshal(params)
	if err != nil {
		// Fallback to empty object schema if marshaling fails.
		schemaJSON = []byte(`{"type":"object"}`)
	}

	return &sdkmcp.Tool{
		Name:        tool.Name(),
		Description: tool.Description(),
		InputSchema: json.RawMessage(schemaJSON),
	}
}

// AdaptAllToolsToMCP converts a map of Conduit tools to MCP SDK tools.
func AdaptAllToolsToMCP(tools map[string]types.Tool) []*sdkmcp.Tool {
	result := make([]*sdkmcp.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, AdaptToolToMCP(tool))
	}
	return result
}

// AdaptToolResult converts a Conduit ToolResult to an MCP CallToolResult.
func AdaptToolResult(result *types.ToolResult) *sdkmcp.CallToolResult {
	if result == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "no result"}},
			IsError: true,
		}
	}

	mcpResult := &sdkmcp.CallToolResult{
		IsError: !result.Success,
	}

	if result.Success {
		mcpResult.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: result.Content}}
	} else {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = result.Content
		}
		if errMsg == "" {
			errMsg = "unknown error"
		}
		mcpResult.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: errMsg}}
	}

	return mcpResult
}
