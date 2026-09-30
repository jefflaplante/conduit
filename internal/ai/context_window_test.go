package ai

import (
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.17: ContextWindowForModel ranged over a map whose prefixes
// overlap (gpt-4/gpt-4o, llama3/llama3.1, deepseek-coder/deepseek-coder2), so
// "gpt-4o-2024-08-06" resolved to 8192 on some calls and 128000 on others —
// and an 8192 answer made trimRequestToFitContext drop nearly all history.

func TestContextWindowForModel_LongestPrefixIsStable(t *testing.T) {
	cases := map[string]int{
		"gpt-4o-2024-08-06":         128000, // not gpt-4 (8192)
		"gpt-4o-mini":               128000,
		"gpt-4-turbo-2024-04-09":    128000,
		"gpt-4-0613":                8192,
		"llama3.1:70b":              128000, // not llama3 (8192)
		"llama3.3-instruct":         128000,
		"llama3:8b":                 8192,
		"claude-sonnet-4-20250514":  200000,
		"claude-sonnet-4-6":         200000,
		"openrouter/gpt-4o-mini":    128000, // provider prefix stripped
		"totally-unknown-model-xyz": DefaultContextWindow,
		"":                          DefaultContextWindow,
	}
	for model, want := range cases {
		for i := 0; i < 1000; i++ {
			if got := ContextWindowForModel(model); got != want {
				t.Fatalf("call %d: ContextWindowForModel(%q) = %d, want %d", i, model, got, want)
			}
		}
	}
}

func TestLookupContextWindow_ReportsDefault(t *testing.T) {
	if _, known := LookupContextWindow("gpt-4o-2024-08-06"); !known {
		t.Error("gpt-4o-2024-08-06 should be a known model")
	}
	if w, known := LookupContextWindow("glm-4-unlisted"); known || w != DefaultContextWindow {
		t.Errorf("glm-4-unlisted: got (%d, %v), want (%d, false)", w, known, DefaultContextWindow)
	}
}

// conduit-31jg.82: GLM-5.3 (Z.ai) and DeepSeek V4.1 Flash (OpenRouter) are
// published as "1M" context. They must resolve as known models: gateway
// context usage, the prompt builder and compaction look the window up by
// model name and never see a provider's context_window override.
func TestLookupContextWindow_GLMAndDeepSeekV41Flash(t *testing.T) {
	cases := map[string]int{
		"glm-5.3":                            1000000,
		"glm-5.3-flash":                      1000000,
		"glm-5.3-flashx":                     1000000,
		"z-ai/glm-5.3":                       1000000, // OpenRouter ID
		"z-ai/glm-5.3:batch":                 1000000,
		"deepseek-v4.1-flash":                1000000,
		"deepseek/deepseek-v4.1-flash":       1000000, // OpenRouter ID
		"deepseek/deepseek-v4.1-flash:batch": 1000000,
		"deepseek-coder-v2":                  16384, // unaffected
	}
	for model, want := range cases {
		got, known := LookupContextWindow(model)
		if !known || got != want {
			t.Errorf("LookupContextWindow(%q) = (%d, %v), want (%d, true)", model, got, known, want)
		}
	}
}

func TestRouterDefaultModel(t *testing.T) {
	r, err := NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.DefaultModel(); got != "" {
		t.Errorf("no providers: DefaultModel() = %q, want empty", got)
	}
	r.default_ = "anthropic"
	r.providerMeta["zai"] = ProviderMeta{Name: "zai", DefaultModel: "glm-5.3"}
	r.providerMeta["anthropic"] = ProviderMeta{Name: "anthropic", DefaultModel: "claude-sonnet-4-6"}
	if got := r.DefaultModel(); got != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel() = %q, want claude-sonnet-4-6", got)
	}
}
