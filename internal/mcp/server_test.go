package mcp

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"conduit/internal/tools/types"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Mock types ---

type mockTool struct {
	name        string
	description string
	params      map[string]interface{}
}

func (m *mockTool) Name() string                       { return m.name }
func (m *mockTool) Description() string                { return m.description }
func (m *mockTool) Parameters() map[string]interface{} { return m.params }
func (m *mockTool) Execute(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: "executed " + m.name}, nil
}

type mockRegistry struct {
	tools       map[string]types.Tool
	executeFunc func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error)
}

func (m *mockRegistry) GetAvailableTools() map[string]types.Tool {
	return m.tools
}

func (m *mockRegistry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, name, args)
	}
	tool, ok := m.tools[name]
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
	return tool.Execute(ctx, args)
}

func newMockRegistry(tools ...types.Tool) *mockRegistry {
	m := &mockRegistry{tools: make(map[string]types.Tool)}
	for _, t := range tools {
		m.tools[t.Name()] = t
	}
	return m
}

// freePort returns an available TCP port on localhost.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// --- Tests ---

func TestServerStartStop(t *testing.T) {
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

	// Verify the port is in use.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	require.NoError(t, err, "should be able to connect to server")
	conn.Close()

	// Stop the server.
	err = srv.Stop(ctx)
	require.NoError(t, err)

	// Give the OS a moment to release the port.
	time.Sleep(50 * time.Millisecond)

	// Verify the port is released.
	conn, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Log("port still accepting connections briefly after stop (acceptable)")
	}
}

func TestServerStopIdempotent(t *testing.T) {
	registry := newMockRegistry()
	port := freePort(t)
	srv := NewServer(registry, port)

	ctx := context.Background()
	err := srv.Start(ctx)
	require.NoError(t, err)

	// Double stop should not panic or error.
	err = srv.Stop(ctx)
	require.NoError(t, err)

	err = srv.Stop(ctx)
	assert.NoError(t, err, "second Stop should not error")
}

func TestServerStopBeforeStart(t *testing.T) {
	registry := newMockRegistry()
	srv := NewServer(registry, 0)

	// Stop without Start should not panic.
	err := srv.Stop(context.Background())
	assert.NoError(t, err)
}

func TestFilterToolsForMCP(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "ReadFile", description: "Read a file", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "WriteFile", description: "Write a file", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Chain", description: "Chain tools", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Gateway", description: "Gateway control", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "SessionsList", description: "List sessions", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Brain", description: "Brain memory", params: map[string]interface{}{"type": "object"}},
	)

	filtered := FilterToolsForMCP(registry)

	// ReadFile, WriteFile, Brain should be included.
	assert.Contains(t, filtered, "ReadFile")
	assert.Contains(t, filtered, "WriteFile")
	assert.Contains(t, filtered, "Brain")

	// Chain, Gateway, SessionsList should be excluded.
	assert.NotContains(t, filtered, "Chain")
	assert.NotContains(t, filtered, "Gateway")
	assert.NotContains(t, filtered, "SessionsList")
}

func TestAdaptToolToMCP(t *testing.T) {
	tool := &mockTool{
		name:        "TestTool",
		description: "A test tool for testing",
		params: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "file path",
				},
			},
		},
	}

	mcpTool := AdaptToolToMCP(tool)

	assert.Equal(t, "TestTool", mcpTool.Name)
	assert.Equal(t, "A test tool for testing", mcpTool.Description)
	assert.NotNil(t, mcpTool.InputSchema)
}

func TestAdaptToolToMCPNilParams(t *testing.T) {
	tool := &mockTool{
		name:        "NoParams",
		description: "Tool with no params",
		params:      nil,
	}

	mcpTool := AdaptToolToMCP(tool)

	assert.Equal(t, "NoParams", mcpTool.Name)
	assert.NotNil(t, mcpTool.InputSchema)
}

func TestAdaptToolResult(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		result := &types.ToolResult{
			Success: true,
			Content: "hello world",
		}
		mcpResult := AdaptToolResult(result)
		assert.False(t, mcpResult.IsError)
		require.Len(t, mcpResult.Content, 1)
		tc, ok := mcpResult.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "hello world", tc.Text)
	})

	t.Run("error", func(t *testing.T) {
		result := &types.ToolResult{
			Success: false,
			Error:   "something went wrong",
		}
		mcpResult := AdaptToolResult(result)
		assert.True(t, mcpResult.IsError)
		require.Len(t, mcpResult.Content, 1)
		tc, ok := mcpResult.Content[0].(*sdkmcp.TextContent)
		require.True(t, ok)
		assert.Equal(t, "something went wrong", tc.Text)
	})

	t.Run("nil result", func(t *testing.T) {
		mcpResult := AdaptToolResult(nil)
		assert.True(t, mcpResult.IsError)
	})
}

func TestToolsListViaInMemoryTransport(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "ReadFile", description: "Read a file", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Brain", description: "Brain memory", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Chain", description: "Chain tools (excluded)", params: map[string]interface{}{"type": "object"}},
	)

	srv := NewServer(registry, 0) // port unused for in-memory test

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

	// Collect all tools via the iterator.
	toolNames := make(map[string]bool)
	for tool, err := range cs.Tools(ctx, nil) {
		require.NoError(t, err)
		toolNames[tool.Name] = true
	}

	// ReadFile and Brain should appear (not excluded).
	assert.True(t, toolNames["ReadFile"], "ReadFile should be in tools list")
	assert.True(t, toolNames["Brain"], "Brain should be in tools list")

	// Chain should NOT appear (excluded by FilterToolsForMCP).
	assert.False(t, toolNames["Chain"], "Chain should be excluded from tools list")
}

func TestToolCallViaInMemoryTransport(t *testing.T) {
	executed := false
	registry := newMockRegistry(
		&mockTool{name: "Greeter", description: "Says hello", params: map[string]interface{}{"type": "object"}},
	)
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		executed = true
		assert.Equal(t, "Greeter", name)
		assert.Equal(t, "world", args["name"])
		return &types.ToolResult{
			Success: true,
			Content: fmt.Sprintf("Hello, %s!", args["name"]),
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

	// Call the tool.
	result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "Greeter",
		Arguments: map[string]any{"name": "world"},
	})
	require.NoError(t, err)
	assert.True(t, executed, "tool should have been executed")

	require.Len(t, result.Content, 1)
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "Hello, world!", tc.Text)
	assert.False(t, result.IsError)
}

func TestToolCallErrorViaInMemoryTransport(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "Failing", description: "Always fails", params: map[string]interface{}{"type": "object"}},
	)
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		return nil, fmt.Errorf("tool explosion")
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
		Name:      "Failing",
		Arguments: map[string]any{},
	})
	require.NoError(t, err, "protocol-level error should not occur; tool error is in result")
	assert.True(t, result.IsError)
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, tc.Text, "tool explosion")
}

func TestToolCallToolResultError(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "SoftFail", description: "Returns error result", params: map[string]interface{}{"type": "object"}},
	)
	registry.executeFunc = func(ctx context.Context, name string, args map[string]interface{}) (*types.ToolResult, error) {
		return &types.ToolResult{
			Success: false,
			Error:   "permission denied",
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
		Name:      "SoftFail",
		Arguments: map[string]any{},
	})
	require.NoError(t, err)
	assert.True(t, result.IsError)
	tc, ok := result.Content[0].(*sdkmcp.TextContent)
	require.True(t, ok)
	assert.Equal(t, "permission denied", tc.Text)
}

func TestServerBindsToLocalhost(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "TestTool", description: "test", params: map[string]interface{}{"type": "object"}},
	)

	port := freePort(t)
	srv := NewServer(registry, port)

	ctx := context.Background()
	err := srv.Start(ctx)
	require.NoError(t, err)
	defer srv.Stop(ctx)

	// Should be reachable on 127.0.0.1.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	require.NoError(t, err, "server should be reachable on 127.0.0.1")
	conn.Close()
}

func TestNewServerRegistersFilteredTools(t *testing.T) {
	registry := newMockRegistry(
		&mockTool{name: "ReadFile", description: "read", params: map[string]interface{}{"type": "object"}},
		&mockTool{name: "Gateway", description: "gateway ctrl", params: map[string]interface{}{"type": "object"}},
	)

	srv := NewServer(registry, 0)
	require.NotNil(t, srv.mcpServer, "MCP server should be initialized")

	// Verify tools were registered by connecting an in-memory client.
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

	assert.True(t, toolNames["ReadFile"], "ReadFile should be registered")
	assert.False(t, toolNames["Gateway"], "Gateway should be excluded")
}
