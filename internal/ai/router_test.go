package ai

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

func TestNewRouter(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "test-provider",
		Providers: []config.ProviderConfig{
			{
				Name:   "test-anthropic",
				Type:   "anthropic",
				APIKey: "test-key",
				Model:  "test-model",
			},
			{
				Name:   "test-openai",
				Type:   "openai",
				APIKey: "test-key",
				Model:  "gpt-4",
			},
		},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	if router.default_ != "test-provider" {
		t.Errorf("Expected default provider 'test-provider', got %s", router.default_)
	}

	if len(router.providers) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(router.providers))
	}

	// Test provider registration
	if _, exists := router.providers["test-anthropic"]; !exists {
		t.Error("Expected anthropic provider to be registered")
	}

	if _, exists := router.providers["test-openai"]; !exists {
		t.Error("Expected openai provider to be registered")
	}
}

func TestNewRouterUnsupportedProvider(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "test",
		Providers: []config.ProviderConfig{
			{
				Name:   "test",
				Type:   "unsupported-provider",
				APIKey: "test-key",
				Model:  "test-model",
			},
		},
	}

	_, err := NewRouter(cfg, nil)
	if err == nil {
		t.Error("Expected error for unsupported provider type")
	}

	if !strings.Contains(err.Error(), "unsupported provider type") {
		t.Errorf("Expected 'unsupported provider type' error, got: %v", err)
	}
}

func TestNewAnthropicProvider(t *testing.T) {
	tests := []struct {
		name        string
		config      config.ProviderConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "API Key Authentication",
			config: config.ProviderConfig{
				Name:   "anthropic-api",
				Type:   "anthropic",
				APIKey: "sk-ant-api-key",
				Model:  "claude-3-5-sonnet-20250114",
			},
			expectError: false,
		},
		{
			name: "OAuth Authentication",
			config: config.ProviderConfig{
				Name:  "anthropic-oauth",
				Type:  "anthropic",
				Model: "claude-3-5-sonnet-20250114",
				Auth: &config.AuthConfig{
					Type:       "oauth",
					OAuthToken: "at_oauth_token_123",
				},
			},
			expectError: false,
		},
		{
			name: "No Authentication",
			config: config.ProviderConfig{
				Name:  "anthropic-none",
				Type:  "anthropic",
				Model: "claude-3-5-sonnet-20250114",
			},
			expectError: true,
			errorMsg:    "either OAuth token or API key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewAnthropicProvider(tt.config)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
					return
				}
				if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing '%s', got: %v", tt.errorMsg, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if provider.Name() != tt.config.Name {
				t.Errorf("Expected provider name '%s', got '%s'", tt.config.Name, provider.Name())
			}

			if provider.model != tt.config.Model {
				t.Errorf("Expected model '%s', got '%s'", tt.config.Model, provider.model)
			}

			// Test authentication configuration
			if tt.config.Auth != nil && tt.config.Auth.Type == "oauth" {
				if provider.authCfg == nil {
					t.Error("Expected OAuth config to be set")
				}
				if provider.authCfg.Type != "oauth" {
					t.Errorf("Expected OAuth auth type, got '%s'", provider.authCfg.Type)
				}
				if provider.apiKey != tt.config.Auth.OAuthToken {
					t.Errorf("Expected OAuth token as API key, got '%s'", provider.apiKey)
				}
			} else {
				if provider.apiKey != tt.config.APIKey {
					t.Errorf("Expected API key '%s', got '%s'", tt.config.APIKey, provider.apiKey)
				}
			}
		})
	}
}

func TestAnthropicProviderAuthHeaders(t *testing.T) {
	tests := []struct {
		name           string
		config         config.ProviderConfig
		expectedHeader string
		expectedValue  string
	}{
		{
			name: "OAuth Bearer Token",
			config: config.ProviderConfig{
				Name:  "oauth-test",
				Type:  "anthropic",
				Model: "claude-3-5-sonnet-20250114",
				Auth: &config.AuthConfig{
					Type:       "oauth",
					OAuthToken: "at_oauth_token_123",
				},
			},
			expectedHeader: "Authorization",
			expectedValue:  "Bearer at_oauth_token_123",
		},
		{
			name: "API Key Authentication",
			config: config.ProviderConfig{
				Name:   "api-key-test",
				Type:   "anthropic",
				APIKey: "sk-ant-api-key-456",
				Model:  "claude-3-5-sonnet-20250114",
			},
			expectedHeader: "x-api-key",
			expectedValue:  "sk-ant-api-key-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, err := NewAnthropicProvider(tt.config)
			if err != nil {
				t.Fatalf("Failed to create provider: %v", err)
			}

			// Verify the provider was configured correctly
			if tt.config.Auth != nil && tt.config.Auth.Type == "oauth" {
				if provider.authCfg == nil {
					t.Error("Expected OAuth config to be set")
				}
				if provider.authCfg.Type != "oauth" {
					t.Errorf("Expected OAuth auth type, got '%s'", provider.authCfg.Type)
				}
			}

			// Note: Full header testing requires integration tests with mock server
			// Unit tests focus on configuration correctness
		})
	}
}

func TestAnthropicProviderRefreshToken(t *testing.T) {
	provider := &AnthropicProvider{
		name:   "test-provider",
		apiKey: "at_token_123",
		model:  "claude-3-5-sonnet-20250114",
		authCfg: &config.AuthConfig{
			Type:         "oauth",
			OAuthToken:   "at_token_123",
			RefreshToken: "rt_refresh_456",
			ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(), // Expired
			// No ClientID — refresh should fail with "client ID is required"
		},
		client: &http.Client{Timeout: 30 * time.Second},
	}

	// Test that refresh is attempted for expired token but fails without client ID
	err := provider.refreshOAuthToken()
	if err == nil {
		t.Error("Expected error for token refresh without client ID")
	}

	if !strings.Contains(err.Error(), "client ID is required") {
		t.Errorf("Expected 'client ID is required' error, got: %v", err)
	}
}

func TestAnthropicProviderRefreshNotNeeded(t *testing.T) {
	// Test with valid token (not expired)
	provider := &AnthropicProvider{
		name:   "test-provider",
		apiKey: "at_token_123",
		model:  "claude-3-5-sonnet-20250114",
		authCfg: &config.AuthConfig{
			Type:       "oauth",
			OAuthToken: "at_token_123",
			ExpiresAt:  time.Now().Add(1 * time.Hour).Unix(), // Valid
		},
		client: &http.Client{Timeout: 30 * time.Second},
	}

	// Should not attempt refresh
	err := provider.refreshOAuthToken()
	if err != nil {
		t.Errorf("Expected no error for valid token, got: %v", err)
	}
}

func TestAnthropicProviderAPIKeyFallback(t *testing.T) {
	// Test with API key authentication (no OAuth)
	provider := &AnthropicProvider{
		name:    "test-provider",
		apiKey:  "sk-ant-api-key-123",
		model:   "claude-3-5-sonnet-20250114",
		authCfg: nil, // No OAuth config
		client:  &http.Client{Timeout: 30 * time.Second},
	}

	// Should not attempt refresh for API key auth
	err := provider.refreshOAuthToken()
	if err != nil {
		t.Errorf("Expected no error for API key auth, got: %v", err)
	}
}

func TestBuildChatMessages(t *testing.T) {
	cfg := config.AIConfig{
		DefaultProvider: "anthropic",
		Providers: []config.ProviderConfig{
			{
				Name:   "anthropic",
				Type:   "anthropic",
				APIKey: "test-key",
				Model:  "claude-3-5-sonnet-20250114",
			},
		},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Create a mock session
	session := &sessions.Session{
		Key:       "test-session",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	userMessage := "Hello, how are you?"

	messages, err := router.buildChatMessages(session, userMessage)
	if err != nil {
		t.Fatalf("Failed to build chat messages: %v", err)
	}

	// Should have at least system message and user message
	if len(messages) < 2 {
		t.Errorf("Expected at least 2 messages, got %d", len(messages))
	}

	// Check system message
	if messages[0].Role != "system" {
		t.Errorf("Expected first message to be system role, got %s", messages[0].Role)
	}

	// Check user message
	lastMessage := messages[len(messages)-1]
	if lastMessage.Role != "user" {
		t.Errorf("Expected last message to be user role, got %s", lastMessage.Role)
	}

	if lastMessage.Content != userMessage {
		t.Errorf("Expected user message content '%s', got '%s'", userMessage, lastMessage.Content)
	}
}

func TestNewOpenAIProvider(t *testing.T) {
	cfg := config.ProviderConfig{
		Name:   "openai-test",
		Type:   "openai",
		APIKey: "sk-openai-test-key",
		Model:  "gpt-4",
	}

	provider, err := NewOpenAIProvider(cfg)
	if err != nil {
		t.Fatalf("Failed to create OpenAI provider: %v", err)
	}

	if provider.Name() != "openai-test" {
		t.Errorf("Expected provider name 'openai-test', got %s", provider.Name())
	}

	if provider.model != "gpt-4" {
		t.Errorf("Expected model 'gpt-4', got %s", provider.model)
	}

	if provider.apiKey != "sk-openai-test-key" {
		t.Errorf("Expected API key 'sk-openai-test-key', got %s", provider.apiKey)
	}
}

func TestNewOpenAIProviderNoAPIKey(t *testing.T) {
	cfg := config.ProviderConfig{
		Name:  "openai-test",
		Type:  "openai",
		Model: "gpt-4",
		// Missing APIKey
	}

	_, err := NewOpenAIProvider(cfg)
	if err == nil {
		t.Error("Expected error for missing API key")
	}

	if !strings.Contains(err.Error(), "API key is required") {
		t.Errorf("Expected 'API key is required' error, got: %v", err)
	}
}

func TestNewRouterEmptyProviders(t *testing.T) {
	// Test that router can be created with no providers (for testing scenarios)
	cfg := config.AIConfig{
		DefaultProvider: "",
		Providers:       []config.ProviderConfig{},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Expected no error for empty providers, got: %v", err)
	}

	if router == nil {
		t.Fatal("Expected router to be created, got nil")
	}

	if len(router.providers) != 0 {
		t.Errorf("Expected 0 providers, got %d", len(router.providers))
	}

	// Verify HasProviders returns false
	if router.HasProviders() {
		t.Error("Expected HasProviders() to return false for empty router")
	}
}

func TestRouterWithMockProvider(t *testing.T) {
	// Create router with empty config
	cfg := config.AIConfig{
		DefaultProvider: "mock",
		Providers:       []config.ProviderConfig{},
	}

	router, err := NewRouter(cfg, nil)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Register a mock provider
	mockProvider := NewMockProvider("mock")
	mockProvider.AddResponse("Hello from mock!", nil)
	router.RegisterProvider("mock", mockProvider)

	// Verify provider was registered
	if !router.HasProviders() {
		t.Error("Expected HasProviders() to return true after registering provider")
	}

	if _, exists := router.providers["mock"]; !exists {
		t.Error("Expected mock provider to be registered")
	}
}

func TestMockProviderBasics(t *testing.T) {
	mock := NewMockProvider("test-mock")

	// Test Name()
	if mock.Name() != "test-mock" {
		t.Errorf("Expected name 'test-mock', got '%s'", mock.Name())
	}

	// Test default response (no responses configured)
	resp, err := mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if resp.Content != "Mock response" {
		t.Errorf("Expected default 'Mock response', got '%s'", resp.Content)
	}

	// Verify call was recorded
	if mock.GetCallCount() != 1 {
		t.Errorf("Expected 1 call, got %d", mock.GetCallCount())
	}
}

func TestMockProviderConfiguredResponses(t *testing.T) {
	mock := NewMockProvider("test-mock")

	// Configure responses
	mock.AddResponse("First response", nil)
	mock.AddResponse("Second response", []ToolCall{
		{ID: "call_1", Name: "TestTool", Args: map[string]interface{}{"arg": "value"}},
	})

	// First call
	resp1, err := mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "first"}},
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if resp1.Content != "First response" {
		t.Errorf("Expected 'First response', got '%s'", resp1.Content)
	}

	// Second call
	resp2, err := mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "second"}},
	})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if resp2.Content != "Second response" {
		t.Errorf("Expected 'Second response', got '%s'", resp2.Content)
	}
	if len(resp2.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(resp2.ToolCalls))
	}

	// Verify both calls recorded
	if mock.GetCallCount() != 2 {
		t.Errorf("Expected 2 calls, got %d", mock.GetCallCount())
	}

	// Check last call
	lastCall := mock.LastCall()
	if lastCall == nil {
		t.Fatal("Expected last call to not be nil")
	}
	if lastCall.Request.Messages[0].Content != "second" {
		t.Errorf("Expected last call message 'second', got '%s'", lastCall.Request.Messages[0].Content)
	}
}

func TestMockProviderErrorResponse(t *testing.T) {
	mock := NewMockProvider("test-mock")

	// Configure error response
	expectedErr := &MockError{Message: "API error"}
	mock.AddErrorResponse(expectedErr)

	// Call should return error
	_, err := mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	})
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if err.Error() != "API error" {
		t.Errorf("Expected 'API error', got '%s'", err.Error())
	}

	// Error calls should still be recorded
	if mock.GetCallCount() != 1 {
		t.Errorf("Expected 1 call, got %d", mock.GetCallCount())
	}
}

func TestMockProviderReset(t *testing.T) {
	mock := NewMockProvider("test-mock")

	// Add responses and make calls
	mock.AddResponse("Response 1", nil)
	mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	})

	// Reset
	mock.Reset()

	// Verify cleared
	if mock.GetCallCount() != 0 {
		t.Errorf("Expected 0 calls after reset, got %d", mock.GetCallCount())
	}
	if mock.LastCall() != nil {
		t.Error("Expected LastCall() to return nil after reset")
	}

	// Should get default response again
	resp, _ := mock.GenerateResponse(nil, &GenerateRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
	})
	if resp.Content != "Mock response" {
		t.Errorf("Expected default response after reset, got '%s'", resp.Content)
	}
}

// MockError is a simple error type for testing
type MockError struct {
	Message string
}

func (e *MockError) Error() string {
	return e.Message
}
