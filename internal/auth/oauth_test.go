// Package auth provides OAuth token detection and mapping for Claude Code API compatibility.
package auth

import (
	"reflect"
	"testing"
)

func TestIsOAuthToken(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "Valid OAuth token",
			token:    "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",
			expected: true,
		},
		{
			name:     "OAuth token with different suffix",
			token:    "sk-ant-oat01-differentkeyherebuthastheprefix",
			expected: true,
		},
		{
			name:     "Regular API token",
			token:    "sk-ant-api03-regulartoken",
			expected: false,
		},
		{
			name:     "Empty token",
			token:    "",
			expected: false,
		},
		{
			name:     "Random string",
			token:    "randomstring",
			expected: false,
		},
		{
			name:     "OAuth prefix in middle",
			token:    "prefix-sk-ant-oat-suffix",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsOAuthToken(tt.token)
			if result != tt.expected {
				t.Errorf("IsOAuthToken(%q) = %v, expected %v", tt.token, result, tt.expected)
			}
		})
	}
}

func TestGetOAuthTokenInfo(t *testing.T) {
	tests := []struct {
		name          string
		token         string
		expectedOAuth bool
	}{
		{
			name:          "OAuth token",
			token:         "sk-ant-oat01-test",
			expectedOAuth: true,
		},
		{
			name:          "Regular token",
			token:         "sk-ant-api03-test",
			expectedOAuth: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := GetOAuthTokenInfo(tt.token)

			if info.IsOAuthToken != tt.expectedOAuth {
				t.Errorf("GetOAuthTokenInfo(%q).IsOAuthToken = %v, expected %v",
					tt.token, info.IsOAuthToken, tt.expectedOAuth)
			}

			if info.Token != tt.token {
				t.Errorf("GetOAuthTokenInfo(%q).Token = %q, expected %q",
					tt.token, info.Token, tt.token)
			}

			if tt.expectedOAuth {
				if len(info.RequiredHeaders) == 0 {
					t.Error("OAuth token should have required headers")
				}

				// Check for key required headers
				requiredHeaders := []string{
					"anthropic-beta",
					"anthropic-dangerous-direct-browser-access",
					"user-agent",
					"x-app",
					"anthropic-version",
				}

				for _, header := range requiredHeaders {
					if _, exists := info.RequiredHeaders[header]; !exists {
						t.Errorf("Missing required OAuth header: %s", header)
					}
				}
			}
		})
	}
}

func TestMapToolForOAuth(t *testing.T) {
	tests := []struct {
		name         string
		toolName     string
		expectedName string
		expectedOK   bool
	}{
		// Compatible tools
		{
			name:         "read tool",
			toolName:     "read",
			expectedName: "Read",
			expectedOK:   true,
		},
		{
			name:         "web_search tool",
			toolName:     "web_search",
			expectedName: "WebSearch",
			expectedOK:   true,
		},
		{
			name:         "web_search versioned",
			toolName:     "web_search_20250305",
			expectedName: "WebSearch",
			expectedOK:   true,
		},
		{
			name:         "web_fetch tool",
			toolName:     "web_fetch",
			expectedName: "WebFetch",
			expectedOK:   true,
		},
		{
			name:         "write tool",
			toolName:     "write",
			expectedName: "Write",
			expectedOK:   true,
		},
		{
			name:         "bash tool",
			toolName:     "bash",
			expectedName: "Bash",
			expectedOK:   true,
		},
		// Forbidden tools
		{
			name:         "message tool (forbidden)",
			toolName:     "message",
			expectedName: "",
			expectedOK:   false,
		},
		{
			name:         "exec tool (forbidden)",
			toolName:     "exec",
			expectedName: "",
			expectedOK:   false,
		},
		{
			name:         "cron tool (forbidden)",
			toolName:     "cron",
			expectedName: "",
			expectedOK:   false,
		},
		// Unknown tools
		{
			name:         "unknown tool",
			toolName:     "unknown_tool",
			expectedName: "",
			expectedOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, ok := MapToolForOAuth(tt.toolName)

			if ok != tt.expectedOK {
				t.Errorf("MapToolForOAuth(%q) ok = %v, expected %v",
					tt.toolName, ok, tt.expectedOK)
			}

			if name != tt.expectedName {
				t.Errorf("MapToolForOAuth(%q) name = %q, expected %q",
					tt.toolName, name, tt.expectedName)
			}
		})
	}
}

func TestFilterToolsForOAuth(t *testing.T) {
	tests := []struct {
		name     string
		tools    []string
		expected []string
	}{
		{
			name:     "Mixed tools",
			tools:    []string{"read", "write", "message", "web_search", "exec"},
			expected: []string{"Read", "Write", "WebSearch"},
		},
		{
			name:     "All compatible tools",
			tools:    []string{"read", "write", "edit"},
			expected: []string{"Read", "Write", "Edit"},
		},
		{
			name:     "All forbidden tools",
			tools:    []string{"message", "exec", "cron"},
			expected: []string{},
		},
		{
			name:     "Empty tools",
			tools:    []string{},
			expected: []string{},
		},
		{
			name:     "Versioned tools",
			tools:    []string{"web_search_20250305", "web_fetch_20250305", "message"},
			expected: []string{"WebSearch", "WebFetch"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FilterToolsForOAuth(tt.tools)

			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("FilterToolsForOAuth(%v) = %v, expected %v",
					tt.tools, result, tt.expected)
			}
		})
	}
}

func TestValidateOAuthHeaders(t *testing.T) {
	validHeaders := map[string]string{
		"anthropic-beta": "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14",
		"anthropic-dangerous-direct-browser-access": "true",
		"user-agent":        "claude-cli/2.1.2 (external, cli)",
		"x-app":             "cli",
		"anthropic-version": "2023-06-01",
	}

	tests := []struct {
		name     string
		headers  map[string]string
		wantErr  bool
		errField string
	}{
		{
			name:    "Valid headers",
			headers: validHeaders,
			wantErr: false,
		},
		{
			name: "Missing anthropic-beta",
			headers: map[string]string{
				"anthropic-dangerous-direct-browser-access": "true",
				"user-agent":        "claude-cli/2.1.2 (external, cli)",
				"x-app":             "cli",
				"anthropic-version": "2023-06-01",
			},
			wantErr:  true,
			errField: "anthropic-beta",
		},
		{
			name: "Wrong anthropic-beta value",
			headers: map[string]string{
				"anthropic-beta": "wrong-value",
				"anthropic-dangerous-direct-browser-access": "true",
				"user-agent":        "claude-cli/2.1.2 (external, cli)",
				"x-app":             "cli",
				"anthropic-version": "2023-06-01",
			},
			wantErr:  true,
			errField: "anthropic-beta",
		},
		{
			name: "Missing user-agent",
			headers: map[string]string{
				"anthropic-beta": "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14",
				"anthropic-dangerous-direct-browser-access": "true",
				"x-app":             "cli",
				"anthropic-version": "2023-06-01",
			},
			wantErr:  true,
			errField: "user-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOAuthHeaders(tt.headers)

			if tt.wantErr && err == nil {
				t.Error("Expected error but got none")
			}

			if !tt.wantErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.wantErr && err != nil {
				if oauthErr, ok := err.(*OAuthValidationError); ok {
					if oauthErr.Field != tt.errField {
						t.Errorf("Expected error field %q, got %q", tt.errField, oauthErr.Field)
					}
				}
			}
		})
	}
}

func TestIsSystemPromptValidForOAuth(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt interface{}
		expected     bool
	}{
		{
			name: "Valid array format",
			systemPrompt: []interface{}{
				map[string]interface{}{"type": "text", "text": "You are Claude Code."},
			},
			expected: true,
		},
		{
			name: "Valid array with multiple items",
			systemPrompt: []interface{}{
				map[string]interface{}{"type": "text", "text": "You are Claude Code."},
				map[string]interface{}{"type": "text", "text": "Additional context."},
			},
			expected: true,
		},
		{
			name:         "String format (invalid for OAuth)",
			systemPrompt: "You are a helpful assistant.",
			expected:     false,
		},
		{
			name:         "Empty array",
			systemPrompt: []interface{}{},
			expected:     false,
		},
		{
			name: "Array with invalid item structure",
			systemPrompt: []interface{}{
				map[string]interface{}{"invalid": "structure"},
			},
			expected: false,
		},
		{
			name:         "Nil",
			systemPrompt: nil,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsSystemPromptValidForOAuth(tt.systemPrompt)
			if result != tt.expected {
				t.Errorf("IsSystemPromptValidForOAuth(%v) = %v, expected %v",
					tt.systemPrompt, result, tt.expected)
			}
		})
	}
}

func TestConvertSystemPromptForOAuth(t *testing.T) {
	tests := []struct {
		name         string
		systemPrompt interface{}
		expected     interface{}
	}{
		{
			name:         "String to array conversion",
			systemPrompt: "You are a helpful assistant.",
			expected: []map[string]interface{}{
				{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
				{"type": "text", "text": "You are a helpful assistant."},
			},
		},
		{
			name: "Array unchanged",
			systemPrompt: []interface{}{
				map[string]interface{}{"type": "text", "text": "Existing prompt"},
			},
			expected: []interface{}{
				map[string]interface{}{"type": "text", "text": "Existing prompt"},
			},
		},
		{
			name:         "Unknown type to default",
			systemPrompt: 42,
			expected: []map[string]interface{}{
				{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertSystemPromptForOAuth(tt.systemPrompt)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ConvertSystemPromptForOAuth(%v) = %v, expected %v",
					tt.systemPrompt, result, tt.expected)
			}
		})
	}
}

func TestGetRequiredOAuthHeaders(t *testing.T) {
	headers := GetRequiredOAuthHeaders()

	// Check that all required headers are present
	requiredKeys := []string{
		"anthropic-beta",
		"anthropic-dangerous-direct-browser-access",
		"user-agent",
		"x-app",
		"anthropic-version",
	}

	for _, key := range requiredKeys {
		if _, exists := headers[key]; !exists {
			t.Errorf("Missing required OAuth header: %s", key)
		}
	}

	// Check specific values
	if headers["anthropic-beta"] != "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14" {
		t.Error("Incorrect anthropic-beta header value")
	}

	if headers["user-agent"] != "claude-cli/2.1.2 (external, cli)" {
		t.Error("Incorrect user-agent header value")
	}

	if headers["x-app"] != "cli" {
		t.Error("Incorrect x-app header value")
	}
}
