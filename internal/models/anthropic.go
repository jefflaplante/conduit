// Package models provides data structures and utilities for interacting with the Anthropic API
package models

import (
	"encoding/json"
	"fmt"

	"conduit/internal/auth"
)

// AnthropicRequest represents a request to the Anthropic API
type AnthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	System      interface{}        `json:"system,omitempty"`
	Messages    []AnthropicMessage `json:"messages"`
	Tools       []AnthropicTool    `json:"tools,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	TopK        *int               `json:"top_k,omitempty"`
	Stream      *bool              `json:"stream,omitempty"`
}

// AnthropicMessage represents a message in the conversation
type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// AnthropicTool represents a tool definition for the Anthropic API
type AnthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// AnthropicResponse represents a response from the Anthropic API
type AnthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// AnthropicError represents an error response from the Anthropic API
type AnthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *AnthropicError) Error() string {
	return fmt.Sprintf("Anthropic API error (%s): %s", e.Type, e.Message)
}

// RequestOptions contains configuration options for building Anthropic requests
type RequestOptions struct {
	Token        string
	Model        string
	MaxTokens    int
	SystemPrompt interface{}
	Messages     []AnthropicMessage
	Tools        []AnthropicTool
	Temperature  *float64
	TopP         *float64
	TopK         *int
	Stream       *bool
	Headers      map[string]string
}

// BuildAnthropicRequest creates an AnthropicRequest with proper OAuth formatting if needed
func BuildAnthropicRequest(opts RequestOptions) (*AnthropicRequest, map[string]string, error) {
	req := &AnthropicRequest{
		Model:       opts.Model,
		MaxTokens:   opts.MaxTokens,
		Messages:    opts.Messages,
		Temperature: opts.Temperature,
		TopP:        opts.TopP,
		TopK:        opts.TopK,
		Stream:      opts.Stream,
	}

	// Check if this is an OAuth token
	oauthInfo := auth.GetOAuthTokenInfo(opts.Token)

	if oauthInfo.IsOAuthToken {
		// Handle OAuth-specific formatting

		// 1. System prompt must be in array format for OAuth
		if opts.SystemPrompt != nil {
			if !auth.IsSystemPromptValidForOAuth(opts.SystemPrompt) {
				req.System = auth.ConvertSystemPromptForOAuth(opts.SystemPrompt)
			} else {
				req.System = opts.SystemPrompt
			}
		} else {
			// Default system prompt for OAuth
			req.System = []map[string]interface{}{
				{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
			}
		}

		// 2. Handle tools for OAuth compatibility
		if len(opts.Tools) > 0 {
			var oauthTools []AnthropicTool
			for _, tool := range opts.Tools {
				// Check if tool is already OAuth-compatible (already mapped)
				if auth.IsValidOAuthTool(tool.Name) {
					// Tool is already properly mapped, use as-is
					oauthTools = append(oauthTools, tool)
				} else if mappedName, compatible := auth.MapToolForOAuth(tool.Name); compatible {
					// Tool needs mapping
					oauthTool := AnthropicTool{
						Name:        mappedName,
						Description: tool.Description,
						InputSchema: tool.InputSchema,
					}
					oauthTools = append(oauthTools, oauthTool)
				}
				// Skip incompatible tools - they're filtered out
			}
			req.Tools = oauthTools
		}

		// 3. Build headers with OAuth requirements
		headers := make(map[string]string)

		// Start with any provided headers
		for k, v := range opts.Headers {
			headers[k] = v
		}

		// Add/override required OAuth headers
		requiredHeaders := auth.GetRequiredOAuthHeaders()
		for k, v := range requiredHeaders {
			headers[k] = v
		}

		// Set authorization header
		headers["Authorization"] = "Bearer " + opts.Token

		// Set content type
		if headers["Content-Type"] == "" {
			headers["Content-Type"] = "application/json"
		}

		// Set accept header for streaming vs non-streaming
		if opts.Stream != nil && *opts.Stream {
			headers["Accept"] = "text/event-stream"
		} else {
			headers["Accept"] = "application/json"
		}

		return req, headers, nil
	} else {
		// Handle regular API key requests

		// System prompt can be string or array for regular tokens
		if opts.SystemPrompt != nil {
			req.System = opts.SystemPrompt
		}

		// Use tools as-is for regular tokens
		req.Tools = opts.Tools

		// Build headers for regular API key
		headers := make(map[string]string)

		// Start with any provided headers
		for k, v := range opts.Headers {
			headers[k] = v
		}

		// Set authorization header
		headers["Authorization"] = "Bearer " + opts.Token

		// Set content type
		if headers["Content-Type"] == "" {
			headers["Content-Type"] = "application/json"
		}

		// Set accept header for streaming vs non-streaming
		if opts.Stream != nil && *opts.Stream {
			headers["Accept"] = "text/event-stream"
		} else {
			headers["Accept"] = "application/json"
		}

		// Set anthropic-version if not already set
		if headers["anthropic-version"] == "" {
			headers["anthropic-version"] = "2023-06-01"
		}

		return req, headers, nil
	}
}

// ValidateRequest performs validation on an Anthropic request
func ValidateRequest(req *AnthropicRequest, token string) error {
	if req.Model == "" {
		return fmt.Errorf("model is required")
	}

	if req.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be positive")
	}

	if len(req.Messages) == 0 {
		return fmt.Errorf("at least one message is required")
	}

	// OAuth-specific validation
	if auth.IsOAuthToken(token) {
		// System prompt must be in array format for OAuth
		if req.System != nil && !auth.IsSystemPromptValidForOAuth(req.System) {
			return fmt.Errorf("system prompt must be in array format for OAuth tokens")
		}

		// Validate tools are OAuth-compatible
		for _, tool := range req.Tools {
			// Check if tool name is either mappable to OAuth or already a valid OAuth tool name
			if mappedName, canMap := auth.MapToolForOAuth(tool.Name); !canMap && !auth.IsValidOAuthTool(tool.Name) {
				return fmt.Errorf("tool %s is not compatible with OAuth tokens", tool.Name)
			} else if canMap && mappedName != tool.Name {
				// Tool name should already be mapped by this point
				return fmt.Errorf("tool %s should be mapped to %s for OAuth", tool.Name, mappedName)
			}
		}
	}

	return nil
}

// ToJSON serializes the request to JSON
func (req *AnthropicRequest) ToJSON() ([]byte, error) {
	return json.Marshal(req)
}

// FromJSON deserializes JSON into an AnthropicRequest
func (req *AnthropicRequest) FromJSON(data []byte) error {
	return json.Unmarshal(data, req)
}
