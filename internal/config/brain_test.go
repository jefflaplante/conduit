package config

import (
	"encoding/json"
	"testing"
)

func TestBrainConfigApplyDefaults_MinimalConfig(t *testing.T) {
	// Regression test: minimal brain config with just enabled:true should pass validation.
	// Previously, omitempty JSON fields deserialized as zero values and failed validation.
	raw := `{"brain": {"enabled": true}}`
	var cfg struct {
		Brain BrainConfig `json:"brain"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if err := cfg.Brain.Validate(); err != nil {
		t.Fatalf("Validate() should succeed with minimal config, got: %v", err)
	}

	defaults := DefaultBrainConfig()
	if cfg.Brain.MaxLTMEntries != defaults.MaxLTMEntries {
		t.Errorf("MaxLTMEntries: got %d, want %d", cfg.Brain.MaxLTMEntries, defaults.MaxLTMEntries)
	}
	if cfg.Brain.AccessCountCap != defaults.AccessCountCap {
		t.Errorf("AccessCountCap: got %d, want %d", cfg.Brain.AccessCountCap, defaults.AccessCountCap)
	}
	if cfg.Brain.AccessWeight != defaults.AccessWeight {
		t.Errorf("AccessWeight: got %f, want %f", cfg.Brain.AccessWeight, defaults.AccessWeight)
	}
}

func TestBrainConfigApplyDefaults_PartialOverride(t *testing.T) {
	raw := `{"brain": {"enabled": true, "max_ltm_entries": 500}}`
	var cfg struct {
		Brain BrainConfig `json:"brain"`
	}
	json.Unmarshal([]byte(raw), &cfg)
	cfg.Brain.Validate()

	if cfg.Brain.MaxLTMEntries != 500 {
		t.Errorf("MaxLTMEntries should be user-specified 500, got %d", cfg.Brain.MaxLTMEntries)
	}
	defaults := DefaultBrainConfig()
	if cfg.Brain.AccessCountCap != defaults.AccessCountCap {
		t.Errorf("AccessCountCap should get default %d, got %d", defaults.AccessCountCap, cfg.Brain.AccessCountCap)
	}
}
