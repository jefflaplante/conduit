package ai

import (
	"math"
	"testing"
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
