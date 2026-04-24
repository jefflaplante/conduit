package gateway

import (
	"context"
	"path/filepath"
	"testing"

	"conduit/internal/monitoring"
	"conduit/internal/sessions"
)

// TestSessionWake_OverflowIncrementsDropCounter verifies that when the
// sessionWake buffer is full (and no dedup slot exists for the target session),
// the wake signal is dropped and the GatewayMetrics.SessionWakeDrops counter is
// incremented exactly once per drop. This guards conduit-t38m: previously drops
// were silent log-only events.
func TestSessionWake_OverflowIncrementsDropCounter(t *testing.T) {
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sessions.NewStore: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	metrics := monitoring.NewGatewayMetrics()

	// Pre-fill a cap-1 wake channel with a DIFFERENT session key so the target
	// key is NOT already pending — the enqueue path will reserve the slot,
	// fail to send, back out, and count a drop.
	wakeCh := make(chan string, 1)
	wakeCh <- "other-session"

	gw := &Gateway{
		sessions:    store,
		sessionWake: wakeCh,
		monitoring:  &MonitoringService{GatewayMetrics: metrics},
	}

	if err := gw.SendToSessionWake(context.Background(), target.Key, "", "first"); err != nil {
		t.Fatalf("SendToSessionWake: %v", err)
	}
	if got := metrics.GetSessionWakeDrops(); got != 1 {
		t.Fatalf("expected SessionWakeDrops=1 after first overflow, got %d", got)
	}

	// Same-session second attempt while the channel is still full and nothing
	// is pending for target.Key: also a drop.
	if err := gw.SendToSessionWake(context.Background(), target.Key, "", "second"); err != nil {
		t.Fatalf("SendToSessionWake 2: %v", err)
	}
	if got := metrics.GetSessionWakeDrops(); got != 2 {
		t.Fatalf("expected SessionWakeDrops=2 after second overflow, got %d", got)
	}

	// Coalesce counter must stay zero: no wake was ever successfully reserved
	// for target.Key, so there's nothing to coalesce onto.
	if got := metrics.GetSessionWakeCoalesced(); got != 0 {
		t.Fatalf("expected SessionWakeCoalesced=0 (no reservation held), got %d", got)
	}
}

// TestSessionWake_DedupCoalescesRepeatedWakes verifies that when a wake for
// session X is already buffered, a second wake for X is coalesced (counted,
// not enqueued) instead of consuming a second slot in the 64-entry channel.
// This protects the wake buffer from being filled by a single chatty source.
func TestSessionWake_DedupCoalescesRepeatedWakes(t *testing.T) {
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sessions.NewStore: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	metrics := monitoring.NewGatewayMetrics()
	gw := &Gateway{
		sessions:    store,
		sessionWake: make(chan string, 8),
		monitoring:  &MonitoringService{GatewayMetrics: metrics},
	}

	// First wake: reserves the slot AND sends into the channel.
	if err := gw.SendToSessionWake(context.Background(), target.Key, "", "first"); err != nil {
		t.Fatalf("SendToSessionWake 1: %v", err)
	}
	if got := len(gw.sessionWake); got != 1 {
		t.Fatalf("expected 1 wake in channel after first send, got %d", got)
	}

	// Three back-to-back wakes for the same session while the first is still
	// buffered (drainer not running in this test): each should coalesce, not
	// enqueue, and not count as drops.
	for i := 0; i < 3; i++ {
		if err := gw.SendToSessionWake(context.Background(), target.Key, "", "repeat"); err != nil {
			t.Fatalf("SendToSessionWake repeat %d: %v", i, err)
		}
	}
	if got := len(gw.sessionWake); got != 1 {
		t.Fatalf("expected still 1 wake in channel after coalesced repeats, got %d", got)
	}
	if got := metrics.GetSessionWakeCoalesced(); got != 3 {
		t.Fatalf("expected SessionWakeCoalesced=3, got %d", got)
	}
	if got := metrics.GetSessionWakeDrops(); got != 0 {
		t.Fatalf("expected SessionWakeDrops=0 while dedup is active, got %d", got)
	}

	// After the drainer (simulated here by clearPendingWake + channel read)
	// consumes the wake, a new enqueue should succeed — not coalesce.
	key := <-gw.sessionWake
	if key != target.Key {
		t.Fatalf("unexpected dequeue: %q", key)
	}
	gw.clearPendingWake(target.Key)

	if err := gw.SendToSessionWake(context.Background(), target.Key, "", "after-drain"); err != nil {
		t.Fatalf("SendToSessionWake after drain: %v", err)
	}
	if got := len(gw.sessionWake); got != 1 {
		t.Fatalf("expected 1 wake in channel after drain+re-enqueue, got %d", got)
	}
	// Coalesce counter should NOT have moved on the post-drain enqueue.
	if got := metrics.GetSessionWakeCoalesced(); got != 3 {
		t.Fatalf("expected SessionWakeCoalesced still 3 after drain+re-enqueue, got %d", got)
	}
}

// TestSessionWake_MissingMonitoringDoesNotPanic guards the defensive nil
// checks in enqueueSessionWake. Early-construction tests (see dlq_test.go)
// assemble a Gateway without a MonitoringService; the wake path must not NPE
// in that configuration.
func TestSessionWake_MissingMonitoringDoesNotPanic(t *testing.T) {
	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sessions.NewStore: %v", err)
	}
	defer store.Close()

	target, err := store.GetOrCreateSession("user1", "ch1")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// Fill the channel so the drop branch fires. No monitoring wired.
	wakeCh := make(chan string, 1)
	wakeCh <- "other"
	gw := &Gateway{
		sessions:    store,
		sessionWake: wakeCh,
	}

	if err := gw.SendToSessionWake(context.Background(), target.Key, "", "msg"); err != nil {
		t.Fatalf("SendToSessionWake: %v", err)
	}
	// If we reach here without panic, the nil-safe path worked.
}
