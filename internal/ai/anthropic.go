package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"context"

	"conduit/internal/auth/oauthflow"
	"conduit/internal/config"
)

// AnthropicProvider implements the Anthropic API
type AnthropicProvider struct {
	name    string
	apiKey  string
	model   string
	authCfg *config.AuthConfig
	client  *http.Client
	isOAuth bool
}

// isOAuthToken detects if the token is an OAuth token (Pro/Max subscription)
// OAuth tokens have the prefix "sk-ant-oat" (e.g., sk-ant-oat01-...)
func isOAuthToken(token string) bool {
	return strings.Contains(token, "sk-ant-oat")
}

// NewAnthropicProvider creates a new Anthropic provider.
// Token resolution priority:
//  1. ~/.conduit/auth.json (CLI-acquired OAuth token)
//  2. Config auth.oauth_token / ANTHROPIC_OAUTH_TOKEN env var
//  3. Config api_key / ANTHROPIC_API_KEY env var
func NewAnthropicProvider(cfg config.ProviderConfig) (*AnthropicProvider, error) {
	var authToken string
	var authCfg *config.AuthConfig

	// 1. Try loading CLI-stored OAuth token first.
	if stored, err := oauthflow.LoadProviderToken("anthropic"); err == nil && stored != nil && !stored.IsExpired() {
		log.Printf("[Anthropic] Using OAuth token from ~/.conduit/auth.json")
		authToken = stored.AccessToken
		authCfg = &config.AuthConfig{
			Type:         "oauth",
			OAuthToken:   stored.AccessToken,
			RefreshToken: stored.RefreshToken,
			ExpiresAt:    stored.ExpiresAt,
			ClientID:     stored.ClientID,
		}
	}

	// 2. Fall back to config / env var.
	if authToken == "" {
		if cfg.Auth != nil && cfg.Auth.Type == "oauth" && cfg.Auth.OAuthToken != "" {
			authToken = cfg.Auth.OAuthToken
			authCfg = cfg.Auth
		} else if cfg.APIKey != "" {
			authToken = cfg.APIKey
		} else {
			return nil, fmt.Errorf("either OAuth token or API key is required for Anthropic provider")
		}
	}

	// Detect if this is an OAuth token based on prefix.
	isOAuth := isOAuthToken(authToken)

	return &AnthropicProvider{
		name:    cfg.Name,
		apiKey:  authToken,
		model:   cfg.Model,
		authCfg: authCfg,
		client:  &http.Client{Timeout: 120 * time.Second},
		isOAuth: isOAuth,
	}, nil
}

func (a *AnthropicProvider) Name() string {
	return a.name
}

func (a *AnthropicProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	// Determine which model to use
	modelToUse := a.model
	if req.Model != "" {
		modelToUse = req.Model
	}

	// Refresh OAuth token if needed
	if err := a.refreshOAuthToken(); err != nil {
		return nil, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	// Build messages, injecting Claude Code identity for OAuth
	messages := req.Messages
	var systemBlocks []map[string]interface{}

	if a.isOAuth {
		// OAuth requires Claude Code identity as first system block
		systemBlocks = append(systemBlocks, map[string]interface{}{
			"type": "text",
			"text": "You are Claude Code, Anthropic's official CLI for Claude.",
		})
	}

	// Check if first message is system and convert to block format
	if len(messages) > 0 && messages[0].Role == "system" {
		systemBlocks = append(systemBlocks, map[string]interface{}{
			"type": "text",
			"text": messages[0].Content,
		})
		messages = messages[1:] // Remove system from messages array
	}

	// Convert messages to Anthropic format (handles tool results)
	anthropicMessages := a.convertMessagesToAnthropic(messages)

	// Anthropic API request format (modelToUse already set at top of function)
	anthropicReq := map[string]interface{}{
		"model":      modelToUse,
		"max_tokens": req.MaxTokens,
		"messages":   anthropicMessages,
	}

	// Add system prompt - as array for OAuth, string for API key
	if len(systemBlocks) > 0 {
		if a.isOAuth {
			anthropicReq["system"] = systemBlocks
		} else {
			// For API key auth, use simple string format
			var systemText string
			for _, block := range systemBlocks {
				if text, ok := block["text"].(string); ok {
					if systemText != "" {
						systemText += "\n\n"
					}
					systemText += text
				}
			}
			anthropicReq["system"] = systemText
		}
	}

	// Add tools if provided (with OAuth name mapping if needed)
	if len(req.Tools) > 0 {
		convertedTools := a.convertToolsToAnthropic(req.Tools)
		if len(convertedTools) > 0 {
			anthropicReq["tools"] = convertedTools
		}
	}

	reqBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	// Use OAuth Bearer token or fall back to API key
	if a.isOAuth {
		httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)
		// Required headers for OAuth tokens - must match Claude Code exactly
		httpReq.Header.Set("accept", "application/json")
		httpReq.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14")
		httpReq.Header.Set("anthropic-dangerous-direct-browser-access", "true")
		httpReq.Header.Set("user-agent", "claude-cli/2.1.2 (external, cli)")
		httpReq.Header.Set("x-app", "cli")
	} else {
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("x-api-key", a.apiKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read body for error details
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: %d - %s", resp.StatusCode, string(bodyBytes))
	}

	var anthropicResp map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Log the model from API response (confirms what Anthropic actually used)
	respModel := ""
	if m, ok := anthropicResp["model"].(string); ok {
		respModel = m
	}

	// Parity check: verify requested model matches response model
	if modelToUse != "" && respModel != "" {
		// Normalize for comparison (handle version suffixes like -20251101)
		requestedBase := strings.Split(modelToUse, "-2025")[0] // Strip date suffix
		responseBase := strings.Split(respModel, "-2025")[0]

		if requestedBase != responseBase {
			log.Printf("[Anthropic] WARNING: Model mismatch! Requested: %s, Got: %s", modelToUse, respModel)
			// Return error that will surface to user
			return &GenerateResponse{
				Content: fmt.Sprintf("⚠️ Model mismatch detected!\nRequested: %s\nReceived: %s\n\nPlease try again or contact support.", modelToUse, respModel),
			}, nil
		}
	}

	// Extract content and tool calls from Anthropic response format
	content, toolCalls := a.parseAnthropicContent(anthropicResp)
	usage := a.parseAnthropicUsage(anthropicResp)

	return &GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

// convertMessagesToAnthropic converts messages to Anthropic API format
// This handles the special case of tool results which must be sent as user messages
func (a *AnthropicProvider) convertMessagesToAnthropic(messages []ChatMessage) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			result = append(result, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			// Build assistant message with potential tool_use blocks
			if len(msg.ToolCalls) > 0 {
				content := make([]map[string]interface{}, 0)
				if msg.Content != "" {
					content = append(content, map[string]interface{}{
						"type": "text",
						"text": msg.Content,
					})
				}
				for _, tc := range msg.ToolCalls {
					// Ensure tool input is always a valid JSON object for OAuth
					input := tc.Args
					if input == nil {
						input = make(map[string]interface{})
					}
					content = append(content, map[string]interface{}{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Name,
						"input": input,
					})
				}
				result = append(result, map[string]interface{}{
					"role":    "assistant",
					"content": content,
				})
			} else {
				result = append(result, map[string]interface{}{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
		case "tool":
			// Tool results must be sent as user messages with tool_result content
			result = append(result, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": msg.ToolCallID,
						"content":     msg.Content,
					},
				},
			})
		}
	}

	return result
}

// Claude Code tool names that are known to work with OAuth tokens
var claudeCodeTools = map[string]bool{
	"Read": true, "Write": true, "Edit": true, "Bash": true,
	"Grep": true, "Glob": true, "WebFetch": true, "WebSearch": true,
	"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true,
	"KillShell": true, "NotebookEdit": true, "Skill": true, "Task": true,
	"TaskOutput": true, "TodoWrite": true,
}

// convertToolsToAnthropic converts tool definitions to Anthropic format
// When using OAuth tokens, only Claude Code-compatible tools are included
func (a *AnthropicProvider) convertToolsToAnthropic(tools []Tool) []interface{} {
	anthropicTools := make([]interface{}, 0, len(tools))

	for _, tool := range tools {
		// For OAuth tokens, only include Claude Code-compatible tools
		if a.isOAuth && !claudeCodeTools[tool.Name] {
			continue
		}

		anthropicTools = append(anthropicTools, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	return anthropicTools
}

// parseAnthropicContent extracts content and tool calls from Anthropic response
func (a *AnthropicProvider) parseAnthropicContent(resp map[string]interface{}) (string, []ToolCall) {
	var content strings.Builder
	var toolCalls []ToolCall

	if contentArray, ok := resp["content"].([]interface{}); ok {
		for _, item := range contentArray {
			if contentObj, ok := item.(map[string]interface{}); ok {
				if contentType, ok := contentObj["type"].(string); ok {
					switch contentType {
					case "text":
						if text, ok := contentObj["text"].(string); ok {
							if content.Len() > 0 {
								content.WriteString("\n")
							}
							content.WriteString(text)
						}
					case "tool_use":
						// Parse tool call
						if toolCall := a.parseAnthropicToolCall(contentObj); toolCall != nil {
							toolCalls = append(toolCalls, *toolCall)
						}
					}
				}
			}
		}
	}

	return content.String(), toolCalls
}

// parseAnthropicToolCall extracts a tool call from Anthropic tool_use block
func (a *AnthropicProvider) parseAnthropicToolCall(toolObj map[string]interface{}) *ToolCall {
	id, hasID := toolObj["id"].(string)
	name, hasName := toolObj["name"].(string)
	input, hasInput := toolObj["input"].(map[string]interface{})

	if !hasID || !hasName || !hasInput {
		return nil
	}

	// Tool names now match Claude Code format directly, no conversion needed

	return &ToolCall{
		ID:   id,
		Name: name,
		Args: input,
	}
}

// parseAnthropicUsage extracts usage statistics from Anthropic response
func (a *AnthropicProvider) parseAnthropicUsage(resp map[string]interface{}) Usage {
	var usage Usage

	if usageObj, ok := resp["usage"].(map[string]interface{}); ok {
		if inputTokens, ok := usageObj["input_tokens"].(float64); ok {
			usage.PromptTokens = int(inputTokens)
		}
		if outputTokens, ok := usageObj["output_tokens"].(float64); ok {
			usage.CompletionTokens = int(outputTokens)
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	return usage
}

// refreshOAuthToken refreshes the OAuth token if needed.
func (a *AnthropicProvider) refreshOAuthToken() error {
	if a.authCfg == nil || a.authCfg.Type != "oauth" || a.authCfg.RefreshToken == "" {
		return nil // No refresh needed for API key auth
	}

	// Check if token needs refresh (expires within 5 minutes).
	if a.authCfg.ExpiresAt > time.Now().Add(5*time.Minute).Unix() {
		return nil // Token is still valid
	}

	log.Printf("[Anthropic] OAuth token expiring soon, refreshing...")

	newToken, err := oauthflow.RefreshToken(a.authCfg.RefreshToken, a.authCfg.ClientID)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	// Update in-memory state.
	a.apiKey = newToken.AccessToken
	a.authCfg.OAuthToken = newToken.AccessToken
	a.authCfg.ExpiresAt = newToken.ExpiresAt
	if newToken.RefreshToken != "" {
		a.authCfg.RefreshToken = newToken.RefreshToken
	}

	// Persist to disk.
	if err := oauthflow.SaveProviderToken("anthropic", newToken); err != nil {
		log.Printf("[Anthropic] WARNING: refreshed token in memory but failed to save to disk: %v", err)
	}

	log.Printf("[Anthropic] OAuth token refreshed, new expiry: %s",
		time.Unix(newToken.ExpiresAt, 0).Format(time.RFC3339))
	return nil
}
