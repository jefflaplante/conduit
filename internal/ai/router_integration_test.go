package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// MockExecutionEngine implements ExecutionEngine for testing
type MockExecutionEngine struct {
	shouldReturnError bool
	responseContent   string
	steps             int
}

func (m *MockExecutionEngine) HandleToolCallFlow(ctx context.Context, provider Provider, initialReq *GenerateRequest, initialResp *GenerateResponse) (ConversationResponse, error) {
	if m.shouldReturnError {
		return nil, fmt.Errorf("mock execution error")
	}

	return &MockConversationResponse{
		Content: m.responseContent,
		Usage:   &initialResp.Usage,
		Steps:   m.steps,
	}, nil
}

// MockConversationResponse implements ConversationResponse
type MockConversationResponse struct {
	Content string `json:"content"`
	Usage   *Usage `json:"usage"`
	Steps   int    `json:"steps"`
}

func (m *MockConversationResponse) GetContent() string {
	return m.Content
}

func (m *MockConversationResponse) GetUsage() *Usage {
	return m.Usage
}

func (m *MockConversationResponse) GetSteps() int {
	return m.Steps
}

func (m *MockConversationResponse) HasToolResults() bool {
	return true
}

// MockAgentSystem implements AgentSystem for testing
type MockAgentSystem struct {
	tools []Tool
}

func (m *MockAgentSystem) BuildSystemPrompt(ctx context.Context, session *sessions.Session) ([]SystemBlock, error) {
	return []SystemBlock{
		{
			Type: "text",
			Text: "You are a helpful AI assistant with access to tools.",
		},
	}, nil
}

func (m *MockAgentSystem) GetToolDefinitions() []Tool {
	return m.tools
}

func (m *MockAgentSystem) ProcessResponse(ctx context.Context, response *GenerateResponse) (*AgentProcessedResponse, error) {
	return &AgentProcessedResponse{
		Content:   response.Content,
		ToolCalls: response.ToolCalls,
		Silent:    false,
		Modified:  false,
	}, nil
}

// Test router with tool execution integration
func TestRouter_GenerateResponseWithTools(t *testing.T) {
	// Create mock Anthropic server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Parse request to check for tools
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		response := map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "I'll help you with that task.",
				},
				map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_123",
					"name": "test_tool",
					"input": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
			"usage": map[string]interface{}{
				"input_tokens":  100,
				"output_tokens": 50,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Create router with mock execution engine
	cfg := config.AIConfig{
		DefaultProvider: "anthropic",
		Providers: []config.ProviderConfig{
			{
				Name:   "anthropic",
				Type:   "anthropic",
				APIKey: "test-key",
				Model:  "claude-3-sonnet",
			},
		},
	}

	agentSystem := &MockAgentSystem{
		tools: []Tool{
			{
				Name:        "test_tool",
				Description: "A test tool for integration testing",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"param1": map[string]interface{}{
							"type":        "string",
							"description": "Test parameter",
						},
					},
					"required": []string{"param1"},
				},
			},
		},
	}

	mockExecEngine := &MockExecutionEngine{
		responseContent: "Tool executed successfully! The result was: value1",
		steps:           2,
	}

	router, err := NewRouterWithExecution(cfg, agentSystem, mockExecEngine)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Patch the Anthropic provider to use our test server
	if anthropicProvider, ok := router.providers["anthropic"].(*AnthropicProvider); ok {
		// We need to mock the HTTP client to use our test server
		anthropicProvider.client = &http.Client{
			Transport: &mockTransport{server: server},
		}
	}

	// Create test session
	session := &sessions.Session{
		Key:     "test-session",
		Context: map[string]string{"auth_type": "api_key"},
	}

	// Test tool execution flow
	response, err := router.GenerateResponseWithTools(context.Background(), session, "Use the test tool with param1='hello'", "anthropic", "")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if response == nil {
		t.Fatal("Expected response, got nil")
	}

	content := response.GetContent()
	if !strings.Contains(content, "Tool executed successfully") {
		t.Fatalf("Expected tool execution result in response, got: %s", content)
	}

	if response.GetSteps() != 2 {
		t.Fatalf("Expected 2 steps (initial + tool execution), got: %d", response.GetSteps())
	}

	if !response.HasToolResults() {
		t.Fatal("Expected response to have tool results")
	}
}

// Test router without execution engine (fallback behavior)
func TestRouter_GenerateResponseWithTools_NoExecutionEngine(t *testing.T) {
	// Create mock server that returns tool calls
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_123",
					"name": "test_tool",
					"input": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
			"usage": map[string]interface{}{
				"input_tokens":  100,
				"output_tokens": 50,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := config.AIConfig{
		DefaultProvider: "anthropic",
		Providers: []config.ProviderConfig{
			{
				Name:   "anthropic",
				Type:   "anthropic",
				APIKey: "test-key",
				Model:  "claude-3-sonnet",
			},
		},
	}

	agentSystem := &MockAgentSystem{
		tools: []Tool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}

	// Create router WITHOUT execution engine
	router, err := NewRouter(cfg, agentSystem)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Patch the provider to use test server
	if anthropicProvider, ok := router.providers["anthropic"].(*AnthropicProvider); ok {
		anthropicProvider.client = &http.Client{
			Transport: &mockTransport{server: server},
		}
	}

	session := &sessions.Session{
		Key:     "test-session",
		Context: map[string]string{"auth_type": "api_key"},
	}

	// Should return simple response without executing tools
	response, err := router.GenerateResponseWithTools(context.Background(), session, "Use the test tool", "anthropic", "")
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Should be a SimpleConversationResponse
	if response.HasToolResults() {
		t.Fatal("Expected no tool results when execution engine is not available")
	}

	if response.GetSteps() != 1 {
		t.Fatalf("Expected 1 step without tool execution, got: %d", response.GetSteps())
	}
}

// Test error handling in tool execution flow
func TestRouter_GenerateResponseWithTools_ExecutionError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{
					"type": "tool_use",
					"id":   "toolu_123",
					"name": "test_tool",
					"input": map[string]interface{}{
						"param1": "value1",
					},
				},
			},
			"usage": map[string]interface{}{
				"input_tokens":  100,
				"output_tokens": 50,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	cfg := config.AIConfig{
		DefaultProvider: "anthropic",
		Providers: []config.ProviderConfig{
			{
				Name:   "anthropic",
				Type:   "anthropic",
				APIKey: "test-key",
				Model:  "claude-3-sonnet",
			},
		},
	}

	agentSystem := &MockAgentSystem{
		tools: []Tool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}

	// Mock execution engine that returns error
	mockExecEngine := &MockExecutionEngine{
		shouldReturnError: true,
	}

	router, err := NewRouterWithExecution(cfg, agentSystem, mockExecEngine)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	if anthropicProvider, ok := router.providers["anthropic"].(*AnthropicProvider); ok {
		anthropicProvider.client = &http.Client{
			Transport: &mockTransport{server: server},
		}
	}

	session := &sessions.Session{
		Key:     "test-session",
		Context: map[string]string{"auth_type": "api_key"},
	}

	// Should return error from execution engine
	_, err = router.GenerateResponseWithTools(context.Background(), session, "Use the test tool", "anthropic", "")
	if err == nil {
		t.Fatal("Expected error from execution engine")
	}

	if !strings.Contains(err.Error(), "mock execution error") {
		t.Fatalf("Expected mock execution error, got: %v", err)
	}
}

// Test OpenAI provider tool parsing
func TestOpenAIProvider_ToolParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{
						"content": "I'll help you with that.",
						"tool_calls": []interface{}{
							map[string]interface{}{
								"id": "call_123",
								"function": map[string]interface{}{
									"name":      "test_tool",
									"arguments": `{"param1": "value1"}`,
								},
							},
						},
					},
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := &OpenAIProvider{
		name:   "openai",
		apiKey: "test-key",
		model:  "gpt-4",
		client: &http.Client{
			Transport: &mockTransport{server: server},
		},
	}

	req := &GenerateRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Use the test tool"},
		},
		Tools: []Tool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
		MaxTokens: 100,
	}

	resp, err := provider.GenerateResponse(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Content != "I'll help you with that." {
		t.Fatalf("Expected content 'I'll help you with that.', got: %s", resp.Content)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got: %d", len(resp.ToolCalls))
	}

	toolCall := resp.ToolCalls[0]
	if toolCall.ID != "call_123" {
		t.Fatalf("Expected tool call ID 'call_123', got: %s", toolCall.ID)
	}

	if toolCall.Name != "test_tool" {
		t.Fatalf("Expected tool name 'test_tool', got: %s", toolCall.Name)
	}

	if toolCall.Args["param1"] != "value1" {
		t.Fatalf("Expected param1 'value1', got: %v", toolCall.Args["param1"])
	}

	// Check usage parsing
	if resp.Usage.PromptTokens != 100 {
		t.Fatalf("Expected 100 prompt tokens, got: %d", resp.Usage.PromptTokens)
	}

	if resp.Usage.CompletionTokens != 50 {
		t.Fatalf("Expected 50 completion tokens, got: %d", resp.Usage.CompletionTokens)
	}

	if resp.Usage.TotalTokens != 150 {
		t.Fatalf("Expected 150 total tokens, got: %d", resp.Usage.TotalTokens)
	}
}

// mockTransport redirects all requests to the test server
type mockTransport struct {
	server *httptest.Server
}

func (t *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.server.URL, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

// Test Anthropic provider tool conversion
func TestAnthropicProvider_ToolConversion(t *testing.T) {
	provider := &AnthropicProvider{}

	tools := []Tool{
		{
			Name:        "test_tool",
			Description: "A test tool for validation",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"param1": map[string]interface{}{
						"type":        "string",
						"description": "First parameter",
					},
					"param2": map[string]interface{}{
						"type":    "integer",
						"minimum": 1,
					},
				},
				"required": []string{"param1"},
			},
		},
	}

	anthropicTools := provider.convertToolsToAnthropic(tools)

	if len(anthropicTools) != 1 {
		t.Fatalf("Expected 1 tool, got: %d", len(anthropicTools))
	}

	toolMap, ok := anthropicTools[0].(map[string]interface{})
	if !ok {
		t.Fatal("Expected tool to be a map")
	}

	if toolMap["name"] != "test_tool" {
		t.Fatalf("Expected name 'test_tool', got: %v", toolMap["name"])
	}

	if toolMap["description"] != "A test tool for validation" {
		t.Fatalf("Expected description, got: %v", toolMap["description"])
	}

	schema, ok := toolMap["input_schema"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected input_schema to be a map")
	}

	if schema["type"] != "object" {
		t.Fatalf("Expected type 'object', got: %v", schema["type"])
	}
}

// Test OpenAI provider tool conversion
func TestOpenAIProvider_ToolConversion(t *testing.T) {
	provider := &OpenAIProvider{}

	tools := []Tool{
		{
			Name:        "test_tool",
			Description: "A test tool for validation",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"param1": map[string]interface{}{
						"type": "string",
					},
				},
			},
		},
	}

	openaiTools := provider.convertToolsToOpenAI(tools)

	if len(openaiTools) != 1 {
		t.Fatalf("Expected 1 tool, got: %d", len(openaiTools))
	}

	toolMap, ok := openaiTools[0].(map[string]interface{})
	if !ok {
		t.Fatal("Expected tool to be a map")
	}

	if toolMap["type"] != "function" {
		t.Fatalf("Expected type 'function', got: %v", toolMap["type"])
	}

	function, ok := toolMap["function"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected function to be a map")
	}

	if function["name"] != "test_tool" {
		t.Fatalf("Expected name 'test_tool', got: %v", function["name"])
	}

	if function["description"] != "A test tool for validation" {
		t.Fatalf("Expected description, got: %v", function["description"])
	}
}
