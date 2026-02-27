// Package models provides request building utilities for the Anthropic API
package models

import (
	"bytes"
	"fmt"
	"net/http"

	"conduit/internal/auth"
)

// RequestBuilder provides a fluent interface for building Anthropic API requests
type RequestBuilder struct {
	opts RequestOptions
	err  error
}

// NewRequestBuilder creates a new RequestBuilder
func NewRequestBuilder() *RequestBuilder {
	return &RequestBuilder{
		opts: RequestOptions{
			Headers: make(map[string]string),
		},
	}
}

// WithToken sets the authentication token
func (rb *RequestBuilder) WithToken(token string) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Token = token
	return rb
}

// WithModel sets the model name
func (rb *RequestBuilder) WithModel(model string) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Model = model
	return rb
}

// WithMaxTokens sets the maximum number of tokens to generate
func (rb *RequestBuilder) WithMaxTokens(maxTokens int) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.MaxTokens = maxTokens
	return rb
}

// WithSystemPrompt sets the system prompt
func (rb *RequestBuilder) WithSystemPrompt(systemPrompt interface{}) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.SystemPrompt = systemPrompt
	return rb
}

// WithMessages sets the conversation messages
func (rb *RequestBuilder) WithMessages(messages []AnthropicMessage) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Messages = messages
	return rb
}

// WithTools sets the available tools
func (rb *RequestBuilder) WithTools(tools []AnthropicTool) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Tools = tools
	return rb
}

// WithTemperature sets the temperature parameter
func (rb *RequestBuilder) WithTemperature(temperature float64) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Temperature = &temperature
	return rb
}

// WithTopP sets the top_p parameter
func (rb *RequestBuilder) WithTopP(topP float64) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.TopP = &topP
	return rb
}

// WithTopK sets the top_k parameter
func (rb *RequestBuilder) WithTopK(topK int) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.TopK = &topK
	return rb
}

// WithStreaming enables or disables streaming
func (rb *RequestBuilder) WithStreaming(stream bool) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Stream = &stream
	return rb
}

// WithHeader adds a custom header
func (rb *RequestBuilder) WithHeader(key, value string) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	rb.opts.Headers[key] = value
	return rb
}

// WithHeaders adds multiple custom headers
func (rb *RequestBuilder) WithHeaders(headers map[string]string) *RequestBuilder {
	if rb.err != nil {
		return rb
	}
	for k, v := range headers {
		rb.opts.Headers[k] = v
	}
	return rb
}

// Build creates the final AnthropicRequest and headers
func (rb *RequestBuilder) Build() (*AnthropicRequest, map[string]string, error) {
	if rb.err != nil {
		return nil, nil, rb.err
	}

	// Validate required fields
	if rb.opts.Token == "" {
		return nil, nil, fmt.Errorf("token is required")
	}

	if rb.opts.Model == "" {
		return nil, nil, fmt.Errorf("model is required")
	}

	if rb.opts.MaxTokens <= 0 {
		return nil, nil, fmt.Errorf("max_tokens must be positive")
	}

	if len(rb.opts.Messages) == 0 {
		return nil, nil, fmt.Errorf("at least one message is required")
	}

	// Build the request with OAuth handling
	req, headers, err := BuildAnthropicRequest(rb.opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build request: %w", err)
	}

	// Validate the final request
	if err := ValidateRequest(req, rb.opts.Token); err != nil {
		return nil, nil, fmt.Errorf("request validation failed: %w", err)
	}

	return req, headers, nil
}

// BuildHTTPRequest creates a complete HTTP request ready to send to the Anthropic API
func (rb *RequestBuilder) BuildHTTPRequest(apiURL string) (*http.Request, error) {
	req, headers, err := rb.Build()
	if err != nil {
		return nil, err
	}

	// Serialize request body
	body, err := req.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	return httpReq, nil
}

// OAuthRequestBuilder is a specialized builder for OAuth requests
type OAuthRequestBuilder struct {
	*RequestBuilder
}

// NewOAuthRequestBuilder creates a new OAuthRequestBuilder with OAuth-specific defaults
func NewOAuthRequestBuilder(token string) (*OAuthRequestBuilder, error) {
	if !auth.IsOAuthToken(token) {
		return nil, fmt.Errorf("token is not an OAuth token")
	}

	rb := NewRequestBuilder().WithToken(token)

	// Set OAuth-specific defaults
	rb.opts.SystemPrompt = []map[string]interface{}{
		{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."},
	}

	return &OAuthRequestBuilder{rb}, nil
}

// WithSystemPromptText is a convenience method for adding text to the OAuth system prompt array
func (orb *OAuthRequestBuilder) WithSystemPromptText(text string) *OAuthRequestBuilder {
	if orb.err != nil {
		return orb
	}

	// Add text to the existing system prompt array
	if systemArray, ok := orb.opts.SystemPrompt.([]map[string]interface{}); ok {
		systemArray = append(systemArray, map[string]interface{}{
			"type": "text",
			"text": text,
		})
		orb.opts.SystemPrompt = systemArray
	}

	return orb
}

// WithOAuthCompatibleTools filters tools to only include OAuth-compatible ones
func (orb *OAuthRequestBuilder) WithOAuthCompatibleTools(tools []AnthropicTool) *OAuthRequestBuilder {
	if orb.err != nil {
		return orb
	}

	var compatibleTools []AnthropicTool
	for _, tool := range tools {
		mappedName, compatible := auth.MapToolForOAuth(tool.Name)
		// Debug logging (remove later)
		if compatible {
			tool.Name = mappedName
			compatibleTools = append(compatibleTools, tool)
		}
	}

	orb.opts.Tools = compatibleTools
	return orb
}

// ValidateOAuthHeaders validates that all required OAuth headers will be present
func (orb *OAuthRequestBuilder) ValidateOAuthHeaders() error {
	if orb.err != nil {
		return orb.err
	}

	// Build the request to get the headers that would be set
	_, headers, err := orb.Build()
	if err != nil {
		return err
	}

	// Validate OAuth headers
	return auth.ValidateOAuthHeaders(headers)
}

// BuildForOAuth is an alias for Build that emphasizes OAuth compatibility
func (orb *OAuthRequestBuilder) BuildForOAuth() (*AnthropicRequest, map[string]string, error) {
	return orb.Build()
}

// OAuth-specific method overrides to maintain chain type

// WithModel overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithModel(model string) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithModel(model)
	return orb
}

// WithMaxTokens overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithMaxTokens(maxTokens int) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithMaxTokens(maxTokens)
	return orb
}

// WithMessages overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithMessages(messages []AnthropicMessage) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithMessages(messages)
	return orb
}

// WithTemperature overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithTemperature(temperature float64) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithTemperature(temperature)
	return orb
}

// WithTopP overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithTopP(topP float64) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithTopP(topP)
	return orb
}

// WithTopK overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithTopK(topK int) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithTopK(topK)
	return orb
}

// WithStreaming overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithStreaming(stream bool) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithStreaming(stream)
	return orb
}

// WithHeader overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithHeader(key, value string) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithHeader(key, value)
	return orb
}

// WithHeaders overrides the embedded method to return *OAuthRequestBuilder
func (orb *OAuthRequestBuilder) WithHeaders(headers map[string]string) *OAuthRequestBuilder {
	orb.RequestBuilder = orb.RequestBuilder.WithHeaders(headers)
	return orb
}

// QuickOAuthRequest is a convenience function for creating simple OAuth requests
func QuickOAuthRequest(token, model string, maxTokens int, userMessage string) (*AnthropicRequest, map[string]string, error) {
	if !auth.IsOAuthToken(token) {
		return nil, nil, fmt.Errorf("token is not an OAuth token")
	}

	builder, err := NewOAuthRequestBuilder(token)
	if err != nil {
		return nil, nil, err
	}

	messages := []AnthropicMessage{
		{
			Role:    "user",
			Content: userMessage,
		},
	}

	return builder.
		WithModel(model).
		WithMaxTokens(maxTokens).
		WithMessages(messages).
		Build()
}

// RequestBuilderFromOptions creates a RequestBuilder from existing options
func RequestBuilderFromOptions(opts RequestOptions) *RequestBuilder {
	return &RequestBuilder{opts: opts}
}
