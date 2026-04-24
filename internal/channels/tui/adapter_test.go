package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"conduit/internal/protocol"
)

func TestTUIAdapter_StopDoesNotPanic(t *testing.T) {
	adapter := NewAdapter("test-adapter", nil)

	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// First stop should succeed
	if err := adapter.Stop(); err != nil {
		t.Fatalf("First Stop failed: %v", err)
	}

	// Second stop should not panic (idempotent)
	if err := adapter.Stop(); err != nil {
		t.Fatalf("Second Stop failed: %v", err)
	}
}

func TestTUIAdapter_SendAfterStop(t *testing.T) {
	adapter := NewAdapter("test-adapter", nil)

	ctx := context.Background()
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := adapter.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Sending to a stopped adapter should not panic
	err := adapter.SendMessage(&protocol.OutgoingMessage{
		Text: "test message",
	})
	if err == nil {
		t.Error("expected error when sending to stopped adapter")
	}

	// SendIncomingMessage should also not panic
	err = adapter.SendIncomingMessage(&protocol.IncomingMessage{
		Text: "test incoming",
	})
	if err == nil {
		t.Error("expected error when sending incoming to stopped adapter")
	}
}

// TestTUIAdapter_ConcurrentSendAndStop hammers SendMessage /
// SendIncomingMessage against Stop to flush out the send-after-close race
// documented in conduit-3rhx. Before the fix, Stop() closed the outgoing /
// incoming channels while senders held only a stale stopped==false observation
// under an RLock that had already been released; the subsequent channel send
// in the select could hit the closed channel and panic.
//
// The test runs many iterations in parallel so at least one Stop lands during
// a concurrent Send. Any panic is recovered and reported as a test failure.
// Run with -race to also catch the ordering violation.
func TestTUIAdapter_ConcurrentSendAndStop(t *testing.T) {
	const iterations = 200
	const sendersPerIter = 8

	for i := 0; i < iterations; i++ {
		adapter := NewAdapter("race-adapter", nil)
		if err := adapter.Start(context.Background()); err != nil {
			t.Fatalf("Start failed on iter %d: %v", i, err)
		}

		var panicked atomic.Bool
		var wg sync.WaitGroup
		start := make(chan struct{})

		// Launch concurrent senders on both the outgoing and incoming paths.
		for s := 0; s < sendersPerIter; s++ {
			wg.Add(2)

			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panicked.Store(true)
						t.Errorf("SendMessage panicked: %v", r)
					}
				}()
				<-start
				for k := 0; k < 50; k++ {
					_ = adapter.SendMessage(&protocol.OutgoingMessage{Text: "ping"})
				}
			}()

			go func() {
				defer wg.Done()
				defer func() {
					if r := recover(); r != nil {
						panicked.Store(true)
						t.Errorf("SendIncomingMessage panicked: %v", r)
					}
				}()
				<-start
				for k := 0; k < 50; k++ {
					_ = adapter.SendIncomingMessage(&protocol.IncomingMessage{Text: "pong"})
				}
			}()
		}

		// Stopper runs in parallel with the senders.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicked.Store(true)
					t.Errorf("Stop panicked: %v", r)
				}
			}()
			<-start
			_ = adapter.Stop()
		}()

		close(start)
		wg.Wait()

		if panicked.Load() {
			t.Fatalf("iteration %d panicked; aborting", i)
		}

		// Drain any remaining outgoing (receiver goroutine may already be gone,
		// so drain non-blockingly) and double-Stop to ensure idempotency.
		if err := adapter.Stop(); err != nil {
			t.Fatalf("second Stop failed on iter %d: %v", i, err)
		}
	}
}
