package monitoring

import (
	"sync"
	"testing"
	"time"
)

func TestTokenWindowTracker_RecordAndSnapshot(t *testing.T) {
	tr := NewTokenWindowTracker()
	tr.Record(100, 50, 10, 5)
	tr.Record(200, 75, 0, 0)

	snap := tr.Snapshot()
	if snap.Hour.Requests != 2 {
		t.Errorf("Hour.Requests: want 2, got %d", snap.Hour.Requests)
	}
	if snap.Hour.InputTokens != 300 {
		t.Errorf("Hour.InputTokens: want 300, got %d", snap.Hour.InputTokens)
	}
	if snap.Hour.OutputTokens != 125 {
		t.Errorf("Hour.OutputTokens: want 125, got %d", snap.Hour.OutputTokens)
	}
	if snap.Hour.CacheRead != 10 {
		t.Errorf("Hour.CacheRead: want 10, got %d", snap.Hour.CacheRead)
	}
	if snap.Hour.CacheWrite != 5 {
		t.Errorf("Hour.CacheWrite: want 5, got %d", snap.Hour.CacheWrite)
	}
	if snap.Day.Requests != 2 {
		t.Errorf("Day.Requests: want 2, got %d", snap.Day.Requests)
	}
	if snap.Day.InputTokens != 300 {
		t.Errorf("Day.InputTokens: want 300, got %d", snap.Day.InputTokens)
	}
	if snap.Hour.Total() != 300+125+10+5 {
		t.Errorf("Total(): want 440, got %d", snap.Hour.Total())
	}
}

func TestTokenWindowTracker_NegativeTokensClamped(t *testing.T) {
	tr := NewTokenWindowTracker()
	tr.Record(-5, -10, -2, -1)
	snap := tr.Snapshot()
	if snap.Hour.InputTokens != 0 || snap.Hour.OutputTokens != 0 {
		t.Errorf("negatives should clamp to 0; got %+v", snap.Hour)
	}
	if snap.Hour.Requests != 1 {
		t.Errorf("request count should still increment; got %d", snap.Hour.Requests)
	}
}

func TestTokenWindowTracker_RecordError(t *testing.T) {
	tr := NewTokenWindowTracker()
	tr.Record(100, 50, 0, 0)
	tr.RecordError()
	tr.RecordError()

	snap := tr.Snapshot()
	if snap.Hour.Errors != 2 {
		t.Errorf("Hour.Errors: want 2, got %d", snap.Hour.Errors)
	}
	if snap.Hour.Requests != 3 {
		t.Errorf("Hour.Requests: want 3, got %d", snap.Hour.Requests)
	}
	if snap.Day.Errors != 2 {
		t.Errorf("Day.Errors: want 2, got %d", snap.Day.Errors)
	}
}

func TestTokenWindowTracker_ObserverInterface(t *testing.T) {
	tr := NewTokenWindowTracker()

	// Exercise the ai.UsageObserver-shaped calls.
	tr.OnUsage("anthropic", "claude-sonnet", 1000, 500, 100, 50, 250)
	tr.OnError("anthropic", "claude-sonnet")

	snap := tr.Snapshot()
	if snap.Hour.InputTokens != 1000 || snap.Hour.OutputTokens != 500 {
		t.Errorf("OnUsage not routed: %+v", snap.Hour)
	}
	// OnUsage passes cacheWriteTokens then cacheReadTokens in the UsageTracker
	// ordering. Our Record takes cacheRead then cacheWrite — the observer
	// wrapper should reorder, so cacheRead=50 cacheWrite=100.
	if snap.Hour.CacheRead != 50 {
		t.Errorf("CacheRead: want 50 (observer reorder), got %d", snap.Hour.CacheRead)
	}
	if snap.Hour.CacheWrite != 100 {
		t.Errorf("CacheWrite: want 100 (observer reorder), got %d", snap.Hour.CacheWrite)
	}
	if snap.Hour.Errors != 1 {
		t.Errorf("OnError did not increment errors, got %d", snap.Hour.Errors)
	}
}

func TestTokenWindowTracker_HourRollover(t *testing.T) {
	tr := NewTokenWindowTracker()

	// Simulate "now" = base time.
	base := time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC)
	tr.setClock(func() time.Time { return base })
	tr.Record(100, 50, 0, 0)

	// Advance time by 2 hours (outside hour window and outside hour ring wrap
	// distance; minute buckets from 2h ago are stale).
	future := base.Add(2 * time.Hour)
	tr.setClock(func() time.Time { return future })

	snap := tr.Snapshot()
	if snap.Hour.Requests != 0 {
		t.Errorf("Hour.Requests after 2h: want 0, got %d", snap.Hour.Requests)
	}
	if snap.Hour.InputTokens != 0 {
		t.Errorf("Hour.InputTokens after 2h: want 0, got %d", snap.Hour.InputTokens)
	}
	// Day window should still include the old record.
	if snap.Day.Requests != 1 {
		t.Errorf("Day.Requests after 2h: want 1, got %d", snap.Day.Requests)
	}
	if snap.Day.InputTokens != 100 {
		t.Errorf("Day.InputTokens after 2h: want 100, got %d", snap.Day.InputTokens)
	}
}

func TestTokenWindowTracker_DayRollover(t *testing.T) {
	tr := NewTokenWindowTracker()

	base := time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC)
	tr.setClock(func() time.Time { return base })
	tr.Record(1000, 500, 0, 0)

	// Advance 25 hours — past the rolling day.
	future := base.Add(25 * time.Hour)
	tr.setClock(func() time.Time { return future })

	snap := tr.Snapshot()
	if snap.Day.Requests != 0 {
		t.Errorf("Day.Requests after 25h: want 0, got %d", snap.Day.Requests)
	}
	if snap.Day.InputTokens != 0 {
		t.Errorf("Day.InputTokens after 25h: want 0, got %d", snap.Day.InputTokens)
	}
}

func TestTokenWindowTracker_AccumulatesWithinBucket(t *testing.T) {
	tr := NewTokenWindowTracker()
	base := time.Date(2026, 4, 20, 10, 30, 15, 0, time.UTC)
	tr.setClock(func() time.Time { return base })

	for i := 0; i < 5; i++ {
		tr.Record(10, 5, 0, 0)
	}
	// Still inside the same minute.
	tr.setClock(func() time.Time { return base.Add(30 * time.Second) })
	for i := 0; i < 3; i++ {
		tr.Record(20, 10, 0, 0)
	}

	snap := tr.Snapshot()
	// 5 * (10+5) + 3 * (20+10) = 75 + 90 = 165 tokens across in+out.
	if snap.Hour.Requests != 8 {
		t.Errorf("Hour.Requests: want 8, got %d", snap.Hour.Requests)
	}
	if snap.Hour.InputTokens != 5*10+3*20 {
		t.Errorf("Hour.InputTokens: want 110, got %d", snap.Hour.InputTokens)
	}
	if snap.Hour.OutputTokens != 5*5+3*10 {
		t.Errorf("Hour.OutputTokens: want 55, got %d", snap.Hour.OutputTokens)
	}
}

func TestTokenWindowTracker_ConcurrentSafe(t *testing.T) {
	tr := NewTokenWindowTracker()
	var wg sync.WaitGroup
	const workers = 8
	const perWorker = 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				tr.Record(1, 1, 0, 0)
			}
		}()
	}
	wg.Wait()

	snap := tr.Snapshot()
	if want := int64(workers * perWorker); snap.Hour.Requests != want {
		t.Errorf("Hour.Requests: want %d, got %d", want, snap.Hour.Requests)
	}
}

func TestTokenWindowTracker_Reset(t *testing.T) {
	tr := NewTokenWindowTracker()
	tr.Record(100, 100, 0, 0)
	if tr.Snapshot().Hour.Requests == 0 {
		t.Fatal("expected a recorded request")
	}
	tr.Reset()
	snap := tr.Snapshot()
	if snap.Hour.Requests != 0 || snap.Day.Requests != 0 {
		t.Errorf("after reset: want zero counts, got %+v", snap)
	}
}

func TestTokenWindowSnapshot_Total(t *testing.T) {
	s := TokenWindowSnapshot{InputTokens: 100, OutputTokens: 50, CacheRead: 10, CacheWrite: 5}
	if s.Total() != 165 {
		t.Errorf("Total: want 165, got %d", s.Total())
	}
}
