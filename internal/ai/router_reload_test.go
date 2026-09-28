package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// replyServer answers OpenAI chat completions with content; when gate is
// non-nil each request signals entered and blocks until gate is closed.
func replyServer(t *testing.T, content string, entered chan<- struct{}, gate <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gate != nil {
			entered <- struct{}{}
			<-gate
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"message":       map[string]interface{}{"content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reloadTestAI(baseURL string, maxConcurrent int) config.AIConfig {
	return config.AIConfig{
		DefaultProvider: "p",
		Providers: []config.ProviderConfig{
			{Name: "p", Type: "openai", BaseURL: baseURL, Model: "m1", MaxConcurrent: maxConcurrent},
			{Name: "q", Type: "openai", BaseURL: baseURL, Model: "m2"},
		},
	}
}

func TestProviderReload_RebuildsOnlyChangedProviders(t *testing.T) {
	r, err := NewRouter(reloadTestAI("http://127.0.0.1:1", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	p0, _ := r.GetProvider("p")
	q0, _ := r.GetProvider("q")

	next := reloadTestAI("http://127.0.0.1:1", 3)
	next.Providers[0].Model = "m1b"
	prep, err := r.PrepareProviderReload(next)
	if err != nil {
		t.Fatal(err)
	}
	if got := prep.Changed(); len(got) != 1 || got[0] != "p" {
		t.Fatalf("Changed = %v, want [p]", got)
	}
	// Nothing visible before Commit.
	if p, _ := r.GetProvider("p"); p != p0 {
		t.Fatal("provider swapped before Commit")
	}
	if r.DefaultModel() != "m1" {
		t.Fatal("metadata changed before Commit")
	}

	prep.Commit()
	prep.Commit() // idempotent
	if p, _ := r.GetProvider("p"); p == p0 {
		t.Error("changed provider was not rebuilt")
	}
	if q, _ := r.GetProvider("q"); q != q0 {
		t.Error("unchanged provider was rebuilt")
	}
	if r.DefaultModel() != "m1b" {
		t.Errorf("DefaultModel = %q, want m1b", r.DefaultModel())
	}

	// The new max_concurrent is what the throttle enforces.
	rel, err := r.acquireProviderSlot(context.Background(), "p", "")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	found := false
	for _, s := range r.ProviderSlots() {
		if s.Provider == "p" {
			found = true
			if s.Limit != 3 || s.Source != ThrottleSourceProviderConfig {
				t.Errorf("slot stats = %+v, want limit 3 from provider_config", s)
			}
		}
	}
	if !found {
		t.Fatal("no pool for provider p")
	}
}

func TestProviderReload_RefusesStructuralChanges(t *testing.T) {
	r, err := NewRouter(reloadTestAI("http://127.0.0.1:1", 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	add := reloadTestAI("http://127.0.0.1:1", 0)
	add.Providers = append(add.Providers, config.ProviderConfig{Name: "r", Type: "openai", Model: "x"})
	remove := reloadTestAI("http://127.0.0.1:1", 0)
	remove.Providers = remove.Providers[:1]
	retype := reloadTestAI("http://127.0.0.1:1", 0)
	retype.Providers[1].Type = "ollama"
	rename := reloadTestAI("http://127.0.0.1:1", 0)
	rename.Providers[1].Name = "q2"
	for name, cfg := range map[string]config.AIConfig{"add": add, "remove": remove, "retype": retype, "rename": rename} {
		if _, err := r.PrepareProviderReload(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestThrottleSetLimits_InFlightSlotReleasesToOldPool(t *testing.T) {
	th := NewProviderThrottle([]config.ProviderConfig{{Name: "p", MaxConcurrent: 1}})
	rel1, err := th.Acquire(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if changed := th.SetLimits([]config.ProviderConfig{{Name: "p", MaxConcurrent: 2}}); len(changed) != 1 {
		t.Fatalf("changed = %v", changed)
	}
	// The new pool has two free slots even though a call still holds one of
	// the old pool's.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rel2, err := th.Acquire(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	rel3, err := th.Acquire(ctx, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	rel1() // back to the dropped pool: must not block or panic
	rel2()
	rel3()
	snap := th.Snapshot()
	if len(snap) != 1 || snap[0].Limit != 2 || snap[0].InFlight != 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
	// Unchanged limits keep the pool.
	if changed := th.SetLimits([]config.ProviderConfig{{Name: "p", MaxConcurrent: 2}}); len(changed) != 0 {
		t.Errorf("unchanged limits reported changed: %v", changed)
	}
	if snap := th.Snapshot(); len(snap) != 1 || snap[0].Acquired != 2 {
		t.Errorf("pool counters lost for unchanged provider: %+v", snap)
	}
}

// A turn in flight during a reload finishes on the provider instance it
// started with; the next turn uses the new one.
func TestProviderReload_InFlightTurnKeepsOldProvider(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	oldSrv := replyServer(t, "from-old", entered, gate)
	newSrv := replyServer(t, "from-new", nil, nil)

	r, err := NewRouter(reloadTestAI(oldSrv.URL, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := &sessions.Session{Key: "s1"}

	var wg sync.WaitGroup
	var inflight *GenerateResponse
	var inflightErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		inflight, inflightErr = r.GenerateResponse(context.Background(), session, "hi", "p")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call never reached the old server")
	}

	prep, err := r.PrepareProviderReload(reloadTestAI(newSrv.URL, 0))
	if err != nil {
		t.Fatal(err)
	}
	prep.Commit()

	resp, err := r.GenerateResponse(context.Background(), &sessions.Session{Key: "s2"}, "hi", "p")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "from-new" {
		t.Errorf("new turn got %q, want from-new", resp.Content)
	}

	close(gate)
	wg.Wait()
	if inflightErr != nil {
		t.Fatal(inflightErr)
	}
	if inflight.Content != "from-old" {
		t.Errorf("in-flight turn got %q, want from-old", inflight.Content)
	}
}

func TestSetPricingResolver_ConcurrentSwap(t *testing.T) {
	r, err := NewRouter(config.AIConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.SetPricingResolver(NewPricingResolver(nil))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = r.ResolvePricing("claude-sonnet-4-6")
			}
		}()
	}
	wg.Wait()
}
