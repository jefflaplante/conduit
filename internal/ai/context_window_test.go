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
	if w, known := LookupContextWindow("glm-5.3"); known || w != DefaultContextWindow {
		t.Errorf("glm-5.3: got (%d, %v), want (%d, false)", w, known, DefaultContextWindow)
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
