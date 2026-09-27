package heartbeat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// conduit-31jg.81: Close must not wait out an in-flight retry attempt
// (retryAttemptTimeout, 30s — longer than the shutdown stop wait). It cancels
// the attempt, returns promptly, and the attempt is audited as interrupted,
// not as a delivery failure.

// hangingSender fails the first send; later sends block until their ctx is
// done (a Telegram call stuck on the network).
type hangingSender struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
}

func (h *hangingSender) SendMessage(ctx context.Context, channelID, userID, content string, md map[string]string) error {
	h.mu.Lock()
	h.calls++
	n := h.calls
	h.mu.Unlock()
	if n == 1 {
		return errors.New("channel down")
	}
	close(h.entered)
	<-ctx.Done()
	return ctx.Err()
}

func (h *hangingSender) callCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func TestDelivery_CloseCancelsInFlightRetry(t *testing.T) {
	sender := &hangingSender{entered: make(chan struct{})}
	g, reg, auditor := newAuditedIntegration(t, sender, fastPolicy(3))

	if err := g.sendToTarget(context.Background(), "telegram:42", "disk 95%"); err == nil {
		t.Fatal("expected the first attempt to fail")
	}
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background retry never started")
	}

	begin := time.Now()
	done := make(chan struct{})
	go func() { _ = g.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on an in-flight retry attempt")
	}
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("Close took %v with an in-flight retry, want well under 1s", d)
	}

	rows, err := auditor.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 audit rows (first failure + interrupted retry), got %d: %+v", len(rows), rows)
	}
	var interrupted int
	for _, r := range rows {
		if strings.HasPrefix(r.ActionTaken, "interrupted:") {
			interrupted++
			if !strings.HasPrefix(r.ActionResult, "interrupted") {
				t.Errorf("interrupted attempt result = %q", r.ActionResult)
			}
		}
	}
	if interrupted != 1 {
		t.Fatalf("interrupted rows = %d, want 1: %+v", interrupted, rows)
	}
	// The interrupted attempt is not the target's fault: the breaker only
	// counts the genuine first failure.
	if _, failures, _ := reg.CircuitBreakerState("telegram:42"); failures != 1 {
		t.Fatalf("breaker failures = %d, want 1 (interrupt not counted)", failures)
	}
	if calls := sender.callCount(); calls != 2 {
		t.Fatalf("sender calls = %d, want 2 (no retry after Close)", calls)
	}
}

// ignoringSender fails the first send; later sends ignore ctx and block until
// released — a deliverer that does not honour cancellation. Close must still
// return within its bounded wait.
type ignoringSender struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (s *ignoringSender) SendMessage(ctx context.Context, channelID, userID, content string, md map[string]string) error {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		return errors.New("channel down")
	}
	close(s.entered)
	<-s.release
	return nil
}

func TestDelivery_CloseBoundedWhenDelivererIgnoresCtx(t *testing.T) {
	sender := &ignoringSender{entered: make(chan struct{}), release: make(chan struct{})}
	g, _, _ := newAuditedIntegration(t, sender, fastPolicy(3))
	t.Cleanup(func() { close(sender.release) })

	_ = g.sendToTarget(context.Background(), "telegram:42", "x")
	select {
	case <-sender.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background retry never started")
	}
	begin := time.Now()
	_ = g.Close()
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("Close took %v with a ctx-ignoring deliverer, want bounded", d)
	}
}
