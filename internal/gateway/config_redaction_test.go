package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/core"
	"conduit/internal/tools/types"
)

// gatewayToolRegistry routes ExecuteTool to a real GatewayTool and opts it
// into Data rendering, as the real Registry does (conduit-31jg.39).
type gatewayToolRegistry struct{ tool *core.GatewayTool }

func (r gatewayToolRegistry) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*tools.ToolResult, error) {
	return r.tool.Execute(ctx, args)
}

func (r gatewayToolRegistry) IncludeDataInModelOutput(name string) bool { return true }

// conduit-31jg.56: Gateway(action="config") must never put provider keys,
// OAuth tokens or client secrets in front of the model or into the
// transcript. Checked at GetConfiguration, at the tool result, and at the
// exact text formatToolResultForAI sends to the provider.
func TestGatewayConfigTool_RedactsSecrets(t *testing.T) {
	sentinels := []string{
		"SENTINEL_API_KEY", "SENTINEL_OAUTH", "SENTINEL_REFRESH", "SENTINEL_CLIENT_SECRET",
		"SENTINEL_API_KEY_2", "SENTINEL_URL_PASS",
	}
	cfg := &config.Config{
		AI: config.AIConfig{
			DefaultProvider: "anthropic",
			Providers: []config.ProviderConfig{
				{
					Name: "anthropic", Type: "anthropic", Model: "claude-x",
					APIKey: "SENTINEL_API_KEY",
					Auth: &config.AuthConfig{
						Type: "oauth", OAuthToken: "SENTINEL_OAUTH", RefreshToken: "SENTINEL_REFRESH",
						ClientID: "public-client-id", ClientSecret: "SENTINEL_CLIENT_SECRET",
					},
				},
				{
					Name: "proxy", Type: "openai", Model: "gpt-x", APIKey: "SENTINEL_API_KEY_2",
					BaseURL: "https://user:SENTINEL_URL_PASS@proxy.example/v1",
				},
			},
		},
		Workspace: config.WorkspaceConfig{ContextDir: "/srv/workspace"},
	}
	gw := &Gateway{config: cfg}

	assertClean := func(stage, text string) {
		t.Helper()
		for _, s := range sentinels {
			if strings.Contains(text, s) {
				t.Errorf("%s leaks %s:\n%s", stage, s, text)
			}
		}
	}

	// 1. The service method.
	got, err := gw.GetConfiguration()
	if err != nil {
		t.Fatalf("GetConfiguration: %v", err)
	}
	raw, _ := json.Marshal(got)
	assertClean("GetConfiguration", string(raw))
	for _, want := range []string{"public-client-id", "claude-x", "/srv/workspace", config.RedactedValue} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("GetConfiguration dropped non-secret %q:\n%s", want, raw)
		}
	}
	// The live config is untouched.
	if cfg.AI.Providers[0].APIKey != "SENTINEL_API_KEY" || cfg.AI.Providers[0].Auth.OAuthToken != "SENTINEL_OAUTH" {
		t.Fatal("GetConfiguration mutated the live config")
	}

	// 2 + 3. The tool result, then the text the execution engine hands the
	// model for it (tool_result content on the follow-up request).
	tool := core.NewGatewayTool(&types.ToolServices{Gateway: gw})
	res, err := tool.Execute(context.Background(), map[string]interface{}{"action": "config"})
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Gateway config: res=%+v err=%v", res, err)
	}
	resJSON, _ := json.Marshal(res)
	assertClean("ToolResult", string(resJSON)+res.Content)

	engine := tools.NewExecutionEngine(gatewayToolRegistry{tool}, 1, 0, 1)
	provider := ai.NewMockProvider("mock")
	provider.AddResponse("done", nil)
	_, err = engine.HandleToolCallFlow(context.Background(), provider,
		&ai.GenerateRequest{Messages: []ai.ChatMessage{{Role: "user", Content: "show config"}}},
		&ai.GenerateResponse{ToolCalls: []ai.ToolCall{{ID: "c1", Name: "Gateway", Args: map[string]interface{}{"action": "config"}}}},
	)
	if err != nil {
		t.Fatalf("HandleToolCallFlow: %v", err)
	}
	calls := provider.GetCalls()
	if len(calls) == 0 {
		t.Fatal("provider never received the tool result")
	}
	var toolContent string
	for _, m := range calls[0].Request.Messages {
		if m.Role == "tool" && m.ToolCallID == "c1" {
			toolContent = m.Content
		}
	}
	if !strings.Contains(toolContent, "Structured data") {
		t.Fatalf("expected the Data payload to be rendered for the model, got:\n%s", toolContent)
	}
	assertClean("model-facing tool_result", toolContent)
}
