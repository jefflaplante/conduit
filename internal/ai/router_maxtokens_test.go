package ai

import (
	"testing"

	"conduit/internal/config"
)

// bd-1k3o: MaxTokens must be configurable via AIConfig — hard-coded 4000
// truncated sub-agent deliverable reports mid-sentence (2026-09-04 RCA).

func TestChainMaxTokens_DefaultsTo4000(t *testing.T) {
	r, err := NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	if got := r.chainMaxTokens(); got != 4000 {
		t.Errorf("default chainMaxTokens: want 4000, got %d", got)
	}
}

func TestChainMaxTokens_Configurable(t *testing.T) {
	r, err := NewRouter(config.AIConfig{MaxTokens: 16000}, nil)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	if got := r.chainMaxTokens(); got != 16000 {
		t.Errorf("configured chainMaxTokens: want 16000, got %d", got)
	}
}

func TestChainMaxTokens_ZeroInvalidIgnored(t *testing.T) {
	r, err := NewRouterWithExecution(config.AIConfig{MaxTokens: -1}, nil, nil)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	if got := r.chainMaxTokens(); got != 4000 {
		t.Errorf("negative chainMaxTokens should fall back to default: want 4000, got %d", got)
	}
}
