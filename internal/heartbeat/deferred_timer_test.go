package heartbeat

import (
	"context"
	"sync"
	"testing"
	"time"

	"conduit/internal/scheduler"
)

// testClock is a goroutine-safe settable clock for the flush-timer tests
// (the timer callback reads it from another goroutine).
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// conduit-31jg.87: deferring an action arms a timer for the end of quiet
// hours, so delivery doesn't wait for the next heartbeat cycle.
func TestDeferredAction_TimerFlushesAtQuietEnd(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	clk := &testClock{t: time.Date(2026, 9, 25, 21, 0, 0, 0, la)}
	sender := &recordingSender{}
	g := newTestIntegration(t, t.TempDir(), sender, clk.t)
	g.now = clk.now
	t.Cleanup(func() { _ = g.Close() })

	job := &scheduler.Job{ID: "hb", Target: "telegram:42"}
	if err := g.executeActions(context.Background(), []HeartbeatAction{quietAwareAction("digest")}, job); err != nil {
		t.Fatal(err)
	}
	quietEnd := time.Date(2026, 9, 26, 6, 0, 0, 0, la)
	g.deferMu.Lock()
	armed, at := g.flushTimer != nil, g.flushTimerAt
	g.deferMu.Unlock()
	if !armed || !at.Equal(quietEnd) {
		t.Fatalf("flush timer armed=%v at=%v, want armed at %v", armed, at, quietEnd)
	}
	if len(sender.sent()) != 0 {
		t.Fatalf("delivered during quiet hours: %v", sender.sent())
	}

	// A later deadline keeps the earlier timer.
	g.armDeferredFlush(quietEnd.Add(time.Hour))
	g.deferMu.Lock()
	at = g.flushTimerAt
	g.deferMu.Unlock()
	if !at.Equal(quietEnd) {
		t.Fatalf("later deadline replaced timer: at=%v", at)
	}

	// Jump the clock past quiet end; re-arming at quiet end with the clock
	// already there fires (almost) at once and delivers.
	clk.set(quietEnd.Add(time.Minute))
	g.armDeferredFlush(quietEnd.Add(-time.Second))
	deadline := time.Now().Add(5 * time.Second)
	for len(sender.sent()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := sender.sent(); len(got) != 1 || got[0] != "telegram:42|💡 digest" {
		t.Fatalf("timer flush delivery = %v", got)
	}
}

// Close stops a pending flush timer.
func TestDeferredAction_CloseStopsFlushTimer(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	g := newTestIntegration(t, t.TempDir(), &recordingSender{}, time.Date(2026, 9, 25, 21, 0, 0, 0, la))
	g.armDeferredFlush(time.Date(2026, 9, 26, 6, 0, 0, 0, la))
	_ = g.Close()
	g.deferMu.Lock()
	armed := g.flushTimer != nil
	g.deferMu.Unlock()
	if armed {
		t.Fatal("flush timer still armed after Close")
	}
	// Arming after Close is a no-op.
	g.armDeferredFlush(time.Date(2026, 9, 26, 6, 0, 0, 0, la))
	g.deferMu.Lock()
	armed = g.flushTimer != nil
	g.deferMu.Unlock()
	if armed {
		t.Fatal("flush timer armed after Close")
	}
}
