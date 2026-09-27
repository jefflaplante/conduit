package ai

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.64: every provider call of a turn — the first call, EmptyGuard
// retries, every tool-loop depth — reaches the usage tracker (fuel gauge,
// TokenWindowTracker) exactly once, and the session totals equal the sum.

type countingObserver struct {
	mu                      sync.Mutex
	calls, errors           int
	in, out, cacheW, cacheR int
	models                  map[string]int
}

func (o *countingObserver) OnUsage(provider, model string, in, out, cw, cr int, _ int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.in += in
	o.out += out
	o.cacheW += cw
	o.cacheR += cr
	if o.models == nil {
		o.models = map[string]int{}
	}
	o.models[provider+"|"+model]++
}

func (o *countingObserver) OnError(string, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.errors++
}

// sumToolLoopEngine mimics tools.ExecutionEngine's accounting: it calls the
// provider it was handed once per remaining tool round and folds every
// response into one whole-turn Usage with Usage.Add.
type sumToolLoopEngine struct{ rounds int }

func (e *sumToolLoopEngine) HandleToolCallFlow(ctx context.Context, provider Provider, req *GenerateRequest, resp *GenerateResponse) (ConversationResponse, error) {
	total := resp.Usage
	for i := 0; i < e.rounds; i++ {
		r, err := provider.GenerateResponse(ctx, req)
		if err != nil {
			return nil, err
		}
		total.Add(r.Usage)
		resp = r
	}
	return &SimpleConversationResponse{Content: resp.Content, Usage: &total, Steps: e.rounds + 1}, nil
}

func meteringRouter(t *testing.T) (*Router, *MockProvider, *countingObserver, *sessions.Store, *sessions.Session) {
	t.Helper()
	cfg := config.AIConfig{DefaultProvider: "z-ai", PricingOverrides: map[string]config.PricingOverride{
		"glm-5.3": {InputPerMToken: 1.4, OutputPerMToken: 4.4},
	}}
	r, err := NewRouterWithExecution(cfg, nil, &sumToolLoopEngine{rounds: 2})
	if err != nil {
		t.Fatal(err)
	}
	mock := NewMockProvider("z-ai")
	r.RegisterProvider("z-ai", mock)
	r.providerMeta["z-ai"] = ProviderMeta{Name: "z-ai", Type: "openai", DefaultModel: "glm-5.3"}
	obs := &countingObserver{}
	r.GetUsageTracker().SetObserver(obs)

	store, err := sessions.NewStore(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	r.SetSessionStore(store)
	sess, err := store.GetOrCreateSession("u1", "c1")
	if err != nil {
		t.Fatal(err)
	}

	tc := []ToolCall{{ID: "t1", Name: "Bash", Args: map[string]interface{}{}}}
	u := func(p, c, cw, cr int) Usage {
		return Usage{PromptTokens: p, CompletionTokens: c, TotalTokens: p + c, CacheCreationInputTokens: cw, CacheReadInputTokens: cr}
	}
	mock.SetResponses([]MockResponse{
		{Content: "", Usage: u(100, 0, 10, 0)},      // 1: raw empty → EmptyGuard retry
		{ToolCalls: tc, Usage: u(110, 20, 0, 50)},   // 2: guard retry → tool call
		{ToolCalls: tc, Usage: u(200, 30, 0, 60)},   // 3: tool loop depth 1
		{Content: "done", Usage: u(300, 40, 5, 70)}, // 4: tool loop depth 2 → final
	})
	return r, mock, obs, store, sess
}

func TestMetering_ToolChainPlusGuardRetryRecordsEveryCall(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			r, mock, obs, store, sess := meteringRouter(t)
			var resp ConversationResponse
			var err error
			if streaming {
				resp, err = r.GenerateResponseStreaming(context.Background(), sess, "go", "", "", func(string, bool) {})
			} else {
				resp, err = r.GenerateResponseWithTools(context.Background(), sess, "go", "", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := mock.GetCallCount(); got != 4 {
				t.Fatalf("provider calls = %d, want 4", got)
			}
			// Fuel gauge / TokenWindowTracker observer: exactly 4 calls, no
			// double counting, cache tokens included.
			if obs.calls != 4 || obs.errors != 0 {
				t.Fatalf("observer calls=%d errors=%d, want 4/0", obs.calls, obs.errors)
			}
			if obs.in != 710 || obs.out != 90 || obs.cacheW != 15 || obs.cacheR != 180 {
				t.Errorf("observer tokens in=%d out=%d cw=%d cr=%d", obs.in, obs.out, obs.cacheW, obs.cacheR)
			}
			if obs.models["z-ai|glm-5.3"] != 4 {
				t.Errorf("calls not attributed to the default model: %v", obs.models)
			}
			mr, ok := r.GetUsageTracker().GetModelUsage("glm-5.3")
			if !ok || mr.TotalRequests != 4 {
				t.Fatalf("tracker model usage = %+v", mr)
			}

			// Whole-turn usage equals the per-call sum, and so do the
			// session-store totals (recorded once per turn).
			u := resp.GetUsage()
			if u.PromptTokens != obs.in || u.CompletionTokens != obs.out ||
				u.CacheCreationInputTokens != obs.cacheW || u.CacheReadInputTokens != obs.cacheR {
				t.Errorf("turn usage %+v != per-call sum", *u)
			}
			if u.PricedCalls != 4 || u.UnpricedCalls != 0 {
				t.Errorf("priced/unpriced calls = %d/%d", u.PricedCalls, u.UnpricedCalls)
			}
			if math.Abs(u.CostUSD-r.GetUsageTracker().TotalCost()) > 1e-12 {
				t.Errorf("turn cost %v != tracker cost %v", u.CostUSD, r.GetUsageTracker().TotalCost())
			}
			want := (710*1.4 + 90*4.4 + 15*1.4*1.25 + 180*1.4*0.1) / 1e6
			if math.Abs(u.CostUSD-want) > 1e-12 {
				t.Errorf("turn cost = %v, want %v", u.CostUSD, want)
			}
			if c, ok := r.TurnCost("", "", *u); !ok || c != u.CostUSD {
				t.Errorf("TurnCost = %v ok=%v, want the metered sum %v", c, ok, u.CostUSD)
			}

			got, _ := store.GetSession(sess.Key)
			for key, want := range map[string]int{
				sessions.CtxKeySessionPromptTokensTotal:    obs.in,
				sessions.CtxKeySessionCompletionTokens:     obs.out,
				sessions.CtxKeySessionCacheCreationTotal:   obs.cacheW,
				sessions.CtxKeySessionCacheReadTokensTotal: obs.cacheR,
			} {
				if n, _ := strconv.Atoi(got.Context[key]); n != want {
					t.Errorf("session %s = %d, want %d", key, n, want)
				}
			}
		})
	}
}

// A failed attempt is recorded once as an error; the successful timeout
// retry once as usage.
func TestMetering_FailedAttemptRecordedOnce(t *testing.T) {
	r, err := NewRouter(config.AIConfig{DefaultProvider: "p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mock := NewMockProvider("p")
	r.RegisterProvider("p", mock)
	r.providerMeta["p"] = ProviderMeta{Name: "p", Type: "anthropic", DefaultModel: "claude-sonnet-4-6"}
	obs := &countingObserver{}
	r.GetUsageTracker().SetObserver(obs)
	mock.SetResponses([]MockResponse{
		{Error: context.DeadlineExceeded},
		{Content: "ok", Usage: Usage{PromptTokens: 10, CompletionTokens: 5}},
	})
	if _, err := r.GenerateResponse(context.Background(), newFallbackSession(t), "hi", ""); err != nil {
		t.Fatal(err)
	}
	if mock.GetCallCount() != 2 || obs.errors != 1 || obs.calls != 1 {
		t.Fatalf("provider calls=%d observer errors=%d usage=%d, want 2/1/1", mock.GetCallCount(), obs.errors, obs.calls)
	}
}
