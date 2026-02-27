package tools

import (
	"context"
	"fmt"
	"testing"
	"time"

	"conduit/internal/ai"
)

// MockTool implements Tool interface for testing
type MockTool struct {
	name        string
	description string
	parameters  map[string]interface{}
	executeFunc func(ctx context.Context, args map[string]interface{}) (*ToolResult, error)
}

func (m *MockTool) Name() string {
	return m.name
}

func (m *MockTool) Description() string {
	return m.description
}

func (m *MockTool) Parameters() map[string]interface{} {
	return m.parameters
}

func (m *MockTool) Execute(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, args)
	}
	return &ToolResult{
		Success: true,
		Content: fmt.Sprintf("Mock tool %s executed with args: %v", m.name, args),
	}, nil
}

// MockRegistry implements registry for testing
type MockRegistry struct {
	tools        map[string]Tool
	enabledTools map[string]bool
}

func NewMockRegistry() *MockRegistry {
	return &MockRegistry{
		tools:        make(map[string]Tool),
		enabledTools: make(map[string]bool),
	}
}

func (m *MockRegistry) AddTool(tool Tool) {
	m.tools[tool.Name()] = tool
	m.enabledTools[tool.Name()] = true
}

func (m *MockRegistry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error) {
	if !m.enabledTools[name] {
		return &ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool '%s' is not enabled", name),
		}, nil
	}

	tool, exists := m.tools[name]
	if !exists {
		return &ToolResult{
			Success: false,
			Error:   fmt.Sprintf("tool '%s' not found", name),
		}, nil
	}

	return tool.Execute(ctx, args)
}

// Test ExecutionEngine single tool execution
func TestExecutionEngine_ExecuteSingle(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	call := ai.ToolCall{
		ID:   "test_1",
		Name: "test_tool",
		Args: map[string]interface{}{"param1": "value1"},
	}

	result := engine.executeSingle(context.Background(), call)

	if result.Error != nil {
		t.Fatalf("Expected no error, got: %v", result.Error)
	}

	if result.Result == nil {
		t.Fatal("Expected result, got nil")
	}

	if !result.Result.Success {
		t.Fatalf("Expected success, got error: %s", result.Result.Error)
	}

	if result.ToolCall.ID != "test_1" {
		t.Fatalf("Expected tool call ID 'test_1', got: %s", result.ToolCall.ID)
	}

	if result.Duration <= 0 {
		t.Fatal("Expected positive duration")
	}
}

// Test ExecutionEngine parallel execution
func TestExecutionEngine_ExecuteParallel(t *testing.T) {
	registry := NewMockRegistry()

	// Add multiple tools
	for i := 1; i <= 3; i++ {
		tool := &MockTool{
			name:        fmt.Sprintf("tool_%d", i),
			description: fmt.Sprintf("Test tool %d", i),
			parameters:  map[string]interface{}{"type": "object"},
			executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
				// Simulate some work
				time.Sleep(10 * time.Millisecond)
				return &ToolResult{
					Success: true,
					Content: "Success",
				}, nil
			},
		}
		registry.AddTool(tool)
	}

	engine := NewExecutionEngine(registry, 2, 30*time.Second, 10)

	calls := []ai.ToolCall{
		{ID: "1", Name: "tool_1", Args: map[string]interface{}{}},
		{ID: "2", Name: "tool_2", Args: map[string]interface{}{}},
		{ID: "3", Name: "tool_3", Args: map[string]interface{}{}},
	}

	start := time.Now()
	results := engine.executeParallel(context.Background(), calls)
	elapsed := time.Since(start)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got: %d", len(results))
	}

	// Check that all results are successful
	for i, result := range results {
		if result.Error != nil {
			t.Fatalf("Result %d failed with error: %v", i, result.Error)
		}
		if !result.Result.Success {
			t.Fatalf("Result %d was not successful", i)
		}
	}

	// Should take less time than sequential execution (30ms) due to parallelism
	if elapsed > 25*time.Millisecond {
		t.Fatalf("Parallel execution took too long: %v", elapsed)
	}
}

// Test ExecutionEngine with middleware
func TestExecutionEngine_WithMiddleware(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	// Add logging middleware
	loggedCalls := []string{}
	loggingMw := &TestLoggingMiddleware{
		logs: &loggedCalls,
	}
	engine.AddMiddleware(loggingMw)

	call := ai.ToolCall{
		ID:   "test_1",
		Name: "test_tool",
		Args: map[string]interface{}{"param1": "value1"},
	}

	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{call})

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got: %d", len(results))
	}

	// Check middleware was called
	if len(loggedCalls) != 2 { // Before and after
		t.Fatalf("Expected 2 middleware calls, got: %d", len(loggedCalls))
	}

	if loggedCalls[0] != "before:test_tool" {
		t.Fatalf("Expected 'before:test_tool', got: %s", loggedCalls[0])
	}

	if loggedCalls[1] != "after:test_tool" {
		t.Fatalf("Expected 'after:test_tool', got: %s", loggedCalls[1])
	}
}

// Test ExecutionEngine error handling
func TestExecutionEngine_ErrorHandling(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "failing_tool",
		description: "A tool that fails",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{
				Success: false,
				Error:   "Tool intentionally failed",
			}, fmt.Errorf("execution error")
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	call := ai.ToolCall{
		ID:   "test_1",
		Name: "failing_tool",
		Args: map[string]interface{}{},
	}

	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{call})

	if err != nil {
		t.Fatalf("ExecuteToolCalls should not return error even if tool fails: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got: %d", len(results))
	}

	result := results[0]
	if result.Error == nil {
		t.Fatal("Expected error in result")
	}

	if result.Result == nil {
		t.Fatal("Expected result to be created even for failed tools")
	}

	if result.Result.Success {
		t.Fatal("Expected result to indicate failure")
	}
}

// Test ExecutionEngine timeout
func TestExecutionEngine_Timeout(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "slow_tool",
		description: "A slow tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			// Wait for context cancellation
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	registry.AddTool(tool)

	// Set a very short timeout
	engine := NewExecutionEngine(registry, 3, 1*time.Millisecond, 10)

	call := ai.ToolCall{
		ID:   "test_1",
		Name: "slow_tool",
		Args: map[string]interface{}{},
	}

	start := time.Now()
	results, err := engine.ExecuteToolCalls(context.Background(), []ai.ToolCall{call})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Expected no error from ExecuteToolCalls, got: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got: %d", len(results))
	}

	result := results[0]
	if result.Error == nil {
		t.Fatal("Expected timeout error")
	}

	// Should timeout quickly
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Timeout took too long: %v", elapsed)
	}
}

// Test formatToolResultForAI
func TestExecutionEngine_FormatToolResultForAI(t *testing.T) {
	registry := NewMockRegistry()
	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	tests := []struct {
		name     string
		result   *ExecutionResult
		expected string
	}{
		{
			name: "successful result",
			result: &ExecutionResult{
				ToolCall: &ai.ToolCall{Name: "test_tool"},
				Result: &ToolResult{
					Success: true,
					Content: "Tool executed successfully",
					Data: map[string]interface{}{
						"count": 5,
						"items": []string{"a", "b", "c"},
					},
				},
			},
			expected: "Tool executed successfully\n\nStructured data: {\"count\":5,\"items\":[\"a\",\"b\",\"c\"]}",
		},
		{
			name: "failed result",
			result: &ExecutionResult{
				ToolCall: &ai.ToolCall{Name: "test_tool"},
				Result: &ToolResult{
					Success: false,
					Error:   "Something went wrong",
				},
			},
			expected: "Tool 'test_tool' failed: Something went wrong",
		},
		{
			name: "error during execution",
			result: &ExecutionResult{
				ToolCall: &ai.ToolCall{Name: "test_tool"},
				Error:    fmt.Errorf("execution failed"),
			},
			expected: "Tool 'test_tool' failed: execution failed",
		},
		{
			name: "no result",
			result: &ExecutionResult{
				ToolCall: &ai.ToolCall{Name: "test_tool"},
				Result:   nil,
			},
			expected: "Tool 'test_tool' executed but returned no result",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			formatted := engine.formatToolResultForAI(test.result)
			if formatted != test.expected {
				t.Fatalf("Expected: %s\nGot: %s", test.expected, formatted)
			}
		})
	}
}

// Test middleware implementations
type TestLoggingMiddleware struct {
	logs *[]string
}

func (m *TestLoggingMiddleware) BeforeExecution(ctx context.Context, call *ai.ToolCall) error {
	*m.logs = append(*m.logs, "before:"+call.Name)
	return nil
}

func (m *TestLoggingMiddleware) AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error {
	*m.logs = append(*m.logs, "after:"+call.Name)
	return nil
}

// Test SecurityMiddleware
func TestSecurityMiddleware(t *testing.T) {
	allowedTools := []string{"safe_tool"}
	middleware := NewSecurityMiddleware(allowedTools)

	// Test allowed tool
	safeCall := &ai.ToolCall{Name: "safe_tool"}
	err := middleware.BeforeExecution(context.Background(), safeCall)
	if err != nil {
		t.Fatalf("Expected no error for allowed tool, got: %v", err)
	}

	// Test disallowed tool
	unsafeCall := &ai.ToolCall{Name: "dangerous_tool"}
	err = middleware.BeforeExecution(context.Background(), unsafeCall)
	if err == nil {
		t.Fatal("Expected error for disallowed tool")
	}

	expectedError := "tool 'dangerous_tool' not allowed by security policy"
	if err.Error() != expectedError {
		t.Fatalf("Expected error: %s, got: %s", expectedError, err.Error())
	}
}

// Test MetricsMiddleware
func TestMetricsMiddleware(t *testing.T) {
	middleware := NewMetricsMiddleware()

	call := &ai.ToolCall{Name: "test_tool"}
	result := &ExecutionResult{
		Duration: 100 * time.Millisecond,
	}

	// Record some executions
	middleware.AfterExecution(context.Background(), call, result)
	middleware.AfterExecution(context.Background(), call, result)

	metrics := middleware.GetMetrics()

	toolMetrics, exists := metrics["test_tool"].(map[string]interface{})
	if !exists {
		t.Fatal("Expected metrics for test_tool")
	}

	count, ok := toolMetrics["count"].(int)
	if !ok || count != 2 {
		t.Fatalf("Expected count 2, got: %v", count)
	}

	avgDuration, ok := toolMetrics["average_duration"].(string)
	if !ok {
		t.Fatal("Expected average_duration")
	}

	if avgDuration != "100ms" {
		t.Fatalf("Expected average duration '100ms', got: %s", avgDuration)
	}
}

// Test that HandleToolCallFlow propagates the Model field to follow-up requests
func TestHandleToolCallFlow_ModelPropagation(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{
				Success: true,
				Content: "tool result",
			}, nil
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	// Mock provider that captures requests
	provider := ai.NewMockProvider("test")
	// Second call (after tool execution) returns a final text response
	provider.AddResponse("final answer", nil)

	initialReq := &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: "hello"}},
		Model:     "claude-sonnet-4-6",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}

	initialResp := &ai.GenerateResponse{
		Content: "",
		ToolCalls: []ai.ToolCall{
			{ID: "call_1", Name: "test_tool", Args: map[string]interface{}{}},
		},
	}

	resp, err := engine.HandleToolCallFlow(context.Background(), provider, initialReq, initialResp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// The provider should have been called once (the follow-up after tool execution)
	calls := provider.GetCalls()
	if len(calls) != 1 {
		t.Fatalf("Expected 1 provider call, got %d", len(calls))
	}

	// Verify the model was propagated to the follow-up request
	followUpReq := calls[0].Request
	if followUpReq.Model != "claude-sonnet-4-6" {
		t.Fatalf("Expected model 'claude-sonnet-4-6' in follow-up request, got '%s'", followUpReq.Model)
	}
}
