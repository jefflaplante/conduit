package main

import (
	"testing"

	"conduit/internal/config"
)

// conduit-31jg.53: CLI Brains must carry the configured recency weight so
// brain migration 9 subtracts the same weight the gateway would.
func TestBrainCLIOptions_CarriesRecencyWeight(t *testing.T) {
	if got := brainCLIOptions(nil); len(got) != 0 {
		t.Fatalf("nil cfg: got %d options, want 0", len(got))
	}
	cfg := &config.Config{}
	if got := brainCLIOptions(cfg); len(got) != 0 {
		t.Fatalf("unset recency weight: got %d options, want 0", len(got))
	}
	cfg.Brain.RecencyWeight = 0.25
	if got := brainCLIOptions(cfg); len(got) != 1 {
		t.Fatalf("recency weight set: got %d options, want 1", len(got))
	}
}
