package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/sessions"
)

// conduit-31jg.35: a caller holding the turn via AcquireTurn can call the
// generate entry points (streaming fallback path included) without
// deadlocking, while other callers on the session stay serialized.
func TestAcquireTurn_LeaseBypassesLockOnlyForHolder(t *testing.T) {
	provider := &blockingProvider{name: "blocking", release: make(chan struct{})}
	close(provider.release) // never block
	router := newTurnLockRouter(provider)
	sess := &sessions.Session{Key: "s1"}

	ctx, release, err := router.AcquireTurn(context.Background(), sess.Key)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 2)
	go func() {
		_, err := router.GenerateResponseWithTools(ctx, sess, "hi", "blocking", "")
		done <- err
	}()
	go func() {
		// blockingProvider is not a StreamingProvider: exercises the
		// streaming → non-streaming fallback under the lease.
		_, err := router.GenerateResponseStreaming(ctx, sess, "hi", "blocking", "", nil)
		done <- err
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("generate under lease: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("DEADLOCK: generate under AcquireTurn lease blocked on the turn lock")
		}
	}

	// A caller without the lease must wait for release.
	other := make(chan struct{})
	go func() {
		_, _ = router.GenerateResponseWithTools(context.Background(), sess, "hi", "blocking", "")
		close(other)
	}()
	select {
	case <-other:
		t.Fatal("non-holder ran while the turn was held")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	release() // idempotent
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("non-holder never ran after release")
	}

	// After release the stale lease no longer bypasses the lock.
	ctx2, release2, err := router.AcquireTurn(context.Background(), sess.Key)
	if err != nil {
		t.Fatal(err)
	}
	stale := make(chan struct{})
	go func() {
		_, _ = router.GenerateResponseWithTools(ctx, sess, "hi", "blocking", "")
		close(stale)
	}()
	select {
	case <-stale:
		t.Fatal("released lease still bypassed the lock")
	case <-time.After(100 * time.Millisecond):
	}
	release2()
	<-stale
	_ = ctx2
}

// conduit-31jg.23: a turn waiting for the lock gives up when cancelled.
func TestAcquireTurn_CancelWhileQueued(t *testing.T) {
	router := newTurnLockRouter(&blockingProvider{name: "b"})
	_, release, err := router.AcquireTurn(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, rel, err := router.AcquireTurn(ctx, "s1")
		rel()
		got <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued AcquireTurn ignored cancellation")
	}

	// A lease for another session does not bypass this one.
	ctxB, relB, _ := router.AcquireTurn(context.Background(), "s2")
	defer relB()
	blocked := make(chan struct{})
	go func() {
		unlock := router.lockSessionCtx(ctxB, "s1")
		unlock()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("lease for s2 bypassed s1's lock")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	<-blocked
}

// conduit-31jg.22: with the stored row's ID the current user message is
// dropped from history even when its stored text differs from the text sent
// to the model, and even when it is not the last row.
func TestDropCurrentUserRow_ByID(t *testing.T) {
	hist := []sessions.Message{
		{ID: "1", Role: "user", Content: "earlier"},
		{ID: "2", Role: "assistant", Content: "ok"},
		{ID: "3", Role: "user", Content: "[Photo] look"},
		{ID: "4", Role: "assistant", Content: "later row"},
	}
	ctx := WithCurrentUserMessageID(context.Background(), "3")
	got := dropCurrentUserRow(ctx, hist, "look\n\n[System: reflect]")
	if len(got) != 3 || got[2].ID != "4" || got[1].ID != "2" {
		t.Fatalf("got %+v", got)
	}
	if len(hist) != 4 || hist[2].ID != "3" {
		t.Fatal("input slice was mutated")
	}

	// Without an ID the legacy trailing exact-text rule applies.
	legacy := dropCurrentUserRow(context.Background(), hist[:3], "[Photo] look")
	if len(legacy) != 2 {
		t.Fatalf("legacy drop: %+v", legacy)
	}
}
