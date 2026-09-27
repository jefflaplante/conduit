package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// conduit-31jg.57: ai.pricing_overrides parses (owner's shape) and the
// deprecated ai.smart_routing.pricing_overrides alias is merged into it.

func loadAIConfig(t *testing.T, aiJSON string) *Config {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Start from a valid default config and swap in the ai block.
	base, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(base, &doc); err != nil {
		t.Fatal(err)
	}
	doc["ai"] = json.RawMessage(aiJSON)
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestLoad_PricingOverridesOwnerShape(t *testing.T) {
	cfg := loadAIConfig(t, `{
	  "default_provider": "z-ai",
	  "pricing_overrides": {
	    "glm-5.3": {"input_per_m_token": 1.4, "output_per_m_token": 4.4},
	    "openrouter/deepseek/deepseek-v4.1-flash": {"input_per_m_token": 0.30, "output_per_m_token": 1.20}
	  },
	  "smart_routing": {"enabled": true, "track_usage": true, "cost_budget_daily": 25.0}
	}`)
	ov := cfg.AI.PricingOverrides
	if len(ov) != 2 {
		t.Fatalf("pricing_overrides = %+v", ov)
	}
	if g := ov["glm-5.3"]; g.InputPerMToken != 1.4 || g.OutputPerMToken != 4.4 {
		t.Errorf("glm-5.3 = %+v", g)
	}
	if d := ov["openrouter/deepseek/deepseek-v4.1-flash"]; d.InputPerMToken != 0.30 || d.OutputPerMToken != 1.20 {
		t.Errorf("deepseek = %+v", d)
	}
}

func TestLoad_PricingOverridesDeprecatedAliasMerged(t *testing.T) {
	cfg := loadAIConfig(t, `{
	  "default_provider": "z-ai",
	  "pricing_overrides": {
	    "glm-5.3": {"input_per_m_token": 1.4, "output_per_m_token": 4.4}
	  },
	  "smart_routing": {"enabled": true, "pricing_overrides": {
	    "glm-5.3": {"input_per_m_token": 9, "output_per_m_token": 9},
	    "glm-4.5": {"input_per_m_token": 0.6, "output_per_m_token": 2.2}
	  }}
	}`)
	ov := cfg.AI.PricingOverrides
	if g := ov["glm-4.5"]; g.InputPerMToken != 0.6 || g.OutputPerMToken != 2.2 {
		t.Errorf("alias entry not merged: %+v", ov)
	}
	if g := ov["glm-5.3"]; g.InputPerMToken != 1.4 {
		t.Errorf("ai.pricing_overrides must win over the alias: %+v", g)
	}
	if cfg.AI.SmartRouting.PricingOverrides != nil {
		t.Error("alias should be folded away after merge")
	}
}

func TestEffectivePricingOverrides_AliasOnly(t *testing.T) {
	a := AIConfig{SmartRouting: &SmartRoutingConfig{PricingOverrides: map[string]PricingOverride{
		"x": {InputPerMToken: 1, OutputPerMToken: 2},
	}}}
	if got := a.EffectivePricingOverrides(); got["x"].OutputPerMToken != 2 {
		t.Errorf("EffectivePricingOverrides = %+v", got)
	}
	if (AIConfig{}).EffectivePricingOverrides() != nil {
		t.Error("empty config should yield nil")
	}
}
