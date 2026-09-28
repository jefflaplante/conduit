package ai

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/config"
)

// conduit-38cz: per-provider/model concurrency throttle at the provider-call
// choke points (callWithRecovery, contextGuardProvider.call,
// GenerateSideCall).

// gateProvider counts concurrent calls and blocks each until released (or
// until its ctx ends). It can also panic or fail on demand.
type gateProvider struct {
	name    string
	gate    chan struct{} // nil = return immediately
	cur     atomic.Int64
	max     atomic.Int64
	calls   atomic.Int64
	panicOn atomic.Bool
	failOn  atomic.Bool
	entered chan struct{} // optional: signalled on each call entry
	onEnter func()
}

func (p *gateProvider) Name() string { return p.name }

func (p *gateProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	p.calls.Add(1)
	n := p.cur.Add(1)
	defer p.cur.Add(-1)
	for {
		m := p.max.Load()
		if n <= m || p.max.CompareAndSwap(m, n) {
			break
		}
	}
	if p.onEnter != nil {
		p.onEnter()
	}
	if p.entered != nil {
		p.entered <- struct{}{}
	}
	if p.panicOn.Load() {
		panic("provider exploded")
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.failOn.Load() {
		return nil, errors.New("API error: 500 - boom")
	}
	return &GenerateResponse{Content: "ok", Usage: Usage{PromptTokens: 3, CompletionTokens: 1}}, nil
}

func (p *gateProvider) GenerateResponseStreaming(ctx context.Context, req *GenerateRequest, onDelta StreamCallback) (*GenerateResponse, error) {
	resp, err := p.GenerateResponse(ctx, req)
	if err == nil && onDelta != nil {
		onDelta("ok", true)
	}
	return resp, err
}

func throttleRouter(t *testing.T, p Provider, pcfg config.ProviderConfig) (*Router, *countingObserver) {
	t.Helper()
	r, err := NewRouter(config.AIConfig{DefaultProvider: pcfg.Name}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RegisterProvider(pcfg.Name, p)
	r.mu.Lock()
	r.providerMeta[pcfg.Name] = ProviderMeta{Name: pcfg.Name, Type: "openai", DefaultModel: pcfg.Model}
	r.mu.Unlock()
	r.throttle = NewProviderThrottle([]config.ProviderConfig{pcfg})
	obs := &countingObserver{}
	r.GetUsageTracker().SetObserver(obs)
	return r, obs
}

func slotStats(r *Router, provider, model string) ProviderSlotStats {
	for _, s := range r.ProviderSlots() {
		if s.Provider == provider && s.Model == model {
			return s
		}
	}
	return ProviderSlotStats{}
}

// 10 concurrent calls spread over all three choke points never exceed 2 in
// flight, all complete, and each is metered exactly once.
func TestThrottle_TenCallsMaxTwoInFlight(t *testing.T) {
	gp := &gateProvider{name: "p", gate: make(chan struct{})}
	r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 2})
	rt := providerRoute{name: "p", provider: gp}

	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			switch i % 3 {
			case 0:
				_, err = r.GenerateSideCall(context.Background(), "p", &GenerateRequest{})
			case 1:
				req := &GenerateRequest{}
				_, _, _, err = r.callWithRecovery(context.Background(), rt, req, recoveryOpts{phase: "test"})
			default:
				_, err = r.guardedProvider(rt).GenerateResponse(context.Background(), &GenerateRequest{})
			}
			errs <- err
		}(i)
	}

	// All 10 arrive: 2 in the provider, 8 queued.
	for end := time.Now().Add(5 * time.Second); ; {
		if gp.cur.Load() == 2 && slotStats(r, "p", "").Waiting == 8 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("never reached 2 in flight / 8 waiting: cur=%d stats=%+v", gp.cur.Load(), slotStats(r, "p", ""))
		}
		time.Sleep(time.Millisecond)
	}

	// Let calls through one at a time while checking the cap.
	deadline := time.After(5 * time.Second)
	for released := 0; released < 10; {
		select {
		case gp.gate <- struct{}{}:
			released++
		case <-deadline:
			t.Fatalf("stalled after %d releases (in flight %d)", released, gp.cur.Load())
		}
		if got := gp.cur.Load(); got > 2 {
			t.Fatalf("in flight = %d, want <= 2", got)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
	}
	if m := gp.max.Load(); m != 2 {
		t.Errorf("max concurrent = %d, want exactly 2", m)
	}
	if c := gp.calls.Load(); c != 10 {
		t.Errorf("provider calls = %d, want 10", c)
	}
	obs.mu.Lock()
	if obs.calls != 10 || obs.errors != 0 {
		t.Errorf("metered calls=%d errors=%d, want 10/0 (exactly once each)", obs.calls, obs.errors)
	}
	obs.mu.Unlock()
	s := slotStats(r, "p", "")
	if s.Limit != 2 || s.InFlight != 0 || s.Waiting != 0 || s.Acquired != 10 || s.Source != ThrottleSourceProviderConfig {
		t.Errorf("stats = %+v", s)
	}
	if s.Queued == 0 {
		t.Errorf("expected some calls to have queued: %+v", s)
	}
}

// A waiter gives up when its ctx deadline passes or it is cancelled; the
// provider is never called for it and nothing is metered.
func TestThrottle_WaiterHonorsDeadlineAndCancel(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			gp := &gateProvider{name: "p", gate: make(chan struct{}), entered: make(chan struct{}, 1)}
			r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})

			holderDone := make(chan error, 1)
			go func() {
				_, err := r.GenerateSideCall(context.Background(), "p", &GenerateRequest{})
				holderDone <- err
			}()
			<-gp.entered // the holder has the only slot

			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(context.Background())
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			defer cancel()
			start := time.Now()
			_, err := r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(ctx, &GenerateRequest{Model: "m1"})
			if time.Since(start) > 2*time.Second {
				t.Fatalf("waiter did not give up promptly")
			}
			var we *ThrottleWaitError
			if !errors.As(err, &we) {
				t.Fatalf("err = %v, want *ThrottleWaitError", err)
			}
			want := context.DeadlineExceeded
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Errorf("err = %v, want wrapping %v", err, want)
			}
			if mode == "deadline" && !IsTransientTimeoutError(err) {
				t.Errorf("deadline wait must stay a transient timeout for the recovery ladder")
			}
			if c := gp.calls.Load(); c != 1 {
				t.Errorf("provider calls = %d, want 1 (waiter never called it)", c)
			}
			s := slotStats(r, "p", "")
			if s.Abandoned != 1 || s.Waiting != 0 || s.InFlight != 1 {
				t.Errorf("stats = %+v", s)
			}

			gp.gate <- struct{}{}
			if err := <-holderDone; err != nil {
				t.Fatal(err)
			}
			obs.mu.Lock()
			if obs.calls != 1 || obs.errors != 0 {
				t.Errorf("metered calls=%d errors=%d, want 1/0 (abandoned wait not metered)", obs.calls, obs.errors)
			}
			obs.mu.Unlock()
		})
	}
}

// Slots are released on error, panic and at stream end.
func TestThrottle_SlotReleasedOnErrorPanicAndStreamEnd(t *testing.T) {
	quick := func(t *testing.T, r *Router, gp *gateProvider) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		gp.panicOn.Store(false)
		gp.failOn.Store(false)
		if _, err := r.GenerateSideCall(ctx, "p", &GenerateRequest{}); err != nil {
			t.Fatalf("slot leaked — follow-up call failed: %v", err)
		}
	}

	t.Run("error", func(t *testing.T) {
		gp := &gateProvider{name: "p"}
		r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
		gp.failOn.Store(true)
		if _, err := r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(context.Background(), &GenerateRequest{}); err == nil {
			t.Fatal("expected error")
		}
		if s := slotStats(r, "p", ""); s.InFlight != 0 {
			t.Fatalf("in flight after error = %d", s.InFlight)
		}
		quick(t, r, gp)
		obs.mu.Lock()
		defer obs.mu.Unlock()
		if obs.errors != 1 || obs.calls != 1 {
			t.Errorf("metered errors=%d calls=%d, want 1/1", obs.errors, obs.calls)
		}
	})

	t.Run("panic", func(t *testing.T) {
		gp := &gateProvider{name: "p"}
		r, _ := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
		gp.panicOn.Store(true)
		for _, call := range []func(){
			func() { _, _ = r.GenerateSideCall(context.Background(), "p", &GenerateRequest{}) },
			func() {
				_, _, _, _ = r.callWithRecovery(context.Background(), providerRoute{name: "p", provider: gp}, &GenerateRequest{}, recoveryOpts{phase: "test"})
			},
			func() {
				_, _ = r.guardedProvider(providerRoute{name: "p", provider: gp}).GenerateResponse(context.Background(), &GenerateRequest{})
			},
		} {
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected panic")
					}
				}()
				call()
			}()
			if s := slotStats(r, "p", ""); s.InFlight != 0 {
				t.Fatalf("in flight after panic = %d", s.InFlight)
			}
			gp.panicOn.Store(true)
		}
		quick(t, r, gp)
	})

	t.Run("stream end", func(t *testing.T) {
		gp := &gateProvider{name: "p"}
		r, obs := throttleRouter(t, gp, config.ProviderConfig{Name: "p", Model: "m1", MaxConcurrent: 1})
		var inFlightDuringStream int64 = -1
		cb := func(delta string, done bool) {
			inFlightDuringStream = slotStats(r, "p", "").InFlight
		}
		_, _, _, err := r.callWithRecovery(context.Background(), providerRoute{name: "p", provider: gp}, &GenerateRequest{},
			recoveryOpts{phase: "streaming", stream: newStreamTracker(cb)})
		if err != nil {
			t.Fatal(err)
		}
		if inFlightDuringStream != 1 {
			t.Errorf("in flight during stream = %d, want 1 (slot held until stream end)", inFlightDuringStream)
		}
		if s := slotStats(r, "p", ""); s.InFlight != 0 {
			t.Fatalf("in flight after stream end = %d", s.InFlight)
		}
		quick(t, r, gp)
		obs.mu.Lock()
		defer obs.mu.Unlock()
		if obs.calls != 2 {
			t.Errorf("metered calls = %d, want 2", obs.calls)
		}
	})
}

// A same-route timeout retry stuck in the queue gives up at its retry
// deadline, which leaves the fallback handoff its full reserve: queue time
// never eats into recoveryFailoverReserve.
func TestThrottle_QueuedRetryLeavesFallbackReserve(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	router, _, _ := newFallbackTestRouter(t)
	obs := &countingObserver{}
	router.GetUsageTracker().SetObserver(obs)
	router.throttle = NewProviderThrottle([]config.ProviderConfig{{Name: "primary", MaxConcurrent: 1}})

	// A concurrent turn grabs primary's only slot as soon as the first
	// attempt releases it, and keeps it.
	hold := make(chan struct{})
	defer close(hold)
	queued := make(chan struct{})
	primary := &gateProvider{name: "primary"}
	primary.onEnter = func() {
		go func() {
			rel, err := router.throttle.Acquire(context.Background(), "primary", "claude-haiku-4-5-20251001")
			if err != nil {
				return
			}
			<-hold
			rel()
		}()
		// Make sure the other turn is queued before this call returns.
		for end := time.Now().Add(2 * time.Second); slotStats(router, "primary", "").Waiting == 0 && time.Now().Before(end); {
			time.Sleep(time.Millisecond)
		}
		close(queued)
	}
	timeoutOnce := &timeoutProvider{gateProvider: primary}
	fallback := &ctxProvider{name: "fallbackprov", responses: []MockResponse{{Content: "fallback in time"}}}
	router.RegisterProvider("primary", timeoutOnce)
	router.RegisterProvider("fallbackprov", fallback)

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	resp, err := router.GenerateResponse(ctx, newFallbackSession(t), "hi", "primary")
	<-queued
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "fallback in time" {
		t.Errorf("content = %q", resp.Content)
	}
	if n := primary.calls.Load(); n != 1 {
		t.Errorf("primary calls = %d, want 1 (retry never got a slot)", n)
	}
	if n := fallback.callCount(); n != 1 {
		t.Fatalf("fallback calls = %d, want 1", n)
	}
	if left := fallback.leftAt(0); left < 200*time.Millisecond {
		t.Errorf("fallback started with %s left, want >= ~300ms reserve", left)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.errors != 1 {
		t.Errorf("metered errors = %d, want 1 (the queued retry made no call)", obs.errors)
	}
	if s := slotStats(router, "primary", ""); s.Abandoned != 1 {
		t.Errorf("primary abandoned waits = %d, want 1", s.Abandoned)
	}
}

// timeoutProvider fails every call with a transient timeout.
type timeoutProvider struct{ *gateProvider }

func (p *timeoutProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	if _, err := p.gateProvider.GenerateResponse(ctx, req); err != nil {
		return nil, err
	}
	return nil, errFallbackTimeout
}

func TestThrottle_PoolResolution(t *testing.T) {
	th := NewProviderThrottle([]config.ProviderConfig{
		{Name: "z-ai", Model: "glm-5.3-flash"},
		{Name: "zcustom", BaseURL: "https://api.z.ai/api/coding/paas/v4", MaxConcurrent: 7,
			ModelMaxConcurrent: map[string]int{"GLM-5.3": 2, "z-ai/glm-5.3-flash": 0}},
		{Name: "anthropic", Model: "claude-sonnet-4-6"},
		{Name: "openrouter", MaxConcurrent: 3},
	})
	cases := []struct {
		provider, model string
		wantModel       string
		limit           int
		source          string
	}{
		{"z-ai", "glm-5.3", "glm-5.3", 5, ThrottleSourceBuiltin},
		{"z-ai", "z-ai/GLM-5.3", "glm-5.3", 5, ThrottleSourceBuiltin},
		{"z-ai", "glm-5.3-flash", "glm-5.3-flash", 50, ThrottleSourceBuiltin},
		{"z-ai", "glm-4.6", "glm-4.6", 0, ThrottleSourceUnlimited},
		{"zcustom", "glm-5.3", "glm-5.3", 2, ThrottleSourceModelConfig},
		{"zcustom", "glm-5.3-flash", "glm-5.3-flash", 0, ThrottleSourceModelConfig},
		{"zcustom", "other", "", 7, ThrottleSourceProviderConfig},
		{"anthropic", "claude-sonnet-4-6", "claude-sonnet-4-6", 0, ThrottleSourceUnlimited},
		{"openrouter", "deepseek/deepseek-v4.1-flash", "", 3, ThrottleSourceProviderConfig},
		{"openrouter", "x/other", "", 3, ThrottleSourceProviderConfig},
	}
	for _, c := range cases {
		p := th.pool(c.provider, c.model)
		if p.model != c.wantModel || p.limit != c.limit || p.source != c.source {
			t.Errorf("%s/%s → pool model=%q limit=%d source=%s, want %q/%d/%s",
				c.provider, c.model, p.model, p.limit, p.source, c.wantModel, c.limit, c.source)
		}
	}
	// Two spellings of one model share one pool.
	if th.pool("z-ai", "glm-5.3") != th.pool("z-ai", "z-ai/glm-5.3") {
		t.Error("prefixed and bare model names must share a pool")
	}
	// A nil throttle is unlimited.
	var nilT *ProviderThrottle
	rel, err := nilT.Acquire(context.Background(), "x", "y")
	if err != nil {
		t.Fatal(err)
	}
	rel()
	rel() // idempotent
}
