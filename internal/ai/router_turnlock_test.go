package ai

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/sessions"
)

// blockingProvider suspends inside GenerateResponse until its release channel is closed.
// Used to observe whether two concurrent calls on the same session overlap.
type blockingProvider struct {
	name    string
	active  atomic.Int32 // concurrent callers currently in GenerateResponse
	peak    atomic.Int32 // max observed concurrency
	release chan struct{}
}

func (p *blockingProvider) Name() string { return p.name }

func (p *blockingProvider) GenerateResponse(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	n := p.active.Add(1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	defer p.active.Add(-1)

	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &GenerateResponse{Content: "ok", Usage: Usage{}}, nil
}

// newTurnLockRouter builds a bare Router with just the blocking provider wired in,
// skipping the provider-type validation in initializeProviders.
func newTurnLockRouter(provider Provider) *Router {
	r := &Router{
		providers:    map[string]Provider{provider.Name(): provider},
		providerMeta: map[string]ProviderMeta{},
		default_:     provider.Name(),
		agentSystem:  &MockAgentSystem{},
		usageTracker: NewUsageTracker(),
	}
	return r
}

// TestRouter_PerSessionTurnLock verifies conduit-2e7r: two concurrent calls on the
// same session.Key must serialize; two calls on different sessions must run in parallel.
func TestRouter_PerSessionTurnLock(t *testing.T) {
	provider := &blockingProvider{name: "blocking", release: make(chan struct{})}
	router := newTurnLockRouter(provider)

	sess := &sessions.Session{Key: "session-A"}
	sessB := &sessions.Session{Key: "session-B"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	start := func(s *sessions.Session) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = router.GenerateResponseWithTools(ctx, s, "hi", "blocking", "")
		}()
	}

	// Two callers on the same session: expected peak concurrency = 1.
	start(sess)
	start(sess)

	// Give both goroutines time to reach (or queue at) the provider.
	time.Sleep(150 * time.Millisecond)
	if got := provider.peak.Load(); got != 1 {
		t.Fatalf("same-session peak concurrency = %d, want 1 (turn lock not serializing)", got)
	}

	// Now add a caller on a different session — should run in parallel with whatever
	// is currently inside the provider.
	start(sessB)
	time.Sleep(150 * time.Millisecond)
	if got := provider.peak.Load(); got != 2 {
		t.Fatalf("cross-session peak concurrency = %d, want 2 (different sessions should not block each other)", got)
	}

	// Release everyone and wait.
	close(provider.release)
	wg.Wait()
}

// TestRouter_TurnLock_EmptySessionKey ensures nil / empty-key sessions don't panic
// and don't serialize against each other (they should fall through as no-op locks).
func TestRouter_TurnLock_EmptySessionKey(t *testing.T) {
	provider := &blockingProvider{name: "blocking", release: make(chan struct{})}
	router := newTurnLockRouter(provider)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = router.GenerateResponseWithTools(ctx, &sessions.Session{Key: ""}, "hi", "blocking", "")
		}()
	}
	time.Sleep(150 * time.Millisecond)
	if got := provider.peak.Load(); got != 2 {
		t.Fatalf("empty-key peak concurrency = %d, want 2 (empty keys must not serialize)", got)
	}
	close(provider.release)
	wg.Wait()
}
