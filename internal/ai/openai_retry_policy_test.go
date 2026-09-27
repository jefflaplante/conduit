package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// conduit-31jg.68(1): the OpenAI-compatible provider (z-ai glm-5.3,
// openrouter) retried 429/5xx on a fixed 2s/4s/8s schedule, ignoring
// retry-after and the caller's deadline, and retried z.ai quota errors
// (1113/1308/1310) that no retry can clear. It now shares the Anthropic
// retryPolicy (anthropic_retry.go).

type openAIScript struct {
	mu    sync.Mutex
	times []time.Time
	steps []func(w http.ResponseWriter)
}

func (s *openAIScript) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	i := len(s.times)
	s.times = append(s.times, time.Now())
	s.mu.Unlock()
	if i < len(s.steps) {
		s.steps[i](w)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
}

func (s *openAIScript) requests() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.times...)
}

func status(code int, headers map[string]string, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

const sseOK = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func newScriptedOpenAI(t *testing.T, s *openAIScript) *OpenAIProvider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	return &OpenAIProvider{
		name:    "z-ai",
		baseURL: srv.URL,
		client:  &http.Client{Timeout: 5 * time.Second},
		// Tiny computed backoff: a retry that waits ~200ms can only be
		// honouring retry-after.
		retry: retryPolicy{maxRetries: 3, baseDelay: time.Millisecond, maxDelay: 2 * time.Second, minAttemptBudget: 100 * time.Millisecond},
	}
}

func callOpenAI(p *OpenAIProvider, ctx context.Context, streaming bool) (*GenerateResponse, error) {
	req := &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	if streaming {
		return p.GenerateResponseStreaming(ctx, req, nil)
	}
	return p.GenerateResponse(ctx, req)
}

func TestOpenAI_429RetryAfterHonored(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := map[bool]string{false: "non-streaming", true: "streaming"}[streaming]
		t.Run(name, func(t *testing.T) {
			s := &openAIScript{steps: []func(http.ResponseWriter){
				status(429, map[string]string{"retry-after-ms": "250"}, `{"error":{"type":"rate_limit_error","message":"slow down"}}`),
			}}
			if streaming {
				s.steps = append(s.steps, func(w http.ResponseWriter) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(sseOK))
				})
			}
			p := newScriptedOpenAI(t, s)
			resp, err := callOpenAI(p, context.Background(), streaming)
			if err != nil {
				t.Fatalf("expected success after retry, got %v", err)
			}
			if resp.Content != "ok" {
				t.Errorf("content = %q", resp.Content)
			}
			times := s.requests()
			if len(times) != 2 {
				t.Fatalf("requests = %d, want 2", len(times))
			}
			if gap := times[1].Sub(times[0]); gap < 240*time.Millisecond {
				t.Errorf("retry after %s, want >= retry-after (250ms)", gap)
			}
		})
	}
}

func TestOpenAI_RetryAfterBoundedByDeadline(t *testing.T) {
	s := &openAIScript{steps: []func(http.ResponseWriter){
		status(429, map[string]string{"retry-after": "1"}, `{"error":{"message":"rate limited"}}`),
		status(200, nil, `{"choices":[{"message":{"content":"WRONG: slept past the deadline budget"}}]}`),
	}}
	p := newScriptedOpenAI(t, s)
	// 1s retry-after + 100ms minAttemptBudget does not fit in 600ms.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := callOpenAI(p, ctx, false)
	if err == nil {
		t.Fatal("expected the 429 to surface")
	}
	if providerStatusCode(err) != 429 {
		t.Errorf("err = %v, want the original 429", err)
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if el := time.Since(start); el > 300*time.Millisecond {
		t.Errorf("took %s — slept instead of surfacing (router fallback needs the time)", el)
	}
}

func TestOpenAI_RetryAfterAboveCapNotSlept(t *testing.T) {
	s := &openAIScript{steps: []func(http.ResponseWriter){
		status(503, map[string]string{"retry-after": "120"}, `overloaded`),
	}}
	p := newScriptedOpenAI(t, s)
	start := time.Now()
	if _, err := callOpenAI(p, context.Background(), false); err == nil {
		t.Fatal("expected the 503 to surface")
	}
	if n := len(s.requests()); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("took %s", el)
	}
}

func TestOpenAI_ZaiQuotaCodesNotRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"1113 insufficient balance", 429, `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`},
		{"1308 usage limit", 429, `{"error":{"code":"1308","message":"Usage limit reached for 5 hour."}}`},
		{"1310 weekly limit", 429, `{"error":{"code":1310,"message":"Weekly/Monthly Limit Exhausted."}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				s := &openAIScript{steps: []func(http.ResponseWriter){status(tc.status, nil, tc.body)}}
				p := newScriptedOpenAI(t, s)
				_, err := callOpenAI(p, context.Background(), streaming)
				if err == nil || !IsQuotaError(err) {
					t.Fatalf("streaming=%v: err = %v, want a quota error (router → fallback)", streaming, err)
				}
				if n := len(s.requests()); n != 1 {
					t.Errorf("streaming=%v: requests = %d, want 1 (quota is not retryable)", streaming, n)
				}
			}
		})
	}
}

func TestOpenAI_PlainRateLimit429StillRetried(t *testing.T) {
	s := &openAIScript{steps: []func(http.ResponseWriter){
		status(429, nil, `{"error":{"code":"1302","message":"High concurrency, please slow down."}}`),
	}}
	p := newScriptedOpenAI(t, s)
	if _, err := callOpenAI(p, context.Background(), false); err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if n := len(s.requests()); n != 2 {
		t.Errorf("requests = %d, want 2", n)
	}
}

// Client timeouts are owned by the router (bd-13p retry, then the
// conduit-1w48 fallback handoff). Retrying them in the provider as well
// multiplied a 600s z-ai timeout by 4 before the router even saw it.
func TestOpenAI_ClientTimeoutNotRetriedInProvider(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	p := &OpenAIProvider{name: "z-ai", baseURL: srv.URL, client: &http.Client{Timeout: 100 * time.Millisecond},
		retry: retryPolicy{maxRetries: 3, baseDelay: time.Millisecond, maxDelay: time.Second, minAttemptBudget: time.Millisecond}}
	_, err := callOpenAI(p, context.Background(), false)
	if err == nil || !IsTransientTimeoutError(err) {
		t.Fatalf("err = %v, want a transient timeout for the router", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("server calls = %d, want 1", calls)
	}
}
