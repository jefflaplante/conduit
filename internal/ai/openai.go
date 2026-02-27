package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"conduit/internal/config"
)

// OpenAIProvider implements the OpenAI API
type OpenAIProvider struct {
	name   string
	apiKey string
	model  string
	client *http.Client
}

// NewOpenAIProvider creates a new OpenAI provider
func NewOpenAIProvider(cfg config.ProviderConfig) (*OpenAIProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("API key is required for OpenAI provider")
	}

	return &OpenAIProvider{
		name:   cfg.Name,
		apiKey: cfg.APIKey,
		model:  cfg.Model,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (o *OpenAIProvider) Name() string {
	return o.name
}

func (o *OpenAIProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	// OpenAI API request format
	openaiReq := map[string]interface{}{
		"model":      o.model,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
	}

	// Add tools if provided
	if len(req.Tools) > 0 {
		openaiReq["tools"] = o.convertToolsToOpenAI(req.Tools)
		openaiReq["tool_choice"] = "auto"
	}

	reqBody, err := json.Marshal(openaiReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error: %d", resp.StatusCode)
	}

	var openaiResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Extract content, tool calls, and usage from OpenAI response
	content, toolCalls := o.parseOpenAIContent(openaiResp)
	usage := o.parseOpenAIUsage(openaiResp)

	return &GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

// convertToolsToOpenAI converts tool definitions to OpenAI format
func (o *OpenAIProvider) convertToolsToOpenAI(tools []Tool) []interface{} {
	openaiTools := make([]interface{}, len(tools))
	for i, tool := range tools {
		openaiTools[i] = map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.Parameters,
			},
		}
	}
	return openaiTools
}

// parseOpenAIContent extracts content and tool calls from OpenAI response
func (o *OpenAIProvider) parseOpenAIContent(resp map[string]interface{}) (string, []ToolCall) {
	var content string
	var toolCalls []ToolCall

	if choices, ok := resp["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				// Extract text content
				if text, ok := message["content"].(string); ok {
					content = text
				}

				// Extract tool calls
				if toolCallsArray, ok := message["tool_calls"].([]interface{}); ok {
					for _, tc := range toolCallsArray {
						if toolCallObj, ok := tc.(map[string]interface{}); ok {
							if parsedCall := o.parseOpenAIToolCall(toolCallObj); parsedCall != nil {
								toolCalls = append(toolCalls, *parsedCall)
							}
						}
					}
				}
			}
		}
	}

	return content, toolCalls
}

// parseOpenAIToolCall extracts a tool call from OpenAI tool_calls array
func (o *OpenAIProvider) parseOpenAIToolCall(toolObj map[string]interface{}) *ToolCall {
	id, hasID := toolObj["id"].(string)

	function, hasFunction := toolObj["function"].(map[string]interface{})
	if !hasID || !hasFunction {
		return nil
	}

	name, hasName := function["name"].(string)
	argumentsStr, hasArgs := function["arguments"].(string)
	if !hasName || !hasArgs {
		return nil
	}

	// Parse JSON arguments
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argumentsStr), &args); err != nil {
		return nil
	}

	return &ToolCall{
		ID:   id,
		Name: name,
		Args: args,
	}
}

// parseOpenAIUsage extracts usage statistics from OpenAI response
func (o *OpenAIProvider) parseOpenAIUsage(resp map[string]interface{}) Usage {
	var usage Usage

	if usageObj, ok := resp["usage"].(map[string]interface{}); ok {
		if promptTokens, ok := usageObj["prompt_tokens"].(float64); ok {
			usage.PromptTokens = int(promptTokens)
		}
		if completionTokens, ok := usageObj["completion_tokens"].(float64); ok {
			usage.CompletionTokens = int(completionTokens)
		}
		if totalTokens, ok := usageObj["total_tokens"].(float64); ok {
			usage.TotalTokens = int(totalTokens)
		}
	}

	return usage
}
