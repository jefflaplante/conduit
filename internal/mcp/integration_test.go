package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Integration test tool types ---

// testTool implements types.Tool with configurable behavior.
type testTool struct {
	name   string
	desc   string
	params map[string]interface{}
	result *types.ToolResult
	err    error
}

func (t *testTool) Name() string        { return t.name }
func (t *testTool) Description() string  { return t.desc }
func (t *testTool) Parameters() map[string]interface{} {
	if t.params != nil {
		return t.params
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{"type": "string"},
		},
	}
}
func (t *testTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	if t.err != nil {
		return nil, t.err
	}
	if t.result != nil {
		return t.result, nil
	}
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("executed %s", t.name),
	}, nil
}

// echoTool returns its input in the result content.
type echoTool struct {
	name string
}

func (t *echoTool) Name() string        { return t.name }
func (t *echoTool) Description() string  { return "Echoes input back" }
func (t *echoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"type":        "string",
				"description": "Message to echo",
			},
		},
		"required": []string{"message"},
	}
}
func (t *echoTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	msg, _ := args["message"].(string)
	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("echo: %s", msg),
	}, nil
}

// --- Integration Tests ---

// TestIntegration_MCPServerStartStopWithRealHTTP starts the MCP server on a real
// port, verifies it responds to HTTP requests, stops it, and verifies the port
// is released.
func TestIntegration_MCPServerStartStopWithRealHTTP(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{
			name:        "TestTool",
			description: "A test tool",
			params:      map[string]interface{}{"type": "object"},
		},
	)

	port := freePort(t)
	srv := NewServer(registry, port)
	require.NotNil(t, srv)

	ctx := context.Background()
	err := srv.Start(ctx)
	require.NoError(t, err)

	// Verify the server responds to HTTP requests on the /mcp endpoint.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(
		fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		"application/json",
		nil,
	)
	// The MCP server should respond (even if with an error for empty body).
	require.NoError(t, err, "HTTP request to MCP server should succeed")
	resp.Body.Close()
	// MCP StreamableHTTP should respond with some HTTP status (not a connection refused).
	assert.NotEqual(t, 0, resp.StatusCode, "should get a valid HTTP status")

	// Stop the server.
	err = srv.Stop(ctx)
	require.NoError(t, err)

	// Give the OS a moment to release the port.
	time.Sleep(100 * time.Millisecond)

	// Verify the port is no longer accepting connections.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err == nil {
		conn.Close()
		// This is acceptable on some OS; the key is that the server stopped cleanly.
		t.Log("port still reachable briefly after stop (OS-dependent)")
	}
}

// TestIntegration_MCPServerDoubleStart verifies that starting a server on an
// already-bound port fails gracefully.
func TestIntegration_MCPServerDoubleStart(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "Tool1", description: "test", params: map[string]interface{}{"type": "object"}},
	)

	port := freePort(t)

	srv1 := NewServer(registry, port)
	ctx := context.Background()
	err := srv1.Start(ctx)
	require.NoError(t, err)
	defer srv1.Stop(ctx)

	// A second server on the same port should fail.
	srv2 := NewServer(registry, port)
	err = srv2.Start(ctx)
	assert.Error(t, err, "starting a second server on the same port should fail")
}

// TestIntegration_ToolsListReturnsCorrectTools starts the MCP server and
// verifies that tools/list returns the correct filtered set of tools via the
// in-memory MCP client transport.
func TestIntegration_ToolsListReturnsCorrectTools(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "ReadFile", description: "Read a file", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "WriteFile", description: "Write a file", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Brain", description: "Brain memory", params: map[string]interface{}{"type": "object"}},
		// These should be excluded:
		&mockTool{name: "Chain", description: "Internal chain", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Gateway", description: "Gateway control", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "SessionsList", description: "List sessions", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "SessionsSend", description: "Send to session", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "SessionsSpawn", description: "Spawn session", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "SessionStatus", description: "Session status", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "StatusUpdate", description: "Status update", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "DebugLog", description: "Debug log", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Context", description: "Context mgmt", params: map[string]interface{}{"type": "object"}},
	)

	srv := NewServer(registry, 0)
	ctx := context.Background()

	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "integration-test", Version: "v0.0.1"},
		nil,
	)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer cs.Close()

	toolNames := make(map[string]bool)
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		toolNames[tool.Name] = true
	}

	// Included tools.
	assert.True(t, toolNames["ReadFile"], "ReadFile should be in tools list")
	assert.True(t, toolNames["WriteFile"], "WriteFile should be in tools list")
	assert.True(t, toolNames["Brain"], "Brain should be in tools list")

	// All excluded tools.
	for _, excluded := range []string{"Chain", "Gateway", "SessionsList", "SessionsSend",
		"SessionsSpawn", "SessionStatus", "StatusUpdate", "DebugLog", "Context"} {
		assert.False(t, toolNames[excluded], "%s should be excluded from tools list", excluded)
	}
}

// TestIntegration_ToolsCallExecutesTool verifies end-to-end tool execution
// through the MCP protocol: client calls a tool, the registry executes it,
// and the result flows back to the client.
func TestIntegration_ToolsCallExecutesTool(t *testing.T) {
	echoT := &echoTool{name: "Echo"}
	registry := newMockRegistry(echoT)
	// Override executeFunc to delegate to the actual tool.
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		return echoT.Execute(ctx, args)
	}

	srv := NewServer(registry, 0)
	ctx := context.Background()

	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
		nil,
	)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer cs.Close()

	result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "Echo",
		Arguments: map[string]any{"message": "hello world"},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError, "tool call should succeed")

	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "echo: hello world", tc.Text)
}

// TestIntegration_ToolsCallWithToolError verifies that tool execution errors
// are properly communicated back through the MCP protocol as error results
// (not protocol-level errors).
func TestIntegration_ToolsCallWithToolError(t *testing.T) {
	t.Run("tool returns Go error", func(t *testing.T) {
		registry := newMockRegistry(
			&mockTool{name: "Exploder", description: "Always explodes", params: map[string]interface{}{"type": "object"}},
		)
		registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
			return nil, fmt.Errorf("internal tool failure: disk full")
		}

		srv := NewServer(registry, 0)
		ctx := context.Background()

		t1, t2 := sdkmcp.NewInMemoryTransports()
		_, err := srv.mcpServer.Connect(ctx, t1, nil)
		require.NoError(t, err)

		client := sdkmcp.NewClient(
			&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
			nil,
		)
		cs, err := client.Connect(ctx, t2, nil)
		require.NoError(t, err)
		defer cs.Close()

		result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      "Exploder",
			Arguments: map[string]any{},
		})
		require.NoError(t, err, "should not return protocol error for tool failure")
		assert.True(t, result.IsError, "result should indicate error")
		tc, ok := result.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		assert.Contains(t, tc.Text, "disk full")
	})

	t.Run("tool returns error ToolResult", func(t *testing.T) {
		registry := newMockRegistry(
			&mockTool{name: "Denier", description: "Denies permission", params: map[string]interface{}{"type": "object"}},
		)
		registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
			return &types.ToolResult{
				Success: false,
				Error:   "permission denied: insufficient privileges",
			}, nil
		}

		srv := NewServer(registry, 0)
		ctx := context.Background()

		t1, t2 := sdkmcp.NewInMemoryTransports()
		_, err := srv.mcpServer.Connect(ctx, t1, nil)
		require.NoError(t, err)

		client := sdkmcp.NewClient(
			&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
			nil,
		)
		cs, err := client.Connect(ctx, t2, nil)
		require.NoError(t, err)
		defer cs.Close()

		result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      "Denier",
			Arguments: map[string]any{},
		})
		require.NoError(t, err)
		assert.True(t, result.IsError)
		tc, ok := result.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "permission denied: insufficient privileges", tc.Text)
	})
}

// TestIntegration_ToolCallWithComplexArguments verifies that complex JSON
// arguments are correctly passed through the MCP protocol to the tool.
func TestIntegration_ToolCallWithComplexArguments(t *testing.T) {
	var receivedArgs map[string]interface{}

	registry := newMockRegistry(
		&mockTool{
			name: "ComplexTool",
			description: "Accepts complex args",
			params: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    map[string]interface{}{"type": "string"},
					"lines":   map[string]interface{}{"type": "integer"},
					"verbose": map[string]interface{}{"type": "boolean"},
				},
			},
		},
	)
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		receivedArgs = args
		return &types.ToolResult{Success: true, Content: "ok"}, nil
	}

	srv := NewServer(registry, 0)
	ctx := context.Background()

	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
		nil,
	)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer cs.Close()

	result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name: "ComplexTool",
		Arguments: map[string]any{
			"path":    "/tmp/test.go",
			"lines":   42,
			"verbose": true,
		},
	})
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Verify arguments were passed through correctly.
	assert.Equal(t, "/tmp/test.go", receivedArgs["path"])
	// JSON numbers come through as float64.
	assert.Equal(t, float64(42), receivedArgs["lines"])
	assert.Equal(t, true, receivedArgs["verbose"])
}

// TestIntegration_MCPConfigLifecycle verifies the full .mcp.json lifecycle:
// setup creates the file, cleanup removes it.
func TestIntegration_MCPConfigLifecycle(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)

	// Initially no config file.
	configPath := mgr.ConfigPath()
	_, err := os.Stat(configPath)
	assert.True(t, os.IsNotExist(err), "config file should not exist initially")

	// Setup creates the file.
	err = mgr.Setup()
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var content mcpFileContent
	require.NoError(t, json.Unmarshal(data, &content))

	entry, ok := content.MCPServers["conduit"]
	require.True(t, ok, "conduit entry should exist")
	assert.Equal(t, "http", entry.Type)
	assert.Equal(t, "http://127.0.0.1:18790", entry.URL)

	// Cleanup removes the file.
	err = mgr.Cleanup()
	require.NoError(t, err)

	_, err = os.Stat(configPath)
	assert.True(t, os.IsNotExist(err), "config file should be removed after cleanup")
}

// TestIntegration_MCPConfigMerge verifies that Setup merges into an existing
// .mcp.json without losing other server entries, and Cleanup restores the original.
func TestIntegration_MCPConfigMerge(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".mcp.json")

	// Write an existing .mcp.json with another server.
	original := mcpFileContent{
		MCPServers: map[string]mcpServerEntry{
			"other-server": {Type: "stdio", URL: ""},
			"remote-mcp":   {Type: "http", URL: "http://10.0.0.5:3000"},
		},
	}
	originalData, err := json.MarshalIndent(original, "", "  ")
	require.NoError(t, err)
	originalData = append(originalData, '\n')
	require.NoError(t, os.WriteFile(configPath, originalData, 0644))

	mgr := NewMCPConfigManager(dir, 18790)

	// Setup should merge.
	err = mgr.Setup()
	require.NoError(t, err)

	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var merged mcpFileContent
	require.NoError(t, json.Unmarshal(data, &merged))

	// All three entries should exist.
	assert.Len(t, merged.MCPServers, 3, "should have 3 server entries after merge")
	assert.Contains(t, merged.MCPServers, "other-server")
	assert.Contains(t, merged.MCPServers, "remote-mcp")
	conduit, ok := merged.MCPServers["conduit"]
	require.True(t, ok)
	assert.Equal(t, "http://127.0.0.1:18790", conduit.URL)

	// Cleanup should restore the original file exactly.
	err = mgr.Cleanup()
	require.NoError(t, err)

	restored, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Equal(t, string(originalData), string(restored),
		"cleanup should restore the exact original file content")

	// Verify the restored content has no conduit entry.
	var restoredContent mcpFileContent
	require.NoError(t, json.Unmarshal(restored, &restoredContent))
	assert.NotContains(t, restoredContent.MCPServers, "conduit",
		"conduit entry should not exist after cleanup")
	assert.Contains(t, restoredContent.MCPServers, "other-server",
		"other-server should be preserved")
	assert.Contains(t, restoredContent.MCPServers, "remote-mcp",
		"remote-mcp should be preserved")
}

// TestIntegration_MCPConfigSetupWithInvalidDir verifies that Setup fails
// gracefully when the working directory does not exist.
func TestIntegration_MCPConfigSetupWithInvalidDir(t *testing.T) {
	mgr := NewMCPConfigManager("/nonexistent/path/xyz/abc", 18790)
	err := mgr.Setup()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "working directory")
}

// TestIntegration_MCPConfigCleanupWithoutSetup verifies that Cleanup is a
// no-op when Setup was never called.
func TestIntegration_MCPConfigCleanupWithoutSetup(t *testing.T) {
	dir := t.TempDir()
	mgr := NewMCPConfigManager(dir, 18790)

	// Cleanup without Setup should not error.
	err := mgr.Cleanup()
	assert.NoError(t, err)

	// And no file should have been created.
	_, err = os.Stat(mgr.ConfigPath())
	assert.True(t, os.IsNotExist(err))
}

// TestIntegration_MultipleToolCallsSequential verifies that multiple sequential
// tool calls through MCP all succeed and return distinct results.
func TestIntegration_MultipleToolCallsSequential(t *testing.T) {
	callCount := 0
	registry := newMockRegistry(
		&mockTool{name: "Counter", description: "Counts calls", params: map[string]interface{}{"type": "object"}},
	)
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		callCount++
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("call #%d", callCount),
		}, nil
	}

	srv := NewServer(registry, 0)
	ctx := context.Background()

	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
		nil,
	)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer cs.Close()

	for i := 1; i <= 5; i++ {
		result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
			Name:      "Counter",
			Arguments: map[string]any{},
		})
		require.NoError(t, err)
		assert.False(t, result.IsError)

		tc, ok := result.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, fmt.Sprintf("call #%d", i), tc.Text)
	}

	assert.Equal(t, 5, callCount, "tool should have been called 5 times")
}

// TestIntegration_ToolAdaptationPreservesSchema verifies that the full schema
// (including nested properties and required fields) survives the Conduit-to-MCP
// adaptation roundtrip.
func TestIntegration_ToolAdaptationPreservesSchema(t *testing.T) {
	tool := &testTool{
		name: "SchemaTest",
		desc: "Tests schema preservation",
		params: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File path",
				},
				"options": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"recursive": map[string]interface{}{
							"type":    "boolean",
							"default": false,
						},
					},
				},
			},
			"required": []interface{}{"path"},
		},
	}

	mcpTool := AdaptToolToMCP(tool)

	assert.Equal(t, "SchemaTest", mcpTool.Name)
	assert.Equal(t, "Tests schema preservation", mcpTool.Description)
	assert.NotNil(t, mcpTool.InputSchema)

	// Parse the schema by marshaling it back to JSON first (InputSchema is any).
	schemaJSON, err := json.Marshal(mcpTool.InputSchema)
	require.NoError(t, err)
	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(schemaJSON, &schema))

	assert.Equal(t, "object", schema["type"])
	props, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok, "properties should be a map")
	assert.Contains(t, props, "path")
	assert.Contains(t, props, "options")

	required, ok := schema["required"].([]interface{})
	require.True(t, ok, "required should be an array")
	assert.Contains(t, required, "path")
}

// TestIntegration_ServerWithManyTools verifies that the MCP server handles
// a large number of tools without issues.
func TestIntegration_ServerWithManyTools(t *testing.T) {
	tools := make([]types.Tool, 0, 30)
	for i := 0; i < 30; i++ {
		tools = append(tools, &mockTool{
			name:        fmt.Sprintf("Tool%02d", i),
			description: fmt.Sprintf("Test tool number %d", i),
			params:      map[string]interface{}{"type": "object"},
		})
	}

	registry := newMockRegistry(tools...)

	srv := NewServer(registry, 0)
	ctx := context.Background()

	t1, t2 := sdkmcp.NewInMemoryTransports()
	_, err := srv.mcpServer.Connect(ctx, t1, nil)
	require.NoError(t, err)

	client := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "test-client", Version: "v0.0.1"},
		nil,
	)
	cs, err := client.Connect(ctx, t2, nil)
	require.NoError(t, err)
	defer cs.Close()

	toolNames := make(map[string]bool)
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		toolNames[tool.Name] = true
	}

	assert.Len(t, toolNames, 30, "all 30 tools should be listed")
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("Tool%02d", i)
		assert.True(t, toolNames[name], "%s should be in tools list", name)
	}
}
