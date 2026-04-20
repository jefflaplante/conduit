package ratelimit

import (
	"testing"
	"time"
)

func TestSnapshot_EmptyLimiter(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 10, 100*time.Millisecond)
	defer sw.Stop()

	snap := sw.Snapshot()
	if snap.Limit != 10 {
		t.Errorf("Limit: want 10, got %d", snap.Limit)
	}
	if snap.WindowDuration != time.Second {
		t.Errorf("WindowDuration: want 1s, got %v", snap.WindowDuration)
	}
	if len(snap.Identifiers) != 0 {
		t.Errorf("Identifiers: want empty, got %v", snap.Identifiers)
	}
	if snap.ActiveBuckets != 0 {
		t.Errorf("ActiveBuckets: want 0, got %d", snap.ActiveBuckets)
	}
}

func TestSnapshot_TracksMultipleIdentifiers(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 5, 100*time.Millisecond)
	defer sw.Stop()

	// Three different identifiers with varying load.
	sw.Allow("alice")
	sw.Allow("alice")
	sw.Allow("bob")
	for i := 0; i < 5; i++ {
		sw.Allow("carol") // maxed out
	}
	// One extra to trigger a rejection (should still be observed as bucket).
	sw.Allow("carol")

	snap := sw.Snapshot()
	if snap.ActiveBuckets != 3 {
		t.Errorf("ActiveBuckets: want 3, got %d", snap.ActiveBuckets)
	}
	byID := make(map[string]IdentifierSnapshot)
	for _, id := range snap.Identifiers {
		byID[id.Identifier] = id
	}

	alice, ok := byID["alice"]
	if !ok {
		t.Fatal("alice missing from snapshot")
	}
	if alice.Used != 2 || alice.Remaining != 3 {
		t.Errorf("alice: want used=2 remaining=3, got used=%d remaining=%d", alice.Used, alice.Remaining)
	}

	bob, ok := byID["bob"]
	if !ok {
		t.Fatal("bob missing from snapshot")
	}
	if bob.Used != 1 || bob.Remaining != 4 {
		t.Errorf("bob: want used=1 remaining=4, got used=%d remaining=%d", bob.Used, bob.Remaining)
	}

	carol, ok := byID["carol"]
	if !ok {
		t.Fatal("carol missing from snapshot")
	}
	if carol.Used != 5 || carol.Remaining != 0 {
		t.Errorf("carol: want used=5 remaining=0, got used=%d remaining=%d", carol.Used, carol.Remaining)
	}
	if carol.ResetAt.IsZero() {
		t.Error("carol.ResetAt should be set")
	}
}

func TestSnapshot_SkipsAllExpiredIdentifiers(t *testing.T) {
	sw := NewSlidingWindow(50*time.Millisecond, 5, 30*time.Millisecond)
	defer sw.Stop()

	sw.Allow("ephemeral")
	// Wait for window to slide fully.
	time.Sleep(80 * time.Millisecond)

	snap := sw.Snapshot()
	// Bucket may still exist (cleanup goroutine runs on 30ms interval), but
	// its timestamps are all expired so it should NOT appear in Identifiers.
	for _, id := range snap.Identifiers {
		if id.Identifier == "ephemeral" {
			t.Errorf("ephemeral bucket with expired timestamps leaked into snapshot: %+v", id)
		}
	}
}

func TestSnapshot_DoesNotMutate(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 3, 100*time.Millisecond)
	defer sw.Stop()

	sw.Allow("alice")
	sw.Allow("alice")

	// Many snapshots should NOT consume alice's remaining slots.
	for i := 0; i < 10; i++ {
		sw.Snapshot()
	}
	allowed, remaining, _, _ := sw.Allow("alice")
	if !allowed {
		t.Fatal("third request should still be allowed")
	}
	if remaining != 0 {
		t.Errorf("remaining after 3 allowed: want 0, got %d", remaining)
	}
}

func TestPeekIdentifier_UnknownReturnsFullLimit(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 7, 100*time.Millisecond)
	defer sw.Stop()

	used, remaining, resetAt, exists := sw.PeekIdentifier("never-seen")
	if exists {
		t.Error("exists should be false for an unseen identifier")
	}
	if used != 0 {
		t.Errorf("used: want 0, got %d", used)
	}
	if remaining != 7 {
		t.Errorf("remaining: want 7 (full limit), got %d", remaining)
	}
	if !resetAt.IsZero() {
		t.Errorf("resetAt: want zero, got %v", resetAt)
	}
}

func TestPeekIdentifier_Known(t *testing.T) {
	sw := NewSlidingWindow(time.Second, 5, 100*time.Millisecond)
	defer sw.Stop()

	sw.Allow("alice")
	sw.Allow("alice")

	used, remaining, resetAt, exists := sw.PeekIdentifier("alice")
	if !exists {
		t.Fatal("alice should exist")
	}
	if used != 2 || remaining != 3 {
		t.Errorf("want used=2 remaining=3, got used=%d remaining=%d", used, remaining)
	}
	if resetAt.IsZero() {
		t.Error("resetAt should be set")
	}
}

func TestLimitAndWindowGetters(t *testing.T) {
	sw := NewSlidingWindow(250*time.Millisecond, 42, 100*time.Millisecond)
	defer sw.Stop()
	if sw.Limit() != 42 {
		t.Errorf("Limit(): want 42, got %d", sw.Limit())
	}
	if sw.WindowDuration() != 250*time.Millisecond {
		t.Errorf("WindowDuration(): want 250ms, got %v", sw.WindowDuration())
	}
}
