// Package mcp provides adapters between Conduit's internal tool system and the
// Model Context Protocol (MCP). It converts Conduit tools to MCP tool
// definitions and translates tool execution results back to MCP format.
package mcp

import (
	"encoding/json"
	"fmt"

	"conduit/internal/tools/types"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolsToExclude lists tool names that Claude Code already has natively.
// These should NOT be exposed via MCP to avoid duplication.
var ToolsToExclude = map[string]bool{
	"ReadFile":  true,
	"WriteFile": true,
	"EditFile":  true,
	"Bash":      true,
	"Glob":      true,
	"Find":      true,
}

// AdaptToolToMCP converts a Conduit Tool to an MCP tool definition.
// It maps Name() to name, Description() to description, and Parameters()
// to inputSchema.
func AdaptToolToMCP(tool types.Tool) *mcp.Tool {
	inputSchema := tool.Parameters()
	if inputSchema == nil {
		inputSchema = map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}

	return &mcp.Tool{
		Name:        tool.Name(),
		Description: tool.Description(),
		InputSchema: inputSchema,
	}
}

// AdaptAllToolsToMCP converts a map of Conduit tools to a slice of MCP tool
// definitions.
func AdaptAllToolsToMCP(tools map[string]types.Tool) []*mcp.Tool {
	result := make([]*mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, AdaptToolToMCP(tool))
	}
	return result
}

// AdaptToolResult converts a Conduit ToolResult to an MCP CallToolResult.
// MCP expects content as a slice of Content blocks and an IsError flag.
// Successful results use result.Content as text; failed results use
// result.Error as text with IsError set to true.
func AdaptToolResult(result *types.ToolResult) *mcp.CallToolResult {
	if result == nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "no result"},
			},
			IsError: true,
		}
	}

	if !result.Success {
		errText := result.Error
		if errText == "" {
			errText = "unknown error"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: errText},
			},
			IsError: true,
		}
	}

	text := result.Content

	// If the result has structured data, include it as JSON in the text output.
	if len(result.Data) > 0 && text == "" {
		data, err := json.Marshal(result.Data)
		if err == nil {
			text = string(data)
		} else {
			text = fmt.Sprintf("result data: %v", result.Data)
		}
	}

	if text == "" {
		text = "ok"
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
		IsError: false,
	}
}

// FilterToolsForMCP takes the full tool registry and returns only tools
// that should be exposed via MCP (excludes Claude Code native equivalents).
func FilterToolsForMCP(registry types.ToolRegistry) map[string]types.Tool {
	all := registry.GetAvailableTools()
	filtered := make(map[string]types.Tool)
	for name, tool := range all {
		if !ToolsToExclude[name] {
			filtered[name] = tool
		}
	}
	return filtered
}
