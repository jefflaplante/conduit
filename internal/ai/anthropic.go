package ai

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"context"

	"conduit/internal/auth/oauthflow"
	"conduit/internal/config"
	"conduit/internal/httpsafe"
	"conduit/internal/models"
)

// AnthropicProvider implements the Anthropic API
type AnthropicProvider struct {
	name    string
	apiKey  string
	model   string
	authCfg *config.AuthConfig
	client  *http.Client
	baseURL string // conduit-3dru: defaults to https://api.anthropic.com, overridable for proxies/tests
	isOAuth bool
	caching config.PromptCachingConfig // conduit-3dru: prompt caching behavior
	oauthMu sync.Mutex                 // protects apiKey, authCfg fields during OAuth refresh
	retry   retryPolicy                // conduit-31jg.46: 429/529/5xx backoff
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

	// conduit-3dru: resolve prompt caching config; nil = defaults (enabled).
	caching := config.DefaultPromptCachingConfig()
	if cfg.PromptCaching != nil {
		caching = *cfg.PromptCaching
	}

	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	// bd-29i: Use per-provider timeout with 300s default
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 300
	}

	return &AnthropicProvider{
		name:    cfg.Name,
		apiKey:  authToken,
		model:   cfg.Model,
		authCfg: authCfg,
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
		baseURL: baseURL,
		isOAuth: isOAuth,
		caching: caching,
		retry:   defaultAnthropicRetryPolicy,
	}, nil
}

func (a *AnthropicProvider) Name() string {
	return a.name
}

func (a *AnthropicProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	// Refresh OAuth token if needed
	if err := a.refreshOAuthToken(); err != nil {
		return nil, fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	// conduit-31jg.12: shared with GenerateResponseStreaming.
	body, modelToUse := a.buildMessagesRequest(req)
	payload, err := marshalMessagesRequest(body)
	if err != nil {
		return nil, err
	}
	// conduit-31jg.46: bounded, jittered retry for 429/529/5xx honouring
	// retry-after and the ctx deadline.
	resp, err := a.postMessagesWithRetry(ctx, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var anthropicResp models.MessagesResponse
	if err := json.NewDecoder(httpsafe.LimitReader(resp.Body, providerResponseBodyLimit)).Decode(&anthropicResp); err != nil { // conduit-31jg.70
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	// Parity check: requested vs served model (what Anthropic actually
	// used). conduit-31jg.16: LOG ONLY. The old check split on "-2025" (so
	// 2024/2026 snapshots behind an alias looked like a mismatch) and
	// replaced the real reply — content and tool calls — with a warning
	// string returned as a success.
	respModel := anthropicResp.Model
	if modelToUse != "" && respModel != "" && !anthropicModelsMatch(modelToUse, respModel) {
		log.Printf("[Anthropic] WARNING: model mismatch: requested %q, served %q — keeping the response (conduit-31jg.16)", modelToUse, respModel)
	}

	// Extract content and tool calls from Anthropic response format
	content, toolCalls := a.parseAnthropicContent(&anthropicResp)
	usage := a.parseAnthropicUsage(anthropicResp.Usage)

	// Log cache statistics for debugging and monitoring
	if usage.CacheCreationInputTokens > 0 || usage.CacheReadInputTokens > 0 {
		totalInput := usage.PromptTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
		var hitRate float64
		if totalInput > 0 {
			hitRate = float64(usage.CacheReadInputTokens) / float64(totalInput) * 100
		}
		log.Printf("[Anthropic] Cache stats - Write: %d tokens, Read: %d tokens (%.1f%% hit rate)",
			usage.CacheCreationInputTokens,
			usage.CacheReadInputTokens,
			hitRate)
	}

	result := &GenerateResponse{
		Content:   content,
		ToolCalls: toolCalls,
		Usage:     usage,
	}
	// conduit-31jg.11: map stop_reason so the bd-1k3o length guard, refusal
	// handling and truncated-tool_use dropping work for Anthropic too.
	applyAnthropicStopReason(result, anthropicResp.StopReason, lastContentBlockType(&anthropicResp) == models.BlockToolUse)
	return result, nil
}

// refreshOAuthToken refreshes the OAuth token if needed.
func (a *AnthropicProvider) refreshOAuthToken() error {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()

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
