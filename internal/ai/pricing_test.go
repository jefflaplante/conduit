package ai

import (
	"math"
	"testing"

	"conduit/internal/config"
)

func TestPricingForModel_ExactMatch(t *testing.T) {
	pricing := PricingForModel("claude-opus-4")
	if pricing.InputPerMToken != 15.0 {
		t.Errorf("Expected InputPerMToken 15.0, got %f", pricing.InputPerMToken)
	}
	if pricing.OutputPerMToken != 75.0 {
		t.Errorf("Expected OutputPerMToken 75.0, got %f", pricing.OutputPerMToken)
	}
}

func TestPricingForModel_PrefixMatch(t *testing.T) {
	pricing := PricingForModel("claude-sonnet-4-20250514")
	if pricing.InputPerMToken != 3.0 {
		t.Errorf("Expected InputPerMToken 3.0 for sonnet, got %f", pricing.InputPerMToken)
	}
}

func TestPricingForModel_Unknown(t *testing.T) {
	pricing := PricingForModel("unknown-model")
	if pricing.InputPerMToken != 0.0 {
		t.Errorf("Expected zero pricing for unknown model, got %f", pricing.InputPerMToken)
	}
}

func TestPricingForModel_Empty(t *testing.T) {
	pricing := PricingForModel("")
	if pricing.InputPerMToken != 0.0 || pricing.OutputPerMToken != 0.0 {
		t.Error("Expected zero pricing for empty model")
	}
}

func TestCalculateCost(t *testing.T) {
	cost := CalculateCost("claude-sonnet-4", 1000, 500)
	// 1000 input tokens at $3/MTok = $0.003
	// 500 output tokens at $15/MTok = $0.0075
	expected := 0.003 + 0.0075

	if math.Abs(cost-expected) > 1e-12 {
		t.Errorf("Expected cost ~%f, got %f", expected, cost)
	}
}

func TestCalculateCost_ZeroTokens(t *testing.T) {
	cost := CalculateCost("claude-sonnet-4", 0, 0)
	if cost != 0.0 {
		t.Errorf("Expected 0 cost for zero tokens, got %f", cost)
	}
}

func TestPricingResolver_OverrideExactMatch(t *testing.T) {
	overrides := map[string]config.PricingOverride{
		"my-custom-model": {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	}
	pr := NewPricingResolver(overrides)

	pricing := pr.PricingForModel("my-custom-model")
	if pricing.InputPerMToken != 5.0 {
		t.Errorf("Expected InputPerMToken 5.0, got %f", pricing.InputPerMToken)
	}
	if pricing.OutputPerMToken != 25.0 {
		t.Errorf("Expected OutputPerMToken 25.0, got %f", pricing.OutputPerMToken)
	}
}

func TestPricingResolver_OverridePrefixMatch(t *testing.T) {
	overrides := map[string]config.PricingOverride{
		"custom-model": {InputPerMToken: 2.0, OutputPerMToken: 10.0},
	}
	pr := NewPricingResolver(overrides)

	// Should match prefix "custom-model" for "custom-model-20250101"
	pricing := pr.PricingForModel("custom-model-20250101")
	if pricing.InputPerMToken != 2.0 {
		t.Errorf("Expected InputPerMToken 2.0, got %f", pricing.InputPerMToken)
	}
	if pricing.OutputPerMToken != 10.0 {
		t.Errorf("Expected OutputPerMToken 10.0, got %f", pricing.OutputPerMToken)
	}
}

func TestPricingResolver_FallbackToDefault(t *testing.T) {
	overrides := map[string]config.PricingOverride{
		"my-custom-model": {InputPerMToken: 5.0, OutputPerMToken: 25.0},
	}
	pr := NewPricingResolver(overrides)

	// Should fall back to default matrix for known model
	pricing := pr.PricingForModel("claude-opus-4")
	if pricing.InputPerMToken != 15.0 {
		t.Errorf("Expected InputPerMToken 15.0 from default matrix, got %f", pricing.InputPerMToken)
	}
	if pricing.OutputPerMToken != 75.0 {
		t.Errorf("Expected OutputPerMToken 75.0 from default matrix, got %f", pricing.OutputPerMToken)
	}
}

func TestPricingResolver_NilOverrides(t *testing.T) {
	pr := NewPricingResolver(nil)

	// Should fall back to default matrix
	pricing := pr.PricingForModel("claude-sonnet-4")
	if pricing.InputPerMToken != 3.0 {
		t.Errorf("Expected InputPerMToken 3.0 from default matrix, got %f", pricing.InputPerMToken)
	}
	if pricing.OutputPerMToken != 15.0 {
		t.Errorf("Expected OutputPerMToken 15.0 from default matrix, got %f", pricing.OutputPerMToken)
	}

	// Empty model should return zero pricing
	pricing = pr.PricingForModel("")
	if pricing.InputPerMToken != 0.0 || pricing.OutputPerMToken != 0.0 {
		t.Error("Expected zero pricing for empty model with nil overrides")
	}
}

func TestPricingResolver_CalculateCost(t *testing.T) {
	overrides := map[string]config.PricingOverride{
		"custom-model": {InputPerMToken: 10.0, OutputPerMToken: 50.0},
	}
	pr := NewPricingResolver(overrides)

	cost := pr.CalculateCost("custom-model", 1000, 500)
	// 1000 input tokens at $10/MTok = $0.01
	// 500 output tokens at $50/MTok = $0.025
	expected := 0.01 + 0.025

	if math.Abs(cost-expected) > 1e-12 {
		t.Errorf("Expected cost ~%f, got %f", expected, cost)
	}
}
