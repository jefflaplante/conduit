package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// Compile-time interface compliance checks
var _ StreamingProvider = (*OpenAIProvider)(nil)
var _ StreamingProvider = (*AnthropicProvider)(nil)

func TestNewOpenAIProvider_DefaultBaseURL(t *testing.T) {
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:   "openai",
		APIKey: "sk-test",
		Model:  "gpt-4",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.baseURL != defaultOpenAIURL {
		t.Errorf("expected base URL %q, got %q", defaultOpenAIURL, p.baseURL)
	}
}

func TestNewOpenAIProvider_CustomBaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"http://localhost:11434/v1", "http://localhost:11434/v1/chat/completions"},
		{"http://localhost:11434/v1/", "http://localhost:11434/v1/chat/completions"},
		{"http://localhost:8000/v1/chat/completions", "http://localhost:8000/v1/chat/completions"},
		{"http://myserver:5000", "http://myserver:5000/chat/completions"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			p, err := NewOpenAIProvider(config.ProviderConfig{
				Name:    "local",
				BaseURL: tt.input,
				Model:   "llama3",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.baseURL != tt.expected {
				t.Errorf("expected base URL %q, got %q", tt.expected, p.baseURL)
			}
		})
	}
}

func TestNewOpenAIProvider_NoAPIKeyWithBaseURL(t *testing.T) {
	// Local servers don't require an API key
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "local",
		BaseURL: "http://localhost:11434/v1",
		Model:   "llama3",
	})
	if err != nil {
		t.Fatalf("expected no error for local server without API key, got: %v", err)
	}
	if p.apiKey != "" {
		t.Errorf("expected empty API key, got %q", p.apiKey)
	}
}

func TestNewOpenAIProvider_NoAPIKeyNoBaseURL(t *testing.T) {
	// OpenAI proper requires an API key
	_, err := NewOpenAIProvider(config.ProviderConfig{
		Name:  "openai",
		Model: "gpt-4",
	})
	if err == nil {
		t.Fatal("expected error for missing API key without base_url")
	}
	if !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNewOpenAIProvider_OllamaViaRouter(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "ollama",
		Providers: []config.ProviderConfig{
			{
				Name:  "ollama",
				Type:  "ollama",
				Model: "llama3.2",
			},
		},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router with ollama provider: %v", err)
	}

	provider, exists := router.providers["ollama"]
	if !exists {
		t.Fatal("ollama provider not registered")
	}

	openaiProvider, ok := provider.(*OpenAIProvider)
	if !ok {
		t.Fatalf("ollama provider should be *OpenAIProvider, got %T", provider)
	}

	expectedURL := "http://localhost:11434/v1/chat/completions"
	if openaiProvider.baseURL != expectedURL {
		t.Errorf("expected base URL %q, got %q", expectedURL, openaiProvider.baseURL)
	}
	if openaiProvider.model != "llama3.2" {
		t.Errorf("expected model 'llama3.2', got %q", openaiProvider.model)
	}
}

func TestOpenAIProvider_ConditionalAuthHeader(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{
						"content": "hello",
					},
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})
	}))
	defer server.Close()

	// Provider with no API key (local server)
	p, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "local",
		BaseURL: server.URL,
		Model:   "llama3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx := context.Background()
	_, err = p.GenerateResponse(ctx, &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if receivedAuth != "" {
		t.Errorf("expected no Authorization header for local server, got %q", receivedAuth)
	}

	// Provider with API key
	p2, err := NewOpenAIProvider(config.ProviderConfig{
		Name:    "openai",
		APIKey:  "sk-test-key",
		BaseURL: server.URL,
		Model:   "gpt-4",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = p2.GenerateResponse(ctx, &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}

	if receivedAuth != "Bearer sk-test-key" {
		t.Errorf("expected 'Bearer sk-test-key', got %q", receivedAuth)
	}
}

func TestOpenAIProvider_ModelOverride(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		json.Unmarshal(body, &req)
		receivedModel, _ = req["model"].(string)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{
				map[string]interface{}{
					"message": map[string]interface{}{"content": "ok"},
				},
			},
			"usage": map[string]interface{}{},
		})
	}))
	defer server.Close()

	p, _ := NewOpenAIProvider(config.ProviderConfig{
		Name:    "test",
		BaseURL: server.URL,
		Model:   "default-model",
	})

	ctx := context.Background()

	// Without override — uses default model
	p.GenerateResponse(ctx, &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 100,
	})
	if receivedModel != "default-model" {
		t.Errorf("expected 'default-model', got %q", receivedModel)
	}

	// With override — uses override model
	p.GenerateResponse(ctx, &GenerateRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		Model:     "override-model",
		MaxTokens: 100,
	})
	if receivedModel != "override-model" {
		t.Errorf("expected 'override-model', got %q", receivedModel)
	}
}

func TestParseOpenAISSEStream_TextDeltas(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"role":"assistant"},"index":0}]}`,
		`data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}`,
		`data: {"choices":[{"delta":{"content":" world"},"index":0}]}`,
		`data: {"choices":[{"delta":{"content":"!"},"index":0}]}`,
		`data: [DONE]`,
	}

	body := strings.NewReader(strings.Join(chunks, "\n"))

	var deltas []string
	var gotDone bool

	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(body, func(delta string, done bool) {
		if done {
			gotDone = true
		} else {
			deltas = append(deltas, delta)
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Content != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", resp.Content)
	}
	if len(deltas) != 3 {
		t.Errorf("expected 3 deltas, got %d: %v", len(deltas), deltas)
	}
	if !gotDone {
		t.Error("expected done=true callback")
	}
}

func TestParseOpenAISSEStream_ToolCalls(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_123","type":"function","function":{"name":"get_weather","arguments":""}}]},"index":0}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":"}}]},"index":0}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"NYC\"}"}}]},"index":0}]}`,
		`data: [DONE]`,
	}

	body := strings.NewReader(strings.Join(chunks, "\n"))

	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}

	tc := resp.ToolCalls[0]
	if tc.ID != "call_123" {
		t.Errorf("expected tool call ID 'call_123', got %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("expected tool name 'get_weather', got %q", tc.Name)
	}
	if tc.Args["location"] != "NYC" {
		t.Errorf("expected location 'NYC', got %v", tc.Args["location"])
	}
}

func TestParseOpenAISSEStream_Done(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"hi"},"index":0}]}`,
		`data: [DONE]`,
	}
	body := strings.NewReader(strings.Join(chunks, "\n"))

	doneCount := 0
	p := &OpenAIProvider{}
	_, err := p.parseOpenAISSEStream(body, func(delta string, done bool) {
		if done {
			doneCount++
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doneCount != 1 {
		t.Errorf("expected done callback called once, got %d", doneCount)
	}
}

func TestParseOpenAISSEStream_Usage(t *testing.T) {
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"hi"},"index":0}]}`,
		fmt.Sprintf(`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`),
		`data: [DONE]`,
	}
	body := strings.NewReader(strings.Join(chunks, "\n"))

	p := &OpenAIProvider{}
	resp, err := p.parseOpenAISSEStream(body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens=10, got %d", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens=5, got %d", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens=15, got %d", resp.Usage.TotalTokens)
	}
}

func TestRouterStreamingFallback(t *testing.T) {
	// A mock provider that does NOT implement StreamingProvider
	mock := &mockNonStreamingProvider{
		name: "mock",
		response: &GenerateResponse{
			Content: "non-streaming response",
			Usage:   Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
		},
	}

	cfg := config.AIConfig{
		DefaultProvider: "mock",
	}
	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("failed to create router: %v", err)
	}
	router.RegisterProvider("mock", mock)

	session := &sessions.Session{Key: "test-session"}

	resp, err := router.GenerateResponseStreaming(context.Background(), session, "hello", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.GetContent() != "non-streaming response" {
		t.Errorf("expected 'non-streaming response', got %q", resp.GetContent())
	}
}

// mockNonStreamingProvider implements Provider but NOT StreamingProvider
type mockNonStreamingProvider struct {
	name     string
	response *GenerateResponse
}

func (m *mockNonStreamingProvider) Name() string { return m.name }
func (m *mockNonStreamingProvider) GenerateResponse(_ context.Context, _ *GenerateRequest) (*GenerateResponse, error) {
	return m.response, nil
}

func TestStripProviderPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ghost/Qwen3.5-9B-Q6_K", "Qwen3.5-9B-Q6_K"},
		{"anthropic/claude-opus-4-6", "claude-opus-4-6"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"gpt-4o", "gpt-4o"},
		{"", ""},
		{"a/b/c", "b/c"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := stripProviderPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("stripProviderPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestConvertMessagesToOpenAI(t *testing.T) {
	p := &OpenAIProvider{}

	messages := []ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "What's the weather?"},
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []ToolCall{
				{ID: "call_123", Name: "get_weather", Args: map[string]interface{}{"location": "NYC"}},
			},
		},
		{Role: "tool", Content: `{"temp": 72}`, ToolCallID: "call_123"},
		{Role: "assistant", Content: "It's 72 degrees in NYC."},
	}

	converted := p.convertMessagesToOpenAI(messages)

	if len(converted) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(converted))
	}

	// Check system message
	if converted[0]["role"] != "system" || converted[0]["content"] != "You are helpful." {
		t.Errorf("system message not converted correctly: %v", converted[0])
	}

	// Check assistant message with tool calls
	assistantMsg := converted[2]
	if assistantMsg["role"] != "assistant" {
		t.Errorf("expected assistant role, got %v", assistantMsg["role"])
	}
	toolCalls, ok := assistantMsg["tool_calls"].([]map[string]interface{})
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %v", assistantMsg["tool_calls"])
	}
	if toolCalls[0]["type"] != "function" {
		t.Errorf("expected type 'function', got %v", toolCalls[0]["type"])
	}
	fn, ok := toolCalls[0]["function"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected function object, got %v", toolCalls[0]["function"])
	}
	if fn["name"] != "get_weather" {
		t.Errorf("expected name 'get_weather', got %v", fn["name"])
	}
	// arguments should be JSON string
	if _, ok := fn["arguments"].(string); !ok {
		t.Errorf("expected arguments to be string, got %T", fn["arguments"])
	}

	// Check tool result message
	toolMsg := converted[3]
	if toolMsg["role"] != "tool" {
		t.Errorf("expected tool role, got %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_123" {
		t.Errorf("expected tool_call_id 'call_123', got %v", toolMsg["tool_call_id"])
	}
}
