// Package models provides data structures and utilities for interacting with the Anthropic API
package models

import (
	"encoding/json"
	"testing"
)

func TestBuildAnthropicRequest_OAuth(t *testing.T) {
	oauthToken := "sk-ant-oat01-test"

	tests := []struct {
		name         string
		opts         RequestOptions
		expectError  bool
		validateFunc func(*AnthropicRequest, map[string]string) error
	}{
		{
			name: "OAuth request with string system prompt",
			opts: RequestOptions{
				Token:        oauthToken,
				Model:        "claude-sonnet-4-20250514",
				MaxTokens:    100,
				SystemPrompt: "You are a helpful assistant.",
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
			},
			validateFunc: func(req *AnthropicRequest, headers map[string]string) error {
				// System prompt should be converted to array
				if _, ok := req.System.([]map[string]interface{}); !ok {
					t.Error("System prompt should be converted to array for OAuth")
				}

				// Check required OAuth headers
				if headers["anthropic-beta"] == "" {
					t.Error("Missing anthropic-beta header")
				}

				if headers["user-agent"] != "claude-cli/2.1.2 (external, cli)" {
					t.Error("Incorrect user-agent header")
				}

				return nil
			},
		},
		{
			name: "OAuth request with tools filtering",
			opts: RequestOptions{
				Token:     oauthToken,
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Tools: []AnthropicTool{
					{Name: "web_search", Description: "Search the web"},
					{Name: "message", Description: "Send message"}, // Should be filtered out
					{Name: "read", Description: "Read file"},
				},
			},
			validateFunc: func(req *AnthropicRequest, headers map[string]string) error {
				// Should only have compatible tools
				if len(req.Tools) != 2 {
					t.Errorf("Expected 2 tools, got %d", len(req.Tools))
				}

				// Tools should be mapped to OAuth names
				expectedTools := map[string]bool{
					"WebSearch": true,
					"Read":      true,
				}

				for _, tool := range req.Tools {
					if !expectedTools[tool.Name] {
						t.Errorf("Unexpected tool: %s", tool.Name)
					}
				}

				return nil
			},
		},
		{
			name: "OAuth request with streaming",
			opts: RequestOptions{
				Token:     oauthToken,
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Stream: &[]bool{true}[0],
			},
			validateFunc: func(req *AnthropicRequest, headers map[string]string) error {
				if headers["Accept"] != "text/event-stream" {
					t.Error("Streaming should set Accept header to text/event-stream")
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, headers, err := BuildAnthropicRequest(tt.opts)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.validateFunc != nil && req != nil {
				if err := tt.validateFunc(req, headers); err != nil {
					t.Errorf("Validation failed: %v", err)
				}
			}
		})
	}
}

func TestBuildAnthropicRequest_Regular(t *testing.T) {
	regularToken := "sk-ant-api03-test"

	tests := []struct {
		name         string
		opts         RequestOptions
		validateFunc func(*AnthropicRequest, map[string]string) error
	}{
		{
			name: "Regular API key request",
			opts: RequestOptions{
				Token:        regularToken,
				Model:        "claude-sonnet-4-20250514",
				MaxTokens:    100,
				SystemPrompt: "You are a helpful assistant.",
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
			},
			validateFunc: func(req *AnthropicRequest, headers map[string]string) error {
				// System prompt should remain as string for regular tokens
				if _, ok := req.System.(string); !ok {
					t.Error("System prompt should remain as string for regular tokens")
				}

				// Should not have OAuth-specific headers
				if headers["x-app"] != "" {
					t.Error("Regular tokens should not have x-app header")
				}

				if headers["anthropic-version"] != "2023-06-01" {
					t.Error("Should have anthropic-version header")
				}

				return nil
			},
		},
		{
			name: "Regular token with tools (no filtering)",
			opts: RequestOptions{
				Token:     regularToken,
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Tools: []AnthropicTool{
					{Name: "web_search", Description: "Search the web"},
					{Name: "message", Description: "Send message"},
					{Name: "custom_tool", Description: "Custom tool"},
				},
			},
			validateFunc: func(req *AnthropicRequest, headers map[string]string) error {
				// All tools should be preserved for regular tokens
				if len(req.Tools) != 3 {
					t.Errorf("Expected 3 tools, got %d", len(req.Tools))
				}

				// Tool names should not be mapped
				expectedTools := []string{"web_search", "message", "custom_tool"}
				for i, tool := range req.Tools {
					if tool.Name != expectedTools[i] {
						t.Errorf("Expected tool %s, got %s", expectedTools[i], tool.Name)
					}
				}

				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, headers, err := BuildAnthropicRequest(tt.opts)

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.validateFunc != nil && req != nil {
				if err := tt.validateFunc(req, headers); err != nil {
					t.Errorf("Validation failed: %v", err)
				}
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	oauthToken := "sk-ant-oat01-test"
	regularToken := "sk-ant-api03-test"

	tests := []struct {
		name        string
		req         *AnthropicRequest
		token       string
		expectError bool
	}{
		{
			name: "Valid OAuth request",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				System: []map[string]interface{}{
					{"type": "text", "text": "System prompt"},
				},
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Tools: []AnthropicTool{
					{Name: "Read", Description: "Read files"},
				},
			},
			token:       oauthToken,
			expectError: false,
		},
		{
			name: "Invalid OAuth request - string system prompt",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				System:    "String system prompt", // Invalid for OAuth
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
			},
			token:       oauthToken,
			expectError: true,
		},
		{
			name: "Invalid OAuth request - incompatible tool",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				System: []map[string]interface{}{
					{"type": "text", "text": "System prompt"},
				},
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Tools: []AnthropicTool{
					{Name: "message", Description: "Forbidden for OAuth"},
				},
			},
			token:       oauthToken,
			expectError: true,
		},
		{
			name: "Valid regular request",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				System:    "String system prompt",
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
				Tools: []AnthropicTool{
					{Name: "message", Description: "Allowed for regular tokens"},
				},
			},
			token:       regularToken,
			expectError: false,
		},
		{
			name: "Invalid - missing model",
			req: &AnthropicRequest{
				MaxTokens: 100,
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
			},
			token:       regularToken,
			expectError: true,
		},
		{
			name: "Invalid - zero max tokens",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 0,
				Messages: []AnthropicMessage{
					{Role: "user", Content: "Hello"},
				},
			},
			token:       regularToken,
			expectError: true,
		},
		{
			name: "Invalid - no messages",
			req: &AnthropicRequest{
				Model:     "claude-sonnet-4-20250514",
				MaxTokens: 100,
				Messages:  []AnthropicMessage{},
			},
			token:       regularToken,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRequest(tt.req, tt.token)

			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestAnthropicRequestJSON(t *testing.T) {
	req := &AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 100,
		System: []map[string]interface{}{
			{"type": "text", "text": "System prompt"},
		},
		Messages: []AnthropicMessage{
			{Role: "user", Content: "Hello"},
		},
		Tools: []AnthropicTool{
			{Name: "Read", Description: "Read files", InputSchema: map[string]interface{}{"type": "object"}},
		},
		Temperature: &[]float64{0.7}[0],
		Stream:      &[]bool{false}[0],
	}

	// Test serialization
	jsonData, err := req.ToJSON()
	if err != nil {
		t.Fatalf("Failed to serialize to JSON: %v", err)
	}

	// Test deserialization
	var newReq AnthropicRequest
	if err := newReq.FromJSON(jsonData); err != nil {
		t.Fatalf("Failed to deserialize from JSON: %v", err)
	}

	// Compare key fields
	if newReq.Model != req.Model {
		t.Errorf("Model mismatch: got %s, expected %s", newReq.Model, req.Model)
	}

	if newReq.MaxTokens != req.MaxTokens {
		t.Errorf("MaxTokens mismatch: got %d, expected %d", newReq.MaxTokens, req.MaxTokens)
	}

	if len(newReq.Messages) != len(req.Messages) {
		t.Errorf("Messages length mismatch: got %d, expected %d", len(newReq.Messages), len(req.Messages))
	}

	if len(newReq.Tools) != len(req.Tools) {
		t.Errorf("Tools length mismatch: got %d, expected %d", len(newReq.Tools), len(req.Tools))
	}

	// Verify the JSON structure matches expected OAuth format
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(jsonData, &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal JSON for inspection: %v", err)
	}

	// Check system prompt is array format
	if system, ok := jsonMap["system"].([]interface{}); !ok {
		t.Error("System prompt should be array in JSON")
	} else if len(system) == 0 {
		t.Error("System prompt array should not be empty")
	}
}
