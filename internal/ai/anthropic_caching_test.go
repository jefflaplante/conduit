package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"conduit/internal/config"
)

// conduit-3dru: prompt caching must be config-driven, not hardcoded.
//
// Previously addCacheBreakpoints ran unconditionally with zero config
// wiring: no disable switch, no granular flags, no TTL. These tests pin
// the contract the config schema claims to provide.

// newCachingTestServer returns an httptest server that captures the request
// body and replies with a minimal valid Anthropic messages response.
func newCachingTestServer(t *testing.T) (*httptest.Server, *[]byte) {
	t.Helper()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          "msg_test",
			"type":        "message",
			"role":        "assistant",
			"content":     []interface{}{map[string]interface{}{"type": "text", "text": "ok"}},
			"stop_reason": "end_turn",
			"model":       "claude-sonnet-4-6",
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 5,
			},
		})
	}))
	t.Cleanup(server.Close)
	return server, &received
}

// cachingTestRequest builds a GenerateRequest with a system prompt and
// history large enough to clear the smallest cache min-token threshold.
func cachingTestRequest(sys string, history int) *GenerateRequest {
	req := &GenerateRequest{
		Model:    "claude-sonnet-4-6",
		MaxTokens: 100,
	}
	for i := 0; i < history; i++ {
		req.Messages = append(req.Messages, ChatMessage{
			Role:    "user",
			Content: strings.Repeat("x ", 600), // ~300 tokens per message, clears sonnet's 2048 min
		})
	}
	req.Messages = append(req.Messages, ChatMessage{Role: "user", Content: "go"})
	if sys != "" {
		req.Messages = append([]ChatMessage{{Role: "system", Content: sys}}, req.Messages...)
	}
	return req
}

func providerCfgCaching(url string, cfg config.PromptCachingConfig) config.ProviderConfig {
	return config.ProviderConfig{
		Name:          "test",
		APIKey:        "sk-ant-api03-test",
		BaseURL:       url,
		PromptCaching: &cfg,
	}
}

func TestPromptCachingDisabledAddsNoBreakpoints(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		Enabled: false,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	// Large system prompt + history that would otherwise trigger breakpoints.
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest(strings.Repeat("s", 20000), 10)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(string(*raw), "cache_control") {
		t.Errorf("cache_control found in request with caching disabled:\n%s", *raw)
	}
}

func TestPromptCachingEnabledAppliesSystemBreakpoint(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		Enabled:      true,
		CacheTools:   true,
		CacheSystem:  true,
		CacheHistory: false,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest(strings.Repeat("s", 20000), 0)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body := capturedRequest(t, raw)
	blocks, ok := body["system"].([]interface{})
	if !ok {
		t.Fatalf("expected system as block array with cache_control, got %T", body["system"])
	}
	found := false
	for _, b := range blocks {
		bm := b.(map[string]interface{})
		if cc, ok := bm["cache_control"].(map[string]interface{}); ok {
			found = true
			if cc["type"] != "ephemeral" {
				t.Errorf("cache_control.type = %v, want ephemeral", cc["type"])
			}
		}
	}
	if !found {
		t.Errorf("no cache_control on any system block:\n%s", *raw)
	}
}

func TestPromptCachingGranularAllOff(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		Enabled:      true,
		CacheTools:   false,
		CacheSystem:  false,
		CacheHistory: false,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest(strings.Repeat("s", 20000), 10)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(string(*raw), "cache_control") {
		t.Errorf("cache_control found with all granular flags off:\n%s", *raw)
	}
}

func TestPromptCachingMasterSwitchGatesTTL(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		// Enabled=false with ExtendedTTL=true must still produce zero
		// breakpoints — the master switch gates everything.
		Enabled:     false,
		ExtendedTTL: true,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest(strings.Repeat("s", 20000), 10)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(string(*raw), "cache_control") {
		t.Errorf("cache_control found with master switch off despite ExtendedTTL:\n%s", *raw)
	}
}

func TestPromptCachingExtendedTTLMarker(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		Enabled:      true,
		CacheSystem:  true,
		CacheTools:   false,
		CacheHistory: false,
		ExtendedTTL:  true,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest(strings.Repeat("s", 20000), 0)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body := capturedRequest(t, raw)
	blocks, ok := body["system"].([]interface{})
	if !ok {
		t.Fatalf("expected system block array, got %T", body["system"])
	}
	for _, b := range blocks {
		bm := b.(map[string]interface{})
		cc, ok := bm["cache_control"].(map[string]interface{})
		if !ok {
			continue
		}
		if cc["ttl"] != "1h" {
			t.Errorf("cache_control.ttl = %v, want 1h when extended_ttl enabled", cc["ttl"])
		}
		return
	}
	t.Errorf("no cache_control marker found with ExtendedTTL:\n%s", *raw)
}

func TestPromptCachingHistoryBreakpointRespectsFlag(t *testing.T) {
	server, raw := newCachingTestServer(t)
	p, err := NewAnthropicProvider(providerCfgCaching(server.URL, config.PromptCachingConfig{
		Enabled:      true,
		CacheTools:   false,
		CacheSystem:  false,
		CacheHistory: true,
	}))
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	// 10 history messages + current = 11 total; interval default 6 → breakpoint expected
	if _, err := p.GenerateResponse(context.Background(), cachingTestRequest("", 10)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	body := capturedRequest(t, raw)
	msgs, ok := body["messages"].([]interface{})
	if !ok {
		t.Fatalf("messages missing: %T", body["messages"])
	}
	found := 0
	for _, m := range msgs {
		mm := m.(map[string]interface{})
		content, ok := mm["content"].([]interface{})
		if !ok {
			continue
		}
		for _, c := range content {
			cm := c.(map[string]interface{})
			if _, ok := cm["cache_control"]; ok {
				found++
			}
		}
	}
	if found == 0 {
		t.Errorf("no history breakpoint found with cache_history=true:\n%s", *raw)
	}
}

func TestPromptCachingConfigDefaults(t *testing.T) {
	cfg := config.DefaultPromptCachingConfig()
	if !cfg.Enabled || !cfg.CacheTools || !cfg.CacheSystem || !cfg.CacheHistory {
		t.Errorf("defaults should enable everything: %+v", cfg)
	}
	if cfg.HistoryBreakpointInterval <= 0 {
		t.Errorf("interval default should be positive: %+v", cfg)
	}
	if cfg.ExtendedTTL {
		// 5-minute TTL is the sensible default; 1h costs 2x write
		t.Errorf("ExtendedTTL should default off")
	}
}
