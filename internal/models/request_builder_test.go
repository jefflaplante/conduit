// Package models provides request building utilities for the Anthropic API
package models

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRequestBuilder(t *testing.T) {
	token := "sk-ant-api03-test"

	t.Run("Basic request building", func(t *testing.T) {
		req, headers, err := NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithSystemPrompt("You are a helpful assistant.").
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		if req.Model != "claude-sonnet-4-20250514" {
			t.Errorf("Expected model claude-sonnet-4-20250514, got %s", req.Model)
		}

		if req.MaxTokens != 100 {
			t.Errorf("Expected max_tokens 100, got %d", req.MaxTokens)
		}

		if len(req.Messages) != 1 {
			t.Errorf("Expected 1 message, got %d", len(req.Messages))
		}

		if headers["Authorization"] != "Bearer "+token {
			t.Errorf("Expected Authorization header with Bearer token")
		}
	})

	t.Run("Request with tools", func(t *testing.T) {
		tools := []AnthropicTool{
			{Name: "web_search", Description: "Search the web", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "read", Description: "Read files", InputSchema: map[string]interface{}{"type": "object"}},
		}

		req, _, err := NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			WithTools(tools).
			Build()

		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		if len(req.Tools) != 2 {
			t.Errorf("Expected 2 tools, got %d", len(req.Tools))
		}
	})

	t.Run("Request with streaming", func(t *testing.T) {
		req, headers, err := NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			WithStreaming(true).
			Build()

		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		if req.Stream == nil || !*req.Stream {
			t.Error("Expected streaming to be enabled")
		}

		if headers["Accept"] != "text/event-stream" {
			t.Error("Expected Accept header for streaming")
		}
	})

	t.Run("Request with custom headers", func(t *testing.T) {
		customHeaders := map[string]string{
			"X-Custom-Header": "custom-value",
			"Another-Header":  "another-value",
		}

		_, headers, err := NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			WithHeaders(customHeaders).
			Build()

		if err != nil {
			t.Fatalf("Failed to build request: %v", err)
		}

		for k, v := range customHeaders {
			if headers[k] != v {
				t.Errorf("Expected header %s: %s, got %s", k, v, headers[k])
			}
		}
	})

	t.Run("Validation errors", func(t *testing.T) {
		// Missing token
		_, _, err := NewRequestBuilder().
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err == nil || !strings.Contains(err.Error(), "token is required") {
			t.Error("Expected token validation error")
		}

		// Missing model
		_, _, err = NewRequestBuilder().
			WithToken(token).
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err == nil || !strings.Contains(err.Error(), "model is required") {
			t.Error("Expected model validation error")
		}

		// Invalid max_tokens
		_, _, err = NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(0).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err == nil || !strings.Contains(err.Error(), "max_tokens must be positive") {
			t.Error("Expected max_tokens validation error")
		}

		// Missing messages
		_, _, err = NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			Build()

		if err == nil || !strings.Contains(err.Error(), "at least one message is required") {
			t.Error("Expected messages validation error")
		}
	})
}

func TestOAuthRequestBuilder(t *testing.T) {
	oauthToken := "sk-ant-oat01-test"
	regularToken := "sk-ant-api03-test"

	t.Run("Create OAuth request builder", func(t *testing.T) {
		builder, err := NewOAuthRequestBuilder(oauthToken)
		if err != nil {
			t.Fatalf("Failed to create OAuth request builder: %v", err)
		}

		req, headers, err := builder.
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err != nil {
			t.Fatalf("Failed to build OAuth request: %v", err)
		}

		// Check OAuth-specific formatting
		if _, ok := req.System.([]map[string]interface{}); !ok {
			t.Error("OAuth request should have array system prompt")
		}

		// Check OAuth headers
		if headers["anthropic-beta"] == "" {
			t.Error("OAuth request should have anthropic-beta header")
		}

		if headers["x-app"] != "cli" {
			t.Error("OAuth request should have x-app header")
		}
	})

	t.Run("Reject regular token", func(t *testing.T) {
		_, err := NewOAuthRequestBuilder(regularToken)
		if err == nil || !strings.Contains(err.Error(), "not an OAuth token") {
			t.Error("Should reject regular tokens")
		}
	})

	t.Run("Add system prompt text", func(t *testing.T) {
		builder, err := NewOAuthRequestBuilder(oauthToken)
		if err != nil {
			t.Fatalf("Failed to create OAuth request builder: %v", err)
		}

		req, _, err := builder.
			WithSystemPromptText("Additional context").
			WithSystemPromptText("More context").
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err != nil {
			t.Fatalf("Failed to build OAuth request: %v", err)
		}

		systemArray, ok := req.System.([]map[string]interface{})
		if !ok {
			t.Fatal("System prompt should be array")
		}

		if len(systemArray) != 3 { // Default + 2 added
			t.Errorf("Expected 3 system prompt items, got %d", len(systemArray))
		}
	})

	t.Run("Filter OAuth compatible tools", func(t *testing.T) {
		builder, err := NewOAuthRequestBuilder(oauthToken)
		if err != nil {
			t.Fatalf("Failed to create OAuth request builder: %v", err)
		}

		tools := []AnthropicTool{
			{Name: "web_search", Description: "Search web", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "message", Description: "Send message", InputSchema: map[string]interface{}{"type": "object"}}, // Should be filtered
			{Name: "read", Description: "Read files", InputSchema: map[string]interface{}{"type": "object"}},
			{Name: "exec", Description: "Execute", InputSchema: map[string]interface{}{"type": "object"}}, // Should be filtered
		}

		req, _, err := builder.
			WithOAuthCompatibleTools(tools).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			Build()

		if err != nil {
			t.Fatalf("Failed to build OAuth request: %v", err)
		}

		if len(req.Tools) != 2 {
			t.Errorf("Expected 2 compatible tools, got %d. Tools: %v", len(req.Tools), req.Tools)
		}

		// Check tool names are mapped correctly
		expectedTools := map[string]bool{
			"WebSearch": true,
			"Read":      true,
		}

		for _, tool := range req.Tools {
			if !expectedTools[tool.Name] {
				t.Errorf("Unexpected tool: %s", tool.Name)
			}
		}
	})

	t.Run("Validate OAuth headers", func(t *testing.T) {
		builder, err := NewOAuthRequestBuilder(oauthToken)
		if err != nil {
			t.Fatalf("Failed to create OAuth request builder: %v", err)
		}

		builder.
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			})

		err = builder.ValidateOAuthHeaders()
		if err != nil {
			t.Errorf("OAuth headers validation failed: %v", err)
		}
	})
}

func TestQuickOAuthRequest(t *testing.T) {
	oauthToken := "sk-ant-oat01-test"
	regularToken := "sk-ant-api03-test"

	t.Run("Valid quick OAuth request", func(t *testing.T) {
		req, headers, err := QuickOAuthRequest(
			oauthToken,
			"claude-sonnet-4-20250514",
			100,
			"Hello, world!",
		)

		if err != nil {
			t.Fatalf("Failed to create quick OAuth request: %v", err)
		}

		if req.Model != "claude-sonnet-4-20250514" {
			t.Error("Model not set correctly")
		}

		if req.MaxTokens != 100 {
			t.Error("MaxTokens not set correctly")
		}

		if len(req.Messages) != 1 || req.Messages[0].Content != "Hello, world!" {
			t.Error("Message not set correctly")
		}

		if headers["x-app"] != "cli" {
			t.Error("OAuth headers not set correctly")
		}
	})

	t.Run("Reject regular token", func(t *testing.T) {
		_, _, err := QuickOAuthRequest(
			regularToken,
			"claude-sonnet-4-20250514",
			100,
			"Hello, world!",
		)

		if err == nil || !strings.Contains(err.Error(), "not an OAuth token") {
			t.Error("Should reject regular tokens")
		}
	})
}

func TestBuildHTTPRequest(t *testing.T) {
	token := "sk-ant-api03-test"
	apiURL := "https://api.anthropic.com/v1/messages"

	t.Run("Build HTTP request", func(t *testing.T) {
		httpReq, err := NewRequestBuilder().
			WithToken(token).
			WithModel("claude-sonnet-4-20250514").
			WithMaxTokens(100).
			WithMessages([]AnthropicMessage{
				{Role: "user", Content: "Hello"},
			}).
			BuildHTTPRequest(apiURL)

		if err != nil {
			t.Fatalf("Failed to build HTTP request: %v", err)
		}

		if httpReq.Method != http.MethodPost {
			t.Errorf("Expected POST method, got %s", httpReq.Method)
		}

		if httpReq.URL.String() != apiURL {
			t.Errorf("Expected URL %s, got %s", apiURL, httpReq.URL.String())
		}

		if httpReq.Header.Get("Authorization") != "Bearer "+token {
			t.Error("Authorization header not set correctly")
		}

		if httpReq.Header.Get("Content-Type") != "application/json" {
			t.Error("Content-Type header not set correctly")
		}

		// Check body is valid JSON
		body, err := io.ReadAll(httpReq.Body)
		if err != nil {
			t.Fatalf("Failed to read request body: %v", err)
		}

		var req AnthropicRequest
		if err := req.FromJSON(body); err != nil {
			t.Errorf("Request body is not valid JSON: %v", err)
		}
	})
}

func TestRequestBuilderFromOptions(t *testing.T) {
	opts := RequestOptions{
		Token:     "sk-ant-api03-test",
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	req, _, err := RequestBuilderFromOptions(opts).Build()

	if err != nil {
		t.Fatalf("Failed to build request from options: %v", err)
	}

	if req.Model != opts.Model {
		t.Error("Model not preserved from options")
	}

	if req.MaxTokens != opts.MaxTokens {
		t.Error("MaxTokens not preserved from options")
	}
}
