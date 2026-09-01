package gateway

import (
	"testing"

	"conduit/internal/config"
)

// TestGetSubagentModel covers sub-agent model resolution:
// explicit model wins (alias-resolved), empty model uses the configured
// sub-agent default (alias-resolved) when set, otherwise the gateway default.
func TestGetSubagentModel(t *testing.T) {
	mkGateway := func(ai config.AIConfig) *Gateway {
		return &Gateway{config: &config.Config{AI: ai}}
	}

	testCfg := config.AIConfig{
		DefaultProvider: "z-ai",
		Providers: []config.ProviderConfig{
			{Name: "z-ai", Type: "openai", Model: "glm-5.3"},
		},
		ModelAliases: map[string]string{
			"glm-5.3":       "z-ai/glm-5.3",
			"glm-5.3-flash": "z-ai/glm-5.3-flash",
			"haiku":         "claude-haiku-4-5-20251001",
			"custom":        "", // alias to empty is ignored, falls through
		},
	}

	t.Run("empty model uses subagent default when set (alias resolved)", func(t *testing.T) {
		cfg := testCfg
		cfg.SubagentDefaultModel = "glm-5.3-flash"
		got := mkGateway(cfg).getSubagentModel("")
		want := "z-ai/glm-5.3-flash"
		if got != want {
			t.Errorf("getSubagentModel(\"\") = %q, want %q", got, want)
		}
	})

	t.Run("empty model falls back to gateway default when subagent default unset", func(t *testing.T) {
		cfg := testCfg // SubagentDefaultModel empty
		got := mkGateway(cfg).getSubagentModel("")
		want := "glm-5.3" // provider model, not alias-resolved (preserves old behavior)
		if got != want {
			t.Errorf("getSubagentModel(\"\") = %q, want %q", got, want)
		}
	})

	t.Run("explicit model resolves via aliases", func(t *testing.T) {
		cfg := testCfg
		cfg.SubagentDefaultModel = "glm-5.3-flash"
		got := mkGateway(cfg).getSubagentModel("haiku")
		want := "claude-haiku-4-5-20251001"
		if got != want {
			t.Errorf("getSubagentModel(%q) = %q, want %q", "haiku", got, want)
		}
	})

	t.Run("explicit model overrides subagent default", func(t *testing.T) {
		cfg := testCfg
		cfg.SubagentDefaultModel = "glm-5.3-flash"
		got := mkGateway(cfg).getSubagentModel("glm-5.3")
		want := "z-ai/glm-5.3"
		if got != want {
			t.Errorf("getSubagentModel(%q) = %q, want %q", "glm-5.3", got, want)
		}
	})

	t.Run("subagent default without alias entry passes through as-is", func(t *testing.T) {
		cfg := testCfg
		cfg.SubagentDefaultModel = "z-ai/glm-5.3-flash" // literal provider/model
		got := mkGateway(cfg).getSubagentModel("")
		want := "z-ai/glm-5.3-flash"
		if got != want {
			t.Errorf("getSubagentModel(\"\") = %q, want %q", got, want)
		}
	})

	t.Run("nil config falls back to gateway default safely", func(t *testing.T) {
		gw := &Gateway{} // no config at all
		got := gw.getSubagentModel("")
		want := "claude-sonnet-4-20250514" // getDefaultModel fallback
		if got != want {
			t.Errorf("getSubagentModel(\"\") = %q, want %q", got, want)
		}
	})
}
