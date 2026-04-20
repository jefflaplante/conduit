package ai

import (
	"sync/atomic"
	"testing"
)

// stubObserver counts fan-out calls for tests.
type stubObserver struct {
	usage    atomic.Int64
	errors   atomic.Int64
	inTokens atomic.Int64
}

func (s *stubObserver) OnUsage(provider, model string, inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int, latencyMs int64) {
	s.usage.Add(1)
	s.inTokens.Add(int64(inputTokens))
}

func (s *stubObserver) OnError(provider, model string) {
	s.errors.Add(1)
}

func TestUsageTracker_ObserverFanout_OnUsage(t *testing.T) {
	tr := NewUsageTracker()
	obs := &stubObserver{}
	tr.SetObserver(obs)

	tr.RecordUsage("anthropic", "claude-sonnet", 100, 50, 0, 0, 250)
	tr.RecordUsage("anthropic", "claude-sonnet", 200, 75, 0, 0, 300)

	if obs.usage.Load() != 2 {
		t.Errorf("OnUsage fanout: want 2, got %d", obs.usage.Load())
	}
	if obs.inTokens.Load() != 300 {
		t.Errorf("observer input tokens: want 300, got %d", obs.inTokens.Load())
	}
}

func TestUsageTracker_ObserverFanout_OnError(t *testing.T) {
	tr := NewUsageTracker()
	obs := &stubObserver{}
	tr.SetObserver(obs)

	tr.RecordError("anthropic", "claude-sonnet")
	tr.RecordError("anthropic", "claude-haiku")

	if obs.errors.Load() != 2 {
		t.Errorf("OnError fanout: want 2, got %d", obs.errors.Load())
	}
}

func TestUsageTracker_ObserverNilIsSafe(t *testing.T) {
	tr := NewUsageTracker()
	// No SetObserver call -> nil observer.
	tr.RecordUsage("anthropic", "claude-sonnet", 10, 5, 0, 0, 100)
	tr.RecordError("anthropic", "claude-sonnet")
	// Should not panic and tracker state should still update.
	snap := tr.GetSnapshot()
	if snap.Providers["anthropic"] == nil {
		t.Error("provider stats missing")
	}
}

func TestUsageTracker_SetObserver_Replace(t *testing.T) {
	tr := NewUsageTracker()
	first := &stubObserver{}
	second := &stubObserver{}
	tr.SetObserver(first)
	tr.RecordUsage("anthropic", "claude", 10, 5, 0, 0, 100)
	tr.SetObserver(second)
	tr.RecordUsage("anthropic", "claude", 10, 5, 0, 0, 100)
	if first.usage.Load() != 1 {
		t.Errorf("first observer: want 1 call, got %d", first.usage.Load())
	}
	if second.usage.Load() != 1 {
		t.Errorf("second observer: want 1 call, got %d", second.usage.Load())
	}
}
