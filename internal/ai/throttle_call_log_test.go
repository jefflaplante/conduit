package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// Integration of the provider throttle (conduit-38cz) with the call log
// (conduit-2lzv): a throttled call is logged exactly once, with its slot
// wait in queue_wait_ms and not in latency_ms; an abandoned wait makes no
// provider call and is not metered, but leaves one queue_timeout /
// queue_cancel record (conduit-3j08).

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

// holdSlot starts a side call that takes the only slot of gp's pool and
// returns a func that lets it finish and waits for it.
func holdSlot(t *testing.T, r *Router, gp *gateProvider) func() {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := r.GenerateSideCall(context.Background(), "p", &GenerateRequest{Model: "m1"})
		done <- err
	}()
	<-gp.entered
	return func() {
		t.Helper()
		gp.gate <- struct{}{}
		if err := <-done; err != nil && !gp.failOn.Load() {
			t.Fatal(err)
		}
	}
}

// callAt runs one call on ctx through the named choke point.
func callAt(ctx context.Context, r *Router, gp *gateProvider, point string) error {
	var err error
	switch point {
	case "guard":
		_, err = r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(ctx, &GenerateRequest{Model: "m1"})
	case "side":
		_, err = r.GenerateSideCall(ctx, "p", &GenerateRequest{Model: "m1"})
	case "recovery":
		_, _, _, err = r.callWithRecovery(ctx, providerRoute{name: "p", provider: gp, model: "m1"}, &GenerateRequest{Model: "m1"}, recoveryOpts{phase: "test"})
	default:
		err = fmt.Errorf("unknown choke point %q", point)
	}
	return err
}

// assertQueueRecord checks a queue_* record: the routing fields a normal
// record has, the wait in queue_wait_ms, and nothing a provider call makes.
func assertQueueRecord(t *testing.T, rec CallRecord, class string, minWait time.Duration) {
	t.Helper()
	if rec.ErrorClass != class {
		t.Errorf("%s: error_class = %q, want %q", rec.Phase, rec.ErrorClass, class)
	}
	if rec.Provider != "p" || rec.Model != "m1" {
		t.Errorf("%s: route = %s/%s, want p/m1", rec.Phase, rec.Provider, rec.Model)
	}
	if rec.QueueWaitMs <= 0 || rec.QueueWaitMs < minWait.Milliseconds() {
		t.Errorf("%s: queue_wait_ms = %d, want > 0 and >= %d", rec.Phase, rec.QueueWaitMs, minWait.Milliseconds())
	}
	if rec.LatencyMs != 0 || rec.TTFTMs != nil {
		t.Errorf("%s: latency_ms=%d ttft=%v, want 0/nil (no provider call)", rec.Phase, rec.LatencyMs, rec.TTFTMs)
	}
	if rec.PromptTokens+rec.CompletionTokens+rec.TotalTokens != 0 || rec.CostUSD != 0 || rec.Priced != nil {
		t.Errorf("%s: tokens/cost set on a queue record: %+v", rec.Phase, rec)
	}
	if !strings.Contains(rec.Error, "gave up waiting") {
		t.Errorf("%s: error = %q", rec.Phase, rec.Error)
	}
}

// A wait given up on its deadline at each of the three choke points leaves
// one queue_timeout record; still no provider call and no metering.
func TestThrottleCallLog_AbandonedWaitDeadlineLogged(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
	read := attachCallLog(t, r)
	finish := holdSlot(t, r, gp)

	const wait = 40 * time.Millisecond
	for _, point := range []string{"guard", "side", "recovery"} {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		err := callAt(ctx, r, gp, point)
		cancel()
		var we *ThrottleWaitError
		if !errors.As(err, &we) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want *ThrottleWaitError(DeadlineExceeded)", point, err)
		}
	}
	finish()

	_, recs := read()
	assertPhases(t, recs, "tool-loop#1", "side-call#1", "depth0#1", "side-call#1")
	for _, rec := range recs[:3] {
		assertQueueRecord(t, rec, ErrClassQueueTimeout, wait/2)
	}
	if holder := recs[3]; holder.ErrorClass != ErrClassNone || holder.QueueWaitMs != 0 {
		t.Errorf("holder record = %+v", holder)
	}
	if c := gp.calls.Load(); c != 1 {
		t.Errorf("provider calls = %d, want 1 (abandoned waits never call)", c)
	}
	obs.mu.Lock()
	if obs.calls != 1 || obs.errors != 0 {
		t.Errorf("metered calls=%d errors=%d, want 1/0 (abandoned waits not metered)", obs.calls, obs.errors)
	}
	obs.mu.Unlock()
}

// A wait ended by ctx cancel (/stop, sub-agent cancel, shutdown) leaves a
// queue_cancel record carrying the turn's session fields.
func TestThrottleCallLog_AbandonedWaitCancelLogged(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
	read := attachCallLog(t, r)
	finish := holdSlot(t, r, gp)

	sess := &sessions.Session{Key: "subagent_42", UserID: "subagent", ChannelID: "subagent_42", Context: map[string]string{"label": "worker"}}
	const queued = 30 * time.Millisecond
	for _, point := range []string{"guard", "side", "recovery"} {
		ctx, cancel := context.WithCancel(r.withCallTurn(context.Background(), sess))
		errc := make(chan error, 1)
		go func() { errc <- callAt(ctx, r, gp, point) }()
		waitForWaiters(t, r, "p", "", 1)
		time.Sleep(queued)
		cancel()
		err := <-errc
		var we *ThrottleWaitError
		if !errors.As(err, &we) || !errors.Is(err, context.Canceled) {
			t.Errorf("%s: err = %v, want *ThrottleWaitError(Canceled)", point, err)
		}
	}
	finish()

	_, recs := read()
	assertPhases(t, recs, "depth1#1", "side-call#1", "depth0#1", "side-call#1")
	for _, rec := range recs[:3] {
		assertQueueRecord(t, rec, ErrClassQueueCancel, queued)
		if rec.SessionKey != "subagent_42" || rec.SessionLabel != "worker" || rec.AgentKind != "subagent" || rec.Channel != "subagent" || rec.TurnID == "" {
			t.Errorf("%s: session fields = %+v", rec.Phase, rec)
		}
	}
	if c := gp.calls.Load(); c != 1 {
		t.Errorf("provider calls = %d, want 1", c)
	}
	obs.mu.Lock()
	if obs.calls != 1 || obs.errors != 0 {
		t.Errorf("metered calls=%d errors=%d, want 1/0", obs.calls, obs.errors)
	}
	obs.mu.Unlock()
}

// A wait that gets its slot and then sees the provider fail produces exactly
// one record — the normal one, with the provider's error class and the wait
// in queue_wait_ms — never an extra queue_* record.
func TestThrottleCallLog_WaitThenProviderErrorLoggedOnce(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 2)}
	gp.failOn.Store(true)
	r, _ := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
	read := attachCallLog(t, r)
	finish := holdSlot(t, r, gp)

	waiterDone := make(chan error, 1)
	go func() {
		waiterDone <- callAt(context.Background(), r, gp, "guard")
	}()
	waitForWaiters(t, r, "p", "", 1)
	const queued = 40 * time.Millisecond
	time.Sleep(queued)
	finish()
	<-gp.entered
	gp.gate <- struct{}{}
	if err := <-waiterDone; err == nil {
		t.Fatal("waiter: want the provider error")
	}

	_, recs := read()
	assertPhases(t, recs, "side-call#1", "tool-loop#1")
	w := recs[1]
	if w.ErrorClass != ErrClassServer || w.HTTPStatus != 500 {
		t.Errorf("waiter error_class=%q http_status=%d, want server/500", w.ErrorClass, w.HTTPStatus)
	}
	if w.QueueWaitMs < queued.Milliseconds() {
		t.Errorf("waiter queue_wait_ms = %d, want >= %d", w.QueueWaitMs, queued.Milliseconds())
	}
	for _, rec := range recs {
		if rec.ErrorClass == ErrClassQueueTimeout || rec.ErrorClass == ErrClassQueueCancel {
			t.Errorf("unexpected queue record: %+v", rec)
		}
	}
}

func TestClassifyCallError_ThrottleWait(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ThrottleWaitError{Provider: "p", Err: context.DeadlineExceeded}, ErrClassQueueTimeout},
		{&ThrottleWaitError{Provider: "p", Err: context.Canceled}, ErrClassQueueCancel},
		{fmt.Errorf("attempt: %w", &ThrottleWaitError{Provider: "p", Err: context.Canceled}), ErrClassQueueCancel},
		{context.Canceled, ErrClassContextCancel},
		{context.DeadlineExceeded, ErrClassTimeout},
	} {
		if got := classifyCallError(tc.err); got != tc.want {
			t.Errorf("classifyCallError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
