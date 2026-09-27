package core

import (
	"strings"
	"testing"
	"time"

	"conduit/internal/channels"
	"conduit/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.71: formatters must render the shapes internal/gateway
// (status_ops.go) actually returns.

func TestGatewayFormat_Status_RealShape(t *testing.T) {
	tool := NewGatewayTool(nil)
	// GetGatewayStatus returns exactly these keys.
	out := tool.formatGatewayStatus(map[string]interface{}{
		"status":  "running",
		"version": "v1.2.3 (abc1234)",
	})
	assert.Contains(t, out, "status: running")
	assert.Contains(t, out, "version: v1.2.3 (abc1234)")
}

func TestGatewayFormat_Channels_RealShape(t *testing.T) {
	tool := NewGatewayTool(nil)
	// GetChannelStatus returns map[id]channels.ChannelStatus (a struct).
	in := map[string]interface{}{
		"telegram": channels.ChannelStatus{
			Status:    channels.StatusOnline,
			Message:   "polling",
			Details:   map[string]interface{}{"message_count": int64(42), "bot": "conduit_bot"},
			Timestamp: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		},
		"tui": channels.ChannelStatus{Status: "offline", Details: map[string]interface{}{"message_count": 0}},
	}
	out := tool.formatChannelStatus(in)
	assert.Contains(t, out, "Channel Status (2)")
	assert.Contains(t, out, "- telegram: online (polling)")
	assert.Contains(t, out, "message_count: 42")
	assert.Contains(t, out, "bot: conduit_bot")
	assert.Contains(t, out, "- tui: offline")
	// Sorted, deterministic.
	assert.Less(t, strings.Index(out, "telegram"), strings.Index(out, "tui"))
}

func TestGatewayFormat_Metrics_RealShape(t *testing.T) {
	tool := NewGatewayTool(nil)
	out := tool.formatMetrics(map[string]interface{}{"uptime": "unknown"})
	assert.Contains(t, out, "uptime: unknown")
	assert.Contains(t, tool.formatMetrics(nil), "none reported")
}

func TestGatewayFormat_PromptDebug_RealShape(t *testing.T) {
	tool := NewGatewayTool(nil)
	out := tool.formatPromptDebug(map[string]interface{}{
		"prompt_text":        strings.Repeat("PROMPT", 1000),
		"total_chars":        6000,
		"estimated_tokens":   1500,
		"context_window":     200000,
		"budget_chars":       80000,
		"budget_constrained": false,
		"sections": []map[string]interface{}{
			{"name": "identity", "priority": 0, "chars": 1200, "included": true},
			{"name": "skills", "priority": 5, "chars": 4800, "included": false},
		},
		"dropped_sections": []string{"skills"},
	})
	assert.Contains(t, out, "Total chars:        6000")
	assert.Contains(t, out, "identity")
	assert.Contains(t, out, "DROPPED")
	assert.Contains(t, out, "Dropped sections: skills")
	assert.NotContains(t, out, "PROMPTPROMPT", "prompt text stays out of Content")
}

// The config action formats config.Redacted output: key fields present, no
// secret sentinel anywhere in Content.
func TestGatewayFormat_Config_RedactedNoSecrets(t *testing.T) {
	sentinels := []string{"SENTINEL_API_KEY", "SENTINEL_OAUTH", "SENTINEL_REFRESH", "SENTINEL_CLIENT_SECRET", "SENTINEL_URL_PASS"}
	aiCfg := config.AIConfig{
		DefaultProvider: "anthropic",
		ModelAliases:    map[string]string{"fast": "claude-haiku"},
		Providers: []config.ProviderConfig{
			{
				Name: "anthropic", Type: "anthropic", Model: "claude-x", APIKey: "SENTINEL_API_KEY",
				Auth: &config.AuthConfig{
					Type: "oauth", OAuthToken: "SENTINEL_OAUTH", RefreshToken: "SENTINEL_REFRESH",
					ClientID: "public-client-id", ClientSecret: "SENTINEL_CLIENT_SECRET",
				},
			},
			{
				Name: "proxy", Type: "openai", Model: "gpt-x", ContextWindow: 128000,
				BaseURL: "https://user:SENTINEL_URL_PASS@proxy.example/v1",
			},
		},
	}
	ai, err := config.Redacted(aiCfg)
	require.NoError(t, err)
	ws, err := config.Redacted(config.WorkspaceConfig{ContextDir: "/srv/workspace"})
	require.NoError(t, err)

	out := NewGatewayTool(nil).formatConfiguration(map[string]interface{}{"ai": ai, "workspace": ws})

	for _, s := range sentinels {
		assert.NotContains(t, out, s)
	}
	for _, want := range []string{
		"Default provider: anthropic",
		"Providers (2)",
		"- anthropic (anthropic) model=claude-x",
		"auth=oauth",
		"- proxy (openai) model=gpt-x context_window=128000",
		"fast=claude-haiku",
		"Context dir: /srv/workspace",
		"public-client-id", // non-secret detail via the redacted JSON
	} {
		assert.Contains(t, out, want)
	}
}

func TestGatewayTool_DataNotDuplicatedForModel(t *testing.T) {
	assert.False(t, NewGatewayTool(nil).IncludeDataInModelOutput())
}
