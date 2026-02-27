package ai

import (
	"sync"
	"testing"
)

func TestUsageTracker_RecordAndGet(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 250)

	pr, ok := tracker.GetProviderUsage("anthropic")
	if !ok {
		t.Fatal("Expected provider usage to exist")
	}
	if pr.TotalRequests != 1 {
		t.Errorf("Expected 1 request, got %d", pr.TotalRequests)
	}
	if pr.TotalInputTokens != 1000 {
		t.Errorf("Expected 1000 input tokens, got %d", pr.TotalInputTokens)
	}
	if pr.TotalOutputTokens != 500 {
		t.Errorf("Expected 500 output tokens, got %d", pr.TotalOutputTokens)
	}

	mr, ok := tracker.GetModelUsage("claude-sonnet-4")
	if !ok {
		t.Fatal("Expected model usage to exist")
	}
	if mr.TotalRequests != 1 {
		t.Errorf("Expected 1 request, got %d", mr.TotalRequests)
	}
	if mr.AvgLatencyMs != 250.0 {
		t.Errorf("Expected avg latency 250.0, got %f", mr.AvgLatencyMs)
	}
}

func TestUsageTracker_MultipleRecords(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 200)
	tracker.RecordUsage("anthropic", "claude-sonnet-4", 2000, 1000, 300)

	pr, _ := tracker.GetProviderUsage("anthropic")
	if pr.TotalRequests != 2 {
		t.Errorf("Expected 2 requests, got %d", pr.TotalRequests)
	}
	if pr.TotalInputTokens != 3000 {
		t.Errorf("Expected 3000 input tokens, got %d", pr.TotalInputTokens)
	}

	mr, _ := tracker.GetModelUsage("claude-sonnet-4")
	if mr.AvgLatencyMs != 250.0 {
		t.Errorf("Expected avg latency 250.0, got %f", mr.AvgLatencyMs)
	}
}

func TestUsageTracker_RecordError(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 200)
	tracker.RecordError("anthropic", "claude-sonnet-4")

	pr, _ := tracker.GetProviderUsage("anthropic")
	if pr.TotalRequests != 2 {
		t.Errorf("Expected 2 total requests, got %d", pr.TotalRequests)
	}
	if pr.ErrorCount != 1 {
		t.Errorf("Expected 1 error, got %d", pr.ErrorCount)
	}
}

func TestUsageTracker_Snapshot(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 200)
	tracker.RecordUsage("openai", "gpt-4o", 500, 250, 150)

	snapshot := tracker.GetSnapshot()

	if len(snapshot.Providers) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(snapshot.Providers))
	}
	if len(snapshot.Models) != 2 {
		t.Errorf("Expected 2 models, got %d", len(snapshot.Models))
	}
}

func TestUsageTracker_TotalCost(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1_000_000, 500_000, 200)

	cost := tracker.TotalCost()
	expected := 3.0 + 7.5 // 1M * $3/MTok + 500K * $15/MTok
	if cost != expected {
		t.Errorf("Expected total cost %f, got %f", expected, cost)
	}
}

func TestUsageTracker_Reset(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.RecordUsage("anthropic", "claude-sonnet-4", 1000, 500, 200)
	tracker.Reset()

	_, ok := tracker.GetProviderUsage("anthropic")
	if ok {
		t.Error("Expected no provider usage after reset")
	}

	if tracker.TotalCost() != 0.0 {
		t.Error("Expected zero cost after reset")
	}
}

func TestUsageTracker_ConcurrentAccess(t *testing.T) {
	tracker := NewUsageTracker()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.RecordUsage("anthropic", "claude-sonnet-4", 100, 50, 10)
		}()
	}
	wg.Wait()

	pr, _ := tracker.GetProviderUsage("anthropic")
	if pr.TotalRequests != 100 {
		t.Errorf("Expected 100 requests after concurrent access, got %d", pr.TotalRequests)
	}
}

func TestUsageTracker_NonExistentProvider(t *testing.T) {
	tracker := NewUsageTracker()

	_, ok := tracker.GetProviderUsage("nonexistent")
	if ok {
		t.Error("Expected no usage for non-existent provider")
	}
}
