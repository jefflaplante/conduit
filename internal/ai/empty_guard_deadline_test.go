package ai

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// conduit-10ip: the EmptyGuard retry and failover share the chain deadline.
// A same-model retry that hung until the deadline left the failover a dead
// context (journal Sep 25/26: "failover completed in 2ms: context deadline
// exceeded"). These tests shrink the budget constants and use real ctx
// deadlines.

// shrinkRecoveryBudget scales the recovery budget constants for a test.
func shrinkRecoveryBudget(t *testing.T, minAttempt, reserve, minRetry time.Duration) {
	t.Helper()
	pa, pr, pm := recoveryMinAttempt, recoveryFailoverReserve, recoveryMinRetry
	recoveryMinAttempt, recoveryFailoverReserve, recoveryMinRetry = minAttempt, reserve, minRetry
	t.Cleanup(func() { recoveryMinAttempt, recoveryFailoverReserve, recoveryMinRetry = pa, pr, pm })
}

// ctxProvider records every call's remaining ctx budget, then either hangs
// until ctx ends (hang=true) or returns the next scripted response, failing
// with the ctx error when ctx is already done.
type ctxProvider struct {
	name      string
	hang      bool
	responses []MockResponse

	mu    sync.Mutex
	calls int
	left  []time.Duration // remaining ctx time at each call (-1 = no deadline)
	reqs  []GenerateRequest
}

func (p *ctxProvider) Name() string { return p.name }

func (p *ctxProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	p.mu.Lock()
	i := p.calls
	p.calls++
	left := time.Duration(-1)
	if d, ok := ctx.Deadline(); ok {
		left = time.Until(d)
	}
	p.left = append(p.left, left)
	p.reqs = append(p.reqs, *req)
	p.mu.Unlock()

	if p.hang {
		<-ctx.Done()
		return nil, fmt.Errorf("request failed: Post \"https://api.z.ai/api/coding/paas/v4/chat/completions\": %w", ctx.Err())
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("request failed: %w", ctx.Err())
	}
	if i < len(p.responses) {
		r := p.responses[i]
		if r.Error != nil {
			return nil, r.Error
		}
		return &GenerateResponse{Content: r.Content, ToolCalls: r.ToolCalls, Usage: r.Usage}, nil
	}
	return &GenerateResponse{Content: "ok"}, nil
}

func (p *ctxProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *ctxProvider) leftAt(i int) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.left[i]
}

func withFailoverStub(t *testing.T, stub EmptyFailoverRouter) {
	t.Helper()
	prev := emptyFailoverRouter
	emptyFailoverRouter = stub
	t.Cleanup(func() { emptyFailoverRouter = prev })
}

func TestGuardEmptyResponse_FailoverGetsReservedSliceWhenRetryHangs(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	sick := &ctxProvider{name: "z-ai", hang: true}
	healthy := &ctxProvider{name: "anthropic", responses: []MockResponse{{Content: "recovered"}}}
	withFailoverStub(t, &failoverRouterStub{model: "claude-sonnet-4-6", provider: healthy, ok: true})

	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	req := &GenerateRequest{Model: "z-ai/glm-5.3-flash", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}

	resp, err := GuardEmptyResponse(ctx, sick, req, &GenerateResponse{}, nil, "depth4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "recovered" {
		t.Fatalf("content = %q, want failover content (failover starved by the hung retry?)", resp.Content)
	}
	if n := sick.callCount(); n != 1 {
		t.Errorf("same-model retry calls = %d, want 1", n)
	}
	if n := healthy.callCount(); n != 1 {
		t.Fatalf("failover calls = %d, want 1", n)
	}
	if left := healthy.leftAt(0); left < 200*time.Millisecond {
		t.Errorf("failover started with %s left, want >= ~reserve (300ms)", left)
	}
	if left := sick.leftAt(0); left > 650*time.Millisecond {
		t.Errorf("retry got %s, want deadline carved to leave the reserve (<= 600ms)", left)
	}
}

func TestGuardEmptyResponse_SkipsRetryWhenBudgetOnlyCoversFailover(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	sick := &ctxProvider{name: "z-ai", hang: true}
	healthy := &ctxProvider{name: "anthropic", responses: []MockResponse{{Content: "recovered"}}}
	withFailoverStub(t, &failoverRouterStub{model: "claude-sonnet-4-6", provider: healthy, ok: true})

	// 350ms left: reserve 300ms leaves 50ms < minRetry → straight to failover.
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	req := &GenerateRequest{Model: "z-ai/glm-5.3-flash"}

	resp, err := GuardEmptyResponse(ctx, sick, req, &GenerateResponse{}, nil, "depth0")
	if err != nil || resp.Content != "recovered" {
		t.Fatalf("resp=%+v err=%v, want failover content", resp, err)
	}
	if n := sick.callCount(); n != 0 {
		t.Errorf("same-model retry calls = %d, want 0 (skipped for lack of budget)", n)
	}
	if n := healthy.callCount(); n != 1 {
		t.Errorf("failover calls = %d, want 1", n)
	}
}

func TestGuardEmptyResponse_DeadContextMakesNoAttempts(t *testing.T) {
	sick := &ctxProvider{name: "z-ai"}
	healthy := &ctxProvider{name: "anthropic"}
	withFailoverStub(t, &failoverRouterStub{model: "claude-sonnet-4-6", provider: healthy, ok: true})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	resp, err := GuardEmptyResponse(ctx, sick, &GenerateRequest{Model: "z-ai/glm-5.3-flash"}, &GenerateResponse{}, nil, "depth2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !IsEmptyResponseFallback(resp.Content) {
		t.Errorf("content = %q, want the visible empty fallback", resp.Content)
	}
	if sick.callCount() != 0 || healthy.callCount() != 0 {
		t.Errorf("calls on dead ctx: retry=%d failover=%d, want 0/0", sick.callCount(), healthy.callCount())
	}
}

func TestGuardEmptyResponse_NoFailoverRouteRetryKeepsFullBudget(t *testing.T) {
	shrinkRecoveryBudget(t, 20*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond)
	p := &ctxProvider{name: "z-ai", responses: []MockResponse{{Content: "second try"}}}
	withFailoverStub(t, &failoverRouterStub{ok: false})

	// Less than the reserve left, but there is no failover to reserve for.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	resp, err := GuardEmptyResponse(ctx, p, &GenerateRequest{Model: "z-ai/glm-5.3"}, &GenerateResponse{}, nil, "initial")
	if err != nil || resp.Content != "second try" {
		t.Fatalf("resp=%+v err=%v, want the retry's content", resp, err)
	}
	if left := p.leftAt(0); left < 150*time.Millisecond {
		t.Errorf("retry got %s, want the full remaining budget (no reserve without failover)", left)
	}
}
