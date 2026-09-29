package gateway

import (
	"context"
	"log/slog"
	"math"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/sessions"
)

// conduit-31jg.75: Image tool vision analyses go through the router's
// metered path — one usage-tracker record per call, priced on the provider +
// model that served it (cache tokens included) — and, when made from a tool
// inside a turn, their cost is part of that turn's request cost and the
// session's session_total_cost.

type visionMeterObserver struct {
	mu     sync.Mutex
	calls  map[string]int // "provider|model" → calls
	cacheR int
	cacheW int
}

func (o *visionMeterObserver) OnUsage(provider, model string, _, _, cw, cr int, _ int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.calls == nil {
		o.calls = map[string]int{}
	}
	o.calls[provider+"|"+model]++
	o.cacheW += cw
	o.cacheR += cr
}
func (o *visionMeterObserver) OnError(string, string) {}

func (o *visionMeterObserver) count(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[key]
}

func (o *visionMeterObserver) total() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, c := range o.calls {
		n += c
	}
	return n
}

// usageProvider returns a fixed reply and usage.
type usageProvider struct {
	name  string
	usage ai.Usage
	onGen func(ctx context.Context) // optional hook run inside the call
}

func (p *usageProvider) Name() string { return p.name }
func (p *usageProvider) GenerateResponse(ctx context.Context, _ *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	if p.onGen != nil {
		p.onGen(ctx)
	}
	return &ai.GenerateResponse{Content: "reply from " + p.name, FinishReason: "stop", Usage: p.usage}, nil
}

// Vision usage: haiku-4-5 at $1/$5, cache write 1.25x, cache read 0.1x.
var visionUsage = ai.Usage{PromptTokens: 1_000, CompletionTokens: 200, CacheCreationInputTokens: 2_000, CacheReadInputTokens: 10_000}

const visionCost = (1_000*1.0 + 200*5.0 + 2_000*1.25 + 10_000*0.1) / 1e6

// newVisionMeterGateway builds a gateway whose router has the chat provider
// "testprov" (llama3, override-priced) and a separate vision provider
// "vision" whose configured model is claude-haiku-4-5.
func newVisionMeterGateway(t *testing.T) (*Gateway, *sessions.Store, *ai.Router, *visionMeterObserver) {
	t.Helper()
	store, err := sessions.NewStore(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.AIConfig{
		DefaultProvider: "testprov",
		Providers: []config.ProviderConfig{
			{Name: "testprov", Type: "ollama", Model: "llama3", ContextWindow: 8000},
			{Name: "vision", Type: "ollama", Model: "claude-haiku-4-5", ContextWindow: 200000},
		},
		PricingOverrides: map[string]config.PricingOverride{
			"testprov/llama3": {InputPerMToken: 1.4, OutputPerMToken: 4.4},
		},
	}
	router, err := ai.NewRouter(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	router.SetPricingResolver(ai.NewPricingResolverFromConfig(cfg))
	router.SetSessionStore(store)
	obs := &visionMeterObserver{}
	router.GetUsageTracker().SetObserver(obs)
	router.RegisterProvider("vision", &usageProvider{name: "vision", usage: visionUsage})

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	gw := &Gateway{
		sessions:       store,
		channelManager: newTestChannelManager(t),
		ai:             router,
		logger:         logger,
		ws:             NewWebSocketService(logger, websocket.Upgrader{}, 16),
		config:         &config.Config{AI: cfg},
	}
	return gw, store, router, obs
}

func TestVisionAdapter_MeteredOnceWithCost(t *testing.T) {
	_, _, router, obs := newVisionMeterGateway(t)
	adapter := &visionAdapter{router: router, cfg: &config.VisionConfig{Provider: "vision"}}

	ctx, ledger := ai.WithSideCallLedger(context.Background())
	if _, err := adapter.AnalyzeImage(ctx, []byte{0xFF, 0xD8, 0xFF}, "image/jpeg", "what?"); err != nil {
		t.Fatal(err)
	}

	if got := obs.total(); got != 1 {
		t.Fatalf("metered calls = %d, want exactly 1", got)
	}
	if got := obs.count("vision|claude-haiku-4-5"); got != 1 {
		t.Fatalf("call not attributed to vision|claude-haiku-4-5: %v", obs.calls)
	}
	if obs.cacheW != 2_000 || obs.cacheR != 10_000 {
		t.Errorf("cache tokens metered = w%d r%d, want w2000 r10000", obs.cacheW, obs.cacheR)
	}
	u := ledger.Usage()
	if u.PricedCalls != 1 || u.UnpricedCalls != 0 {
		t.Fatalf("ledger calls = priced %d unpriced %d, want 1/0", u.PricedCalls, u.UnpricedCalls)
	}
	if math.Abs(u.CostUSD-visionCost) > 1e-12 {
		t.Errorf("vision cost = %v, want %v", u.CostUSD, visionCost)
	}
}

func TestVisionAdapter_CostIncludedInTurnAndSession(t *testing.T) {
	gw, store, router, obs := newVisionMeterGateway(t)
	adapter := &visionAdapter{router: router, cfg: &config.VisionConfig{Provider: "vision"}}

	// The chat provider stands in for a tool loop: while generating, it runs
	// the Image tool's analysis with the ctx the turn handed it (tools run
	// inside the turn ctx).
	chatUsage := ai.Usage{PromptTokens: 10_000, CompletionTokens: 1_000, TotalTokens: 11_000}
	var visionErr error
	router.RegisterProvider("testprov", &usageProvider{name: "testprov", usage: chatUsage, onGen: func(ctx context.Context) {
		_, visionErr = adapter.AnalyzeImage(ctx, []byte{0xFF, 0xD8, 0xFF}, "image/jpeg", "")
	}})

	gw.handleIncomingMessage(context.Background(), tgMsg("look at this"))
	if visionErr != nil {
		t.Fatal(visionErr)
	}

	if got := obs.count("vision|claude-haiku-4-5"); got != 1 {
		t.Fatalf("vision metered %d times, want 1 (%v)", got, obs.calls)
	}
	if got := obs.count("testprov|llama3"); got != 1 {
		t.Fatalf("chat metered %d times, want 1 (%v)", got, obs.calls)
	}

	sess, _ := store.GetOrCreateSession("42", "telegram")
	got, _ := strconv.ParseFloat(sess.Context["session_total_cost"], 64)
	chatCost := 10_000*1.4/1e6 + 1_000*4.4/1e6
	if want := chatCost + visionCost; math.Abs(got-want) > 1e-6 {
		t.Fatalf("session_total_cost = %v, want chat %v + vision %v = %v", got, chatCost, visionCost, want)
	}
	if n := sess.Context["session_unpriced_requests"]; n != "" {
		t.Errorf("session_unpriced_requests = %q, want unset", n)
	}
	if n := sess.Context["session_request_count"]; n != "1" {
		t.Errorf("session_request_count = %q, want 1 (vision is part of the turn)", n)
	}
}

// A vision call on an unpriced model marks the turn unpriced instead of
// silently adding $0.
func TestVisionAdapter_UnpricedVisionMarksTurnUnpriced(t *testing.T) {
	gw, store, router, _ := newVisionMeterGateway(t)
	// A provider with no configured model and no override has no price.
	router.RegisterProvider("vision-unpriced", &usageProvider{name: "vision-unpriced", usage: visionUsage})
	adapter := &visionAdapter{router: router, cfg: &config.VisionConfig{Provider: "vision-unpriced"}}

	router.RegisterProvider("testprov", &usageProvider{name: "testprov", usage: ai.Usage{PromptTokens: 10, CompletionTokens: 1}, onGen: func(ctx context.Context) {
		_, _ = adapter.AnalyzeImage(ctx, []byte{0xFF}, "image/jpeg", "")
	}})
	gw.handleIncomingMessage(context.Background(), tgMsg("look"))

	sess, _ := store.GetOrCreateSession("42", "telegram")
	if n := sess.Context["session_unpriced_requests"]; n != "1" {
		t.Fatalf("session_unpriced_requests = %q, want 1", n)
	}
}
