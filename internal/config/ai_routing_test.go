package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// no-anthropic-routing: ai.providers[].routable and ai.vision.

// routingConfig mirrors the live shape: z-ai default (openai-compatible),
// anthropic kept in config, a claude-code provider.
func routingConfig() *Config {
	cfg := minimalValidConfig()
	cfg.AI = AIConfig{
		DefaultProvider: "z-ai",
		Providers: []ProviderConfig{
			{Name: "z-ai", Type: "openai", BaseURL: "https://api.z.ai/api/coding/paas/v4", Model: "glm-5.3-flash"},
			{Name: "anthropic", Type: "anthropic", Model: "claude-sonnet-4-6"},
			{Name: "cc", Type: "claude-code", Model: "sonnet"},
		},
	}
	return cfg
}

func TestProviderConfig_IsRoutable(t *testing.T) {
	if !(ProviderConfig{}).IsRoutable() {
		t.Error("omitted routable must default to true")
	}
	if !(ProviderConfig{Routable: boolPtr(true)}).IsRoutable() {
		t.Error("routable=true must be routable")
	}
	if (ProviderConfig{Routable: boolPtr(false)}).IsRoutable() {
		t.Error("routable=false must not be routable")
	}
}

func TestInferProviderType(t *testing.T) {
	for model, want := range map[string]string{
		"claude-sonnet-4-6": "anthropic",
		"Claude-Opus-4-6":   "anthropic",
		"gpt-5":             "openai",
		"llama3":            "ollama",
		"glm-5.3-flash":     "",
		"":                  "",
	} {
		if got := InferProviderType(model); got != want {
			t.Errorf("InferProviderType(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestValidateAIRouting_Valid(t *testing.T) {
	cfg := routingConfig()
	cfg.AI.Providers[1].Routable = boolPtr(false)
	cfg.AI.Vision = &VisionConfig{Provider: "z-ai", Model: "glm-5.3-flash"}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Fatalf("valid routing config rejected: %v", err)
	}
	// Model optional; a matching provider prefix is fine.
	cfg.AI.Vision = &VisionConfig{Provider: "z-ai"}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Fatalf("vision without model rejected: %v", err)
	}
	cfg.AI.Vision = &VisionConfig{Provider: "z-ai", Model: "z-ai/glm-5.3-flash"}
	if err := cfg.ValidateSemantic(); err != nil {
		t.Fatalf("vision with own provider prefix rejected: %v", err)
	}
}

func TestValidateAIRouting_Errors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"non-routable default", func(c *Config) { c.AI.Providers[0].Routable = boolPtr(false) },
			`ai.default_provider "z-ai" is configured with routable=false`},
		{"vision unknown provider", func(c *Config) { c.AI.Vision = &VisionConfig{Provider: "nope"} },
			`ai.vision.provider "nope" is not a configured provider (ai.providers: anthropic, cc, z-ai)`},
		{"vision claude-code", func(c *Config) { c.AI.Vision = &VisionConfig{Provider: "cc"} },
			`ai.vision.provider "cc" has type claude-code`},
		{"vision non-routable", func(c *Config) {
			c.AI.Providers[1].Routable = boolPtr(false)
			c.AI.Vision = &VisionConfig{Provider: "anthropic"}
		}, `ai.vision.provider "anthropic" is configured with routable=false`},
		{"vision empty provider", func(c *Config) { c.AI.Vision = &VisionConfig{Model: "glm-5.3-flash"} },
			"ai.vision.provider is required"},
		{"vision model names another provider", func(c *Config) {
			c.AI.Vision = &VisionConfig{Provider: "z-ai", Model: "anthropic/claude-sonnet-4-6"}
		}, `names provider "anthropic" but ai.vision.provider is "z-ai"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := routingConfig()
			tc.mutate(cfg)
			assertSemanticError(t, cfg, tc.want)
		})
	}
}

func TestRoutingWarnings_AliasesToNonRoutable(t *testing.T) {
	cfg := routingConfig()
	// Routable anthropic: the built-in claude-* aliases are fine.
	if w := cfg.AI.RoutingWarnings(); len(w) != 0 {
		t.Fatalf("unexpected warnings with routable anthropic: %v", w)
	}

	cfg.AI.Providers[1].Routable = boolPtr(false)
	cfg.AI.SubagentDefaultModel = "sonnet"
	cfg.AI.Providers[0].FallbackModel = "anthropic/claude-sonnet-4-6"
	w := cfg.AI.RoutingWarnings()
	joined := strings.Join(w, "\n")
	// Built-in aliases (none configured) resolve to anthropic: haiku,
	// sonnet, opus, default.
	for _, alias := range []string{"haiku", "sonnet", "opus", "default"} {
		if !strings.Contains(joined, `ai.model_aliases["`+alias+`"]`) {
			t.Errorf("no warning for alias %q:\n%s", alias, joined)
		}
	}
	if !strings.Contains(joined, `ai.subagent_default_model "sonnet" resolves to provider "anthropic"`) {
		t.Errorf("no sub-agent default warning:\n%s", joined)
	}
	if !strings.Contains(joined, `ai.providers["z-ai"].fallback_model "anthropic/claude-sonnet-4-6"`) {
		t.Errorf("no fallback_model warning:\n%s", joined)
	}
	// Warnings are not validation errors: the config still validates.
	if err := cfg.ValidateSemantic(); err != nil {
		t.Errorf("warnings must not fail validation: %v", err)
	}

	// Aliases pointed at z-ai: no alias warnings.
	cfg.AI.ModelAliases = map[string]string{"sonnet": "z-ai/glm-5.3", "haiku": "glm-5.3-flash"}
	cfg.AI.SubagentDefaultModel = ""
	cfg.AI.Providers[0].FallbackModel = ""
	if w := cfg.AI.RoutingWarnings(); len(w) != 0 {
		t.Errorf("unexpected warnings after re-pointing aliases: %v", w)
	}
}

func TestRoutingWarnings_RoutableSameTypeProviderWins(t *testing.T) {
	cfg := routingConfig()
	cfg.AI.Providers[1].Routable = boolPtr(false)
	cfg.AI.Providers = append(cfg.AI.Providers, ProviderConfig{Name: "anthropic-work", Type: "anthropic", Model: "claude-sonnet-4-6"})
	if w := cfg.AI.RoutingWarnings(); len(w) != 0 {
		t.Errorf("a routable anthropic-type provider serves claude-* — no warnings expected, got %v", w)
	}
}

func TestParse_RoutableAndVisionKeys(t *testing.T) {
	doc := `{
  "port": 18789,
  "ai": {
    "default_provider": "z-ai",
    "providers": [
      {"name": "z-ai", "type": "openai", "base_url": "https://api.z.ai/api/coding/paas/v4", "model": "glm-5.3-flash"},
      {"name": "anthropic", "type": "anthropic", "model": "claude-sonnet-4-6", "routable": false}
    ],
    "vision": {"provider": "z-ai", "model": "glm-5.3-flash"}
  },
  "tools": {"enabled_tools": ["Read"], "max_tool_chains": 25}
}`
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.AI.Providers[0].Routable != nil || !cfg.AI.Providers[0].IsRoutable() {
		t.Error("z-ai: omitted routable must stay nil and routable")
	}
	if cfg.AI.Providers[1].IsRoutable() {
		t.Error("anthropic: routable=false not parsed")
	}
	if cfg.AI.Vision == nil || cfg.AI.Vision.Provider != "z-ai" || cfg.AI.Vision.Model != "glm-5.3-flash" {
		t.Errorf("vision = %+v", cfg.AI.Vision)
	}

	// Round trip: routable only where set, vision only when set.
	b, err := json.Marshal(cfg.AI)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Count(s, `"routable"`) != 1 || !strings.Contains(s, `"routable":false`) {
		t.Errorf("routable round trip: %s", s)
	}
	if !strings.Contains(s, `"vision":{"provider":"z-ai","model":"glm-5.3-flash"}`) {
		t.Errorf("vision round trip: %s", s)
	}
	b, _ = json.Marshal(AIConfig{})
	if strings.Contains(string(b), "vision") {
		t.Errorf("unset vision must be omitted: %s", b)
	}

	// Non-routable default is fatal at Parse.
	bad := strings.Replace(doc, `"default_provider": "z-ai"`, `"default_provider": "anthropic"`, 1)
	if _, err := Parse([]byte(bad)); err == nil || !strings.Contains(err.Error(), "routable=false") {
		t.Errorf("non-routable default_provider: err = %v", err)
	}
}
