package mcp

import (
	"encoding/json"
	"strings"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpExcludedTools lists tools that should not be exposed over MCP.
// These are Conduit-internal tools that do not make sense for external callers.
var mcpExcludedTools = map[string]bool{
	"Chain":          true, // Internal orchestration
	"DebugLog":       true, // Internal debug
	"Gateway":        true, // Internal gateway control
	"SessionsList":   true, // Internal session management
	"SessionsSend":   true, // Internal session management
	"SessionsSpawn":  true, // Internal session management
	"SessionsCancel": true, // Internal session management (conduit-38cz)
	"SessionStatus":  true, // Internal session management
	"StatusUpdate":   true, // Internal status updates
	"Context":        true, // Internal context/prompt management
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

// AdaptToolResult converts a Conduit ToolResult to an MCP CallToolResult.
// includeData mirrors the execution engine's rule (conduit-31jg.39): Data is
// appended as "Structured data: {json}" for tools that opt in via
// IncludeDataInModelOutput, or when a successful result has no Content.
// conduit-31jg.8: Data used to be dropped entirely over MCP.
func AdaptToolResult(result *types.ToolResult, includeData bool) *sdkmcp.CallToolResult {
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
		text := result.Content
		if len(result.Data) > 0 && (includeData || strings.TrimSpace(text) == "") {
			if dataJSON, err := json.Marshal(result.Data); err == nil {
				text += "\n\nStructured data: " + string(dataJSON)
			}
		}
		mcpResult.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}
	} else {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = result.Content
		} else if out := strings.TrimSpace(result.Content); out != "" && out != strings.TrimSpace(errMsg) {
			// conduit-31jg.10: include the tool's output (e.g. stderr from a
			// failed command) alongside the error instead of dropping it.
			errMsg += "\nOutput:\n" + out
		}
		if errMsg == "" {
			errMsg = "unknown error"
		}
		mcpResult.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: errMsg}}
	}

	return mcpResult
}
