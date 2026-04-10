package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mockTool implements types.Tool for testing.
type mockTool struct {
	name   string
	desc   string
	params map[string]interface{}
}

func (m *mockTool) Name() string                      { return m.name }
func (m *mockTool) Description() string               { return m.desc }
func (m *mockTool) Parameters() map[string]interface{} { return m.params }
func (m *mockTool) Execute(_ context.Context, _ map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

// mockRegistry implements types.ToolRegistry for testing.
type mockRegistry struct {
	tools map[string]types.Tool
}

func (r *mockRegistry) GetAvailableTools() map[string]types.Tool {
	return r.tools
}

func (r *mockRegistry) ExecuteTool(_ context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
	tool, ok := r.tools[name]
	if !ok {
		return &types.ToolResult{Success: false, Error: "tool not found: " + name}, nil
	}
	return tool.Execute(context.Background(), args)
}

func TestAdaptToolToMCP(t *testing.T) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "The search query",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum results to return",
			},
		},
		"required": []string{"query"},
	}

	tool := &mockTool{
		name:   "WebSearch",
		desc:   "Search the web for information",
		params: params,
	}

	mcpTool := AdaptToolToMCP(tool)

	assert.Equal(t, "WebSearch", mcpTool.Name)
	assert.Equal(t, "Search the web for information", mcpTool.Description)

	// Verify the input schema was passed through correctly by round-tripping via JSON.
	schemaJSON, err := json.Marshal(mcpTool.InputSchema)
	require.NoError(t, err)
	var schemaMap map[string]interface{}
	require.NoError(t, json.Unmarshal(schemaJSON, &schemaMap))
	assert.Equal(t, "object", schemaMap["type"])
	props, ok := schemaMap["properties"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, props, "query")
	assert.Contains(t, props, "limit")
}

func TestAdaptToolToMCP_NilParams(t *testing.T) {
	tool := &mockTool{
		name:   "SimpleTool",
		desc:   "A tool with no parameters",
		params: nil,
	}

	mcpTool := AdaptToolToMCP(tool)

	assert.Equal(t, "SimpleTool", mcpTool.Name)
	// Should get a default empty object schema.
	schemaJSON, err := json.Marshal(mcpTool.InputSchema)
	require.NoError(t, err)
	var schemaMap map[string]interface{}
	require.NoError(t, json.Unmarshal(schemaJSON, &schemaMap))
	assert.Equal(t, "object", schemaMap["type"])
}

func TestAdaptToolResult_Success(t *testing.T) {
	result := &types.ToolResult{
		Success: true,
		Content: "Found 3 results for your query",
	}

	mcpResult := AdaptToolResult(result)

	assert.False(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Found 3 results for your query", textContent.Text)
}

func TestAdaptToolResult_SuccessWithData(t *testing.T) {
	result := &types.ToolResult{
		Success: true,
		Content: "",
		Data: map[string]interface{}{
			"count": 42,
			"items": []string{"a", "b"},
		},
	}

	mcpResult := AdaptToolResult(result)

	assert.False(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	// Should contain JSON of the data.
	assert.Contains(t, textContent.Text, "count")
	assert.Contains(t, textContent.Text, "42")
}

func TestAdaptToolResult_SuccessEmptyContent(t *testing.T) {
	result := &types.ToolResult{
		Success: true,
		Content: "",
	}

	mcpResult := AdaptToolResult(result)

	assert.False(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "ok", textContent.Text)
}

func TestAdaptToolResult_Error(t *testing.T) {
	result := &types.ToolResult{
		Success: false,
		Error:   "permission denied: cannot access /etc/shadow",
	}

	mcpResult := AdaptToolResult(result)

	assert.True(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "permission denied: cannot access /etc/shadow", textContent.Text)
}

func TestAdaptToolResult_ErrorEmpty(t *testing.T) {
	result := &types.ToolResult{
		Success: false,
		Error:   "",
	}

	mcpResult := AdaptToolResult(result)

	assert.True(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "unknown error", textContent.Text)
}

func TestAdaptToolResult_Nil(t *testing.T) {
	mcpResult := AdaptToolResult(nil)

	assert.True(t, mcpResult.IsError)
	require.Len(t, mcpResult.Content, 1)
	textContent, ok := mcpResult.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "no result", textContent.Text)
}

func TestFilterToolsForMCP(t *testing.T) {
	registry := &mockRegistry{
		tools: map[string]types.Tool{
			// Tools that should be excluded (Claude Code native).
			"ReadFile":  &mockTool{name: "ReadFile", desc: "Read a file"},
			"WriteFile": &mockTool{name: "WriteFile", desc: "Write a file"},
			"EditFile":  &mockTool{name: "EditFile", desc: "Edit a file"},
			"Bash":      &mockTool{name: "Bash", desc: "Run bash commands"},
			"Glob":      &mockTool{name: "Glob", desc: "Glob file patterns"},
			"Find":      &mockTool{name: "Find", desc: "Find files"},
			// Tools that should pass through.
			"Brain":     &mockTool{name: "Brain", desc: "Cognitive memory"},
			"WebSearch": &mockTool{name: "WebSearch", desc: "Web search"},
			"MQTT":      &mockTool{name: "MQTT", desc: "MQTT operations"},
			"Gateway":   &mockTool{name: "Gateway", desc: "Gateway control"},
		},
	}

	filtered := FilterToolsForMCP(registry)

	// Should have exactly the domain tools.
	assert.Len(t, filtered, 4)
	assert.Contains(t, filtered, "Brain")
	assert.Contains(t, filtered, "WebSearch")
	assert.Contains(t, filtered, "MQTT")
	assert.Contains(t, filtered, "Gateway")

	// Should NOT contain any excluded tools.
	assert.NotContains(t, filtered, "ReadFile")
	assert.NotContains(t, filtered, "WriteFile")
	assert.NotContains(t, filtered, "EditFile")
	assert.NotContains(t, filtered, "Bash")
	assert.NotContains(t, filtered, "Glob")
	assert.NotContains(t, filtered, "Find")
}

func TestFilterToolsForMCP_AllExcluded(t *testing.T) {
	registry := &mockRegistry{
		tools: map[string]types.Tool{
			"ReadFile": &mockTool{name: "ReadFile"},
			"Bash":     &mockTool{name: "Bash"},
		},
	}

	filtered := FilterToolsForMCP(registry)
	assert.Empty(t, filtered)
}

func TestFilterToolsForMCP_NoneExcluded(t *testing.T) {
	registry := &mockRegistry{
		tools: map[string]types.Tool{
			"Brain":   &mockTool{name: "Brain"},
			"Gateway": &mockTool{name: "Gateway"},
		},
	}

	filtered := FilterToolsForMCP(registry)
	assert.Len(t, filtered, 2)
}

func TestToolsToExclude_Completeness(t *testing.T) {
	// Verify that the exclude list contains the expected Claude Code native tools.
	expectedExcludes := []string{"ReadFile", "WriteFile", "EditFile", "Bash", "Glob", "Find"}
	for _, name := range expectedExcludes {
		assert.True(t, ToolsToExclude[name], "expected %q to be in ToolsToExclude", name)
	}
	assert.Len(t, ToolsToExclude, len(expectedExcludes),
		"ToolsToExclude has unexpected entries")
}

func TestAdaptAllToolsToMCP(t *testing.T) {
	tools := map[string]types.Tool{
		"Brain": &mockTool{
			name: "Brain",
			desc: "Cognitive memory",
			params: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{"type": "string"},
				},
			},
		},
		"MQTT": &mockTool{
			name: "MQTT",
			desc: "MQTT operations",
			params: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}

	mcpTools := AdaptAllToolsToMCP(tools)
	assert.Len(t, mcpTools, 2)

	// Collect names for assertion (order is not guaranteed from map iteration).
	names := make(map[string]bool)
	for _, tool := range mcpTools {
		names[tool.Name] = true
	}
	assert.True(t, names["Brain"])
	assert.True(t, names["MQTT"])
}
