package ai

import (
	"context"
	"errors"
	"testing"
	"time"

	"conduit/internal/config"
)

// Integration of the provider throttle (conduit-38cz) with the call log
// (conduit-2lzv): a throttled call is logged exactly once, with its slot
// wait in queue_wait_ms and not in latency_ms; an abandoned wait makes no
// provider call and leaves no record.

// waitForWaiters blocks until the pool (provider-wide when model == "") has
// n queued callers.
func waitForWaiters(t *testing.T, r *Router, provider, model string, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for slotStats(r, provider, model).Waiting != n {
		if time.Now().After(deadline) {
			t.Fatalf("pool never reached %d waiter(s): %+v", n, slotStats(r, provider, model))
		}
		time.Sleep(time.Millisecond)
	}
}

func TestThrottleCallLog_ThrottledCallLoggedOnceWithQueueWait(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 2)}
	r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
	read := attachCallLog(t, r)

	holderDone := make(chan error, 1)
	go func() {
		_, err := r.GenerateSideCall(context.Background(), "p", &GenerateRequest{Model: "m1"})
		holderDone <- err
	}()
	<-gp.entered // the holder has the only slot

	waiterDone := make(chan error, 1)
	go func() {
		_, err := r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(context.Background(), &GenerateRequest{Model: "m1"})
		waiterDone <- err
	}()
	waitForWaiters(t, r, "p", "", 1)
	const queued = 80 * time.Millisecond
	time.Sleep(queued) // the waiter's queue time

	gp.gate <- struct{}{} // holder finishes, waiter takes the slot
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	<-gp.entered
	gp.gate <- struct{}{} // waiter's provider call returns at once
	if err := <-waiterDone; err != nil {
		t.Fatal(err)
	}

	_, recs := read()
	assertPhases(t, recs, "side-call#1", "tool-loop#1")
	holder, waiter := recs[0], recs[1]
	if holder.QueueWaitMs != 0 {
		t.Errorf("holder never queued, queue_wait_ms = %d", holder.QueueWaitMs)
	}
	if waiter.QueueWaitMs < queued.Milliseconds() {
		t.Errorf("waiter queue_wait_ms = %d, want >= %d", waiter.QueueWaitMs, queued.Milliseconds())
	}
	if waiter.LatencyMs >= waiter.QueueWaitMs {
		t.Errorf("latency_ms = %d must exclude the %dms queue wait", waiter.LatencyMs, waiter.QueueWaitMs)
	}
	if waiter.ErrorClass != ErrClassNone {
		t.Errorf("waiter error_class = %q", waiter.ErrorClass)
	}
	obs.mu.Lock()
	if obs.calls != 2 || obs.errors != 0 {
		t.Errorf("metered calls=%d errors=%d, want 2/0 (each real call metered once)", obs.calls, obs.errors)
	}
	obs.mu.Unlock()
	if c := gp.calls.Load(); c != 2 {
		t.Errorf("provider calls = %d, want 2", c)
	}
}

func TestThrottleCallLog_AbandonedWaitNotLogged(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
	read := attachCallLog(t, r)

	holderDone := make(chan error, 1)
	go func() {
		_, err := r.GenerateSideCall(context.Background(), "p", &GenerateRequest{Model: "m1"})
		holderDone <- err
	}()
	<-gp.entered

	// All three choke points give up waiting.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, gErr := r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(ctx, &GenerateRequest{Model: "m1"})
	_, sErr := r.GenerateSideCall(ctx, "p", &GenerateRequest{Model: "m1"})
	req := &GenerateRequest{Model: "m1"}
	_, _, _, rErr := r.callWithRecovery(ctx, providerRoute{name: "p", provider: gp, model: "m1"}, req, recoveryOpts{phase: "test"})
	for name, err := range map[string]error{"guard": gErr, "side": sErr, "recovery": rErr} {
		var we *ThrottleWaitError
		if !errors.As(err, &we) {
			t.Errorf("%s: err = %v, want *ThrottleWaitError", name, err)
		}
	}

	gp.gate <- struct{}{}
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	_, recs := read()
	assertPhases(t, recs, "side-call#1") // only the holder's real call
	if c := gp.calls.Load(); c != 1 {
		t.Errorf("provider calls = %d, want 1 (abandoned waits never call)", c)
	}
	obs.mu.Lock()
	if obs.calls != 1 || obs.errors != 0 {
		t.Errorf("metered calls=%d errors=%d, want 1/0 (abandoned waits not metered)", obs.calls, obs.errors)
	}
	obs.mu.Unlock()
}
