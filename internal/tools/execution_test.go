package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/types"
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

// TestToolCallFlow_SliceIsolation verifies that HandleToolCallFlow does not
// mutate the caller's Messages slice when it has spare capacity.
func TestToolCallFlow_SliceIsolation(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "ok"}, nil
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 10)

	provider := ai.NewMockProvider("test")
	provider.AddResponse("done", nil) // final response after tool exec

	// Allocate a backing array with extra capacity so an unguarded append
	// would overwrite elements beyond len.
	backing := make([]ai.ChatMessage, 1, 10)
	backing[0] = ai.ChatMessage{Role: "user", Content: "hello"}

	// Place a sentinel in the spare capacity slot
	sentinel := ai.ChatMessage{Role: "sentinel", Content: "DO_NOT_OVERWRITE"}
	backing = append(backing, sentinel)
	// Now trim back to len=1 but capacity=10; backing[1] == sentinel
	callerSlice := backing[:1]

	initialReq := &ai.GenerateRequest{
		Messages:  callerSlice,
		Model:     "test-model",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}

	initialResp := &ai.GenerateResponse{
		Content:   "",
		ToolCalls: []ai.ToolCall{{ID: "c1", Name: "test_tool", Args: map[string]interface{}{}}},
	}

	_, err := engine.HandleToolCallFlow(context.Background(), provider, initialReq, initialResp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	// The sentinel in backing[1] must still be intact.
	if backing[1].Role != "sentinel" || backing[1].Content != "DO_NOT_OVERWRITE" {
		t.Errorf("Caller's backing array was mutated: backing[1] = %+v", backing[1])
	}

	// The original request's Messages slice must be unchanged.
	if len(initialReq.Messages) != 1 {
		t.Errorf("Original request messages were mutated: len=%d, want 1", len(initialReq.Messages))
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

// Test that formatToolResultForAI surfaces ErrorDetails
func TestFormatToolResultForAI_ErrorDetails(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)

	result := &ExecutionResult{
		ToolCall: &ai.ToolCall{ID: "1", Name: "mqtt"},
		Result: &ToolResult{
			Success: false,
			Error:   "publish failed: connection refused",
			ErrorDetails: &types.ToolErrorDetails{
				Type:            "connection_error",
				Suggestions:     []string{"Check broker is running", "Verify MQTT config"},
				AvailableValues: []string{"status", "topics", "recent"},
				Examples:        []string{`action=status`, `action=topics`},
			},
		},
	}

	formatted := engine.formatToolResultForAI(result)

	if !strings.Contains(formatted, "Error type: connection_error") {
		t.Errorf("Expected error type in output, got: %s", formatted)
	}
	if !strings.Contains(formatted, "Check broker is running") {
		t.Errorf("Expected suggestion in output, got: %s", formatted)
	}
	if !strings.Contains(formatted, "status, topics, recent") {
		t.Errorf("Expected available values in output, got: %s", formatted)
	}
	if !strings.Contains(formatted, "action=status") {
		t.Errorf("Expected examples in output, got: %s", formatted)
	}
}

// Test that formatToolResultForAI truncates oversized results
func TestFormatToolResultForAI_Truncation(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)
	engine.SetMaxResultChars(100)

	// Create a result with content larger than the limit
	bigContent := ""
	for i := 0; i < 200; i++ {
		bigContent += "X"
	}

	result := &ExecutionResult{
		ToolCall: &ai.ToolCall{ID: "1", Name: "test"},
		Result: &ToolResult{
			Success: true,
			Content: bigContent,
		},
	}

	formatted := engine.formatToolResultForAI(result)

	if !strings.Contains(formatted, "truncated") {
		t.Errorf("Expected truncation indicator in output, got length %d: %s", len(formatted), formatted)
	}
	if !strings.Contains(formatted, "200 chars") {
		t.Errorf("Expected original size in truncation message, got: %s", formatted)
	}
}

// Test that formatToolResultForAI does not truncate small results
func TestFormatToolResultForAI_NoTruncation(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)

	result := &ExecutionResult{
		ToolCall: &ai.ToolCall{ID: "1", Name: "test"},
		Result: &ToolResult{
			Success: true,
			Content: "small result",
		},
	}

	formatted := engine.formatToolResultForAI(result)

	if formatted != "small result" {
		t.Errorf("Expected unmodified content, got: %s", formatted)
	}
}

// TestHandleToolCallFlow_RefocusInterval verifies that goal reminders are injected
// at the configured interval to prevent agent drift on long tool chains.
func TestHandleToolCallFlow_RefocusInterval(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "ok"}, nil
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 25)
	// Set refocus interval to 2 for easier testing (injects at depth 2, 4, 6, etc.)
	engine.SetRefocusInterval(2)

	provider := ai.NewMockProvider("test")
	// Configure responses: tool calls at depth 0, 1, 2, then final response at depth 3
	// Depth 0: initial call makes tool call
	// Depth 1: provider responds with another tool call
	// Depth 2: provider responds with another tool call (refocus should inject here)
	// Depth 3: provider responds with final content
	provider.AddResponse("", []ai.ToolCall{{ID: "c1", Name: "test_tool", Args: map[string]interface{}{}}})
	provider.AddResponse("", []ai.ToolCall{{ID: "c2", Name: "test_tool", Args: map[string]interface{}{}}})
	provider.AddResponse("", []ai.ToolCall{{ID: "c3", Name: "test_tool", Args: map[string]interface{}{}}})
	provider.AddResponse("final answer", nil)

	initialReq := &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: "Please analyze this complex data"}},
		Model:     "test-model",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}

	initialResp := &ai.GenerateResponse{
		Content:   "",
		ToolCalls: []ai.ToolCall{{ID: "c0", Name: "test_tool", Args: map[string]interface{}{}}},
	}

	resp, err := engine.HandleToolCallFlow(context.Background(), provider, initialReq, initialResp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	if resp == nil {
		t.Fatal("Expected non-nil response")
	}

	// Check that refocus message was injected at depth 2
	calls := provider.GetCalls()
	if len(calls) != 4 {
		t.Fatalf("Expected 4 provider calls (depths 0,1,2,3), got %d", len(calls))
	}

	// At depth 2 (3rd call, index 2), we should see a system message with the refocus reminder
	depth2Request := calls[2].Request
	foundRefocus := false
	for _, msg := range depth2Request.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Reminder: Your original goal was:") {
			foundRefocus = true
			if !strings.Contains(msg.Content, "analyze this complex data") {
				t.Errorf("Refocus message should contain original goal, got: %s", msg.Content)
			}
			break
		}
	}
	if !foundRefocus {
		t.Error("Expected refocus message to be injected at depth 2")
	}

	// At depth 1 (2nd call, index 1), there should be NO refocus message
	depth1Request := calls[1].Request
	for _, msg := range depth1Request.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Reminder: Your original goal was:") {
			t.Error("Refocus message should NOT be injected at depth 1")
		}
	}
}

// TestHandleToolCallFlow_RefocusDisabled verifies that refocusing can be disabled.
func TestHandleToolCallFlow_RefocusDisabled(t *testing.T) {
	registry := NewMockRegistry()
	tool := &MockTool{
		name:        "test_tool",
		description: "A test tool",
		parameters:  map[string]interface{}{"type": "object"},
		executeFunc: func(ctx context.Context, args map[string]interface{}) (*ToolResult, error) {
			return &ToolResult{Success: true, Content: "ok"}, nil
		},
	}
	registry.AddTool(tool)

	engine := NewExecutionEngine(registry, 3, 30*time.Second, 25)
	// Disable refocusing
	engine.SetRefocusInterval(0)

	provider := ai.NewMockProvider("test")
	// Go through multiple depths
	provider.AddResponse("", []ai.ToolCall{{ID: "c1", Name: "test_tool", Args: map[string]interface{}{}}})
	provider.AddResponse("", []ai.ToolCall{{ID: "c2", Name: "test_tool", Args: map[string]interface{}{}}})
	provider.AddResponse("final answer", nil)

	initialReq := &ai.GenerateRequest{
		Messages:  []ai.ChatMessage{{Role: "user", Content: "Do something"}},
		Model:     "test-model",
		Tools:     []ai.Tool{{Name: "test_tool"}},
		MaxTokens: 1024,
	}

	initialResp := &ai.GenerateResponse{
		Content:   "",
		ToolCalls: []ai.ToolCall{{ID: "c0", Name: "test_tool", Args: map[string]interface{}{}}},
	}

	_, err := engine.HandleToolCallFlow(context.Background(), provider, initialReq, initialResp)
	if err != nil {
		t.Fatalf("HandleToolCallFlow failed: %v", err)
	}

	// Check that NO refocus messages were injected
	calls := provider.GetCalls()
	for i, call := range calls {
		for _, msg := range call.Request.Messages {
			if msg.Role == "system" && strings.Contains(msg.Content, "Reminder: Your original goal was:") {
				t.Errorf("Refocus message should NOT be injected when disabled (found at call %d)", i)
			}
		}
	}
}

// TestExtractOriginalGoal verifies the goal extraction helper function.
func TestExtractOriginalGoal(t *testing.T) {
	engine := NewExecutionEngine(NewMockRegistry(), 3, 30*time.Second, 10)

	tests := []struct {
		name     string
		messages []ai.ChatMessage
		expected string
	}{
		{
			name: "simple user message",
			messages: []ai.ChatMessage{
				{Role: "user", Content: "Please help me with this task"},
			},
			expected: "Please help me with this task",
		},
		{
			name: "multiple messages - returns last user message",
			messages: []ai.ChatMessage{
				{Role: "system", Content: "You are a helpful assistant"},
				{Role: "user", Content: "First question"},
				{Role: "assistant", Content: "First answer"},
				{Role: "user", Content: "Follow up question"},
			},
			expected: "Follow up question",
		},
		{
			name: "no user messages",
			messages: []ai.ChatMessage{
				{Role: "system", Content: "You are a helpful assistant"},
				{Role: "assistant", Content: "Hello"},
			},
			expected: "",
		},
		{
			name:     "empty messages",
			messages: []ai.ChatMessage{},
			expected: "",
		},
		{
			name: "truncates long goals",
			messages: []ai.ChatMessage{
				{Role: "user", Content: strings.Repeat("X", 250)},
			},
			expected: strings.Repeat("X", 200) + "...",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := engine.extractOriginalGoal(test.messages)
			if result != test.expected {
				t.Errorf("Expected: %q, got: %q", test.expected, result)
			}
		})
	}
}
