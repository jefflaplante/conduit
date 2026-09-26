package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// conduit-31jg.46: Anthropic had no retry/backoff for 429/529/overloaded.

type scriptedReply func(w http.ResponseWriter)

func scriptedServer(t *testing.T, replies ...scriptedReply) (*httptest.Server, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(atomic.AddInt32(&n, 1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		replies[i](w)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func statusReply(code int, headers map[string]string, body string) scriptedReply {
	return func(w http.ResponseWriter) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func jsonReply(body map[string]interface{}) scriptedReply {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}

func sseReply(events ...map[string]interface{}) scriptedReply {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], b)
		}
	}
}

func sseText(s string) map[string]interface{} {
	return map[string]interface{}{"type": "content_block_delta", "index": 0, "delta": map[string]interface{}{"type": "text_delta", "text": s}}
}

var (
	sseStart      = map[string]interface{}{"type": "message_start", "message": map[string]interface{}{"usage": map[string]interface{}{"input_tokens": 5}}}
	sseBlockStart = map[string]interface{}{"type": "content_block_start", "index": 0, "content_block": map[string]interface{}{"type": "text", "text": ""}}
	sseBlockStop  = map[string]interface{}{"type": "content_block_stop", "index": 0}
	sseEnd        = map[string]interface{}{"type": "message_delta", "delta": map[string]interface{}{"stop_reason": "end_turn"}, "usage": map[string]interface{}{"output_tokens": 3}}
	sseStop       = map[string]interface{}{"type": "message_stop"}
	sseOverloaded = map[string]interface{}{"type": "error", "error": map[string]interface{}{"type": "overloaded_error", "message": "Overloaded"}}
)

const overloadedBody = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
const rateLimitBody = `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`

func fastRetryAnthropic(t *testing.T, url string) *AnthropicProvider {
	t.Helper()
	p := newTestAnthropic(t, url)
	p.retry = retryPolicy{maxRetries: 3, baseDelay: time.Millisecond, maxDelay: 2 * time.Second}
	return p
}

func simpleReq() *GenerateRequest {
	return &GenerateRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 100}
}

func TestAnthropic_529ThenSuccess_RetriesHonouringRetryAfter(t *testing.T) {
	srv, n := scriptedServer(t,
		statusReply(529, map[string]string{"retry-after": "1"}, overloadedBody),
		statusReply(429, map[string]string{"retry-after-ms": "20"}, rateLimitBody),
		jsonReply(anthropicMsg("end_turn", textBlock("recovered"))),
	)
	p := fastRetryAnthropic(t, srv.URL)

	start := time.Now()
	resp, err := p.GenerateResponse(context.Background(), simpleReq())
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if resp.Content != "recovered" {
		t.Errorf("content = %q", resp.Content)
	}
	if got := atomic.LoadInt32(n); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
	if el := time.Since(start); el < time.Second {
		t.Errorf("elapsed %s: retry-after: 1 was not honoured", el)
	}
}

func TestAnthropic_RetryAfterBeyondCapIsNotWaited(t *testing.T) {
	srv, n := scriptedServer(t, statusReply(429, map[string]string{"retry-after": "120"}, rateLimitBody))
	p := fastRetryAnthropic(t, srv.URL)
	start := time.Now()
	_, err := p.GenerateResponse(context.Background(), simpleReq())
	if err == nil || !strings.Contains(err.Error(), "API error: 429") {
		t.Fatalf("expected the 429 to surface, got %v", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if time.Since(start) > time.Second {
		t.Error("must not block on an over-cap retry-after")
	}
}

func TestAnthropic_NonRetryableAndQuotaAreNotRetried(t *testing.T) {
	for name, reply := range map[string]scriptedReply{
		"400 invalid":            statusReply(400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`),
		"401":                    statusReply(401, nil, `{"type":"error","error":{"type":"authentication_error","message":"no"}}`),
		"429 quota":              statusReply(429, nil, `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`),
		"529 should-retry=false": statusReply(529, map[string]string{"x-should-retry": "false"}, overloadedBody),
	} {
		t.Run(name, func(t *testing.T) {
			srv, n := scriptedServer(t, reply, jsonReply(anthropicMsg("end_turn", textBlock("nope"))))
			p := fastRetryAnthropic(t, srv.URL)
			if _, err := p.GenerateResponse(context.Background(), simpleReq()); err == nil {
				t.Fatal("expected error")
			}
			if got := atomic.LoadInt32(n); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
		})
	}
}

func TestAnthropic_RetriesExhausted(t *testing.T) {
	srv, n := scriptedServer(t, statusReply(529, nil, overloadedBody))
	p := fastRetryAnthropic(t, srv.URL)
	_, err := p.GenerateResponse(context.Background(), simpleReq())
	if err == nil || !strings.Contains(err.Error(), "529") {
		t.Fatalf("expected 529 error, got %v", err)
	}
	if got := atomic.LoadInt32(n); got != 4 {
		t.Errorf("requests = %d, want 1 + 3 retries", got)
	}
}

// conduit-10ip: retries share the turn's chain deadline — a backoff that
// would eat the remaining budget is skipped and the error surfaces.
func TestAnthropic_RetryRespectsCtxDeadline(t *testing.T) {
	srv, n := scriptedServer(t, statusReply(529, map[string]string{"retry-after": "1"}, overloadedBody))
	p := fastRetryAnthropic(t, srv.URL)
	p.retry.minAttemptBudget = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := p.GenerateResponse(ctx, simpleReq())
	if err == nil || !strings.Contains(err.Error(), "529") {
		t.Fatalf("expected the original 529 (not a ctx error), got %v", err)
	}
	if got := atomic.LoadInt32(n); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Error("slept past the point where a retry could still fit in the deadline")
	}
}

func TestAnthropicStreaming_529ThenSuccess(t *testing.T) {
	srv, n := scriptedServer(t,
		statusReply(529, map[string]string{"retry-after-ms": "10"}, overloadedBody),
		sseReply(sseStart, sseBlockStart, sseText("hello"), sseBlockStop, sseEnd, sseStop),
	)
	p := fastRetryAnthropic(t, srv.URL)
	var got strings.Builder
	resp, err := p.GenerateResponseStreaming(context.Background(), simpleReq(), func(d string, _ bool) { got.WriteString(d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Content != "hello" || got.String() != "hello" {
		t.Errorf("content=%q streamed=%q", resp.Content, got.String())
	}
	if c := atomic.LoadInt32(n); c != 2 {
		t.Errorf("requests = %d, want 2", c)
	}
}

func TestAnthropicStreaming_MidStreamOverloadBeforeTextIsRetried(t *testing.T) {
	srv, n := scriptedServer(t,
		sseReply(sseStart, sseOverloaded),
		sseReply(sseStart, sseBlockStart, sseText("fine now"), sseBlockStop, sseEnd, sseStop),
	)
	p := fastRetryAnthropic(t, srv.URL)
	var got strings.Builder
	resp, err := p.GenerateResponseStreaming(context.Background(), simpleReq(), func(d string, _ bool) { got.WriteString(d) })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Content != "fine now" || got.String() != "fine now" {
		t.Errorf("content=%q streamed=%q", resp.Content, got.String())
	}
	if c := atomic.LoadInt32(n); c != 2 {
		t.Errorf("requests = %d, want 2", c)
	}
}

func TestAnthropicStreaming_MidStreamOverloadAfterTextIsNotDuplicated(t *testing.T) {
	srv, n := scriptedServer(t,
		sseReply(sseStart, sseBlockStart, sseText("partial "), sseOverloaded),
		sseReply(sseStart, sseBlockStart, sseText("partial answer"), sseBlockStop, sseEnd, sseStop),
	)
	p := fastRetryAnthropic(t, srv.URL)
	var got strings.Builder
	_, err := p.GenerateResponseStreaming(context.Background(), simpleReq(), func(d string, _ bool) { got.WriteString(d) })
	var se *anthropicStreamError
	if !errors.As(err, &se) || se.Type != "overloaded_error" {
		t.Fatalf("expected typed overloaded_error, got %v", err)
	}
	if got.String() != "partial " {
		t.Errorf("streamed %q — provider must not replay after emitting text", got.String())
	}
	if c := atomic.LoadInt32(n); c != 1 {
		t.Errorf("requests = %d, want 1", c)
	}
	if !IsRetryableOverloadError(err) {
		t.Error("mid-stream overloaded_error should classify as retryable overload for the router")
	}
}

// Router level: after text was streamed, an overload error gets ONE muted
// retry on the same route; the client sees no duplicate text and the turn
// returns the complete content.
func TestRouterStreaming_OverloadAfterEmissionGetsMutedRetry(t *testing.T) {
	router, primary, fallback := newFallbackTestRouter(t)
	sp := &partialThenErrorStreamer{MockProvider: primary, partial: "The answ",
		err: &anthropicStreamError{Type: "overloaded_error", Message: "Overloaded"}, final: "The answer is 42."}
	router.RegisterProvider("primary", sp)

	var streamed strings.Builder
	resp, err := router.GenerateResponseStreaming(context.Background(), newFallbackSession(t), "q", "primary", "claude-haiku-4-5-20251001",
		func(d string, _ bool) { streamed.WriteString(d) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetContent() != "The answer is 42." {
		t.Errorf("content = %q", resp.GetContent())
	}
	if streamed.String() != "The answ" {
		t.Errorf("client received %q, want only the first partial (retry muted)", streamed.String())
	}
	if sp.calls != 2 {
		t.Errorf("calls = %d, want 2", sp.calls)
	}
	if fallback.GetCallCount() != 0 {
		t.Error("overload must not trigger the quota fallback")
	}
}

func TestIsRetryableOverloadError(t *testing.T) {
	cases := map[error]bool{
		errors.New("API error: 529 - " + overloadedBody):                                         true,
		errors.New("API error: 429 - " + rateLimitBody):                                          true,
		errors.New("API error: 503 - unavailable"):                                               true,
		&anthropicStreamError{Type: "overloaded_error"}:                                          true,
		&anthropicStreamError{Type: "invalid_request_error"}:                                     false,
		errors.New(`API error: 429 - {"error":{"type":"insufficient_quota","message":"quota"}}`): false,
		errors.New("API error: 400 - out of extra usage"):                                        false,
		errors.New("API error: 500 - limit 4000"):                                                false,
		nil: false,
	}
	for err, want := range cases {
		if got := IsRetryableOverloadError(err); got != want {
			t.Errorf("IsRetryableOverloadError(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("retry-after", "3")
	if d, ok := parseRetryAfter(h, now); !ok || d != 3*time.Second {
		t.Errorf("seconds: got %v %v", d, ok)
	}
	h = http.Header{}
	h.Set("retry-after-ms", "250")
	h.Set("retry-after", "9")
	if d, ok := parseRetryAfter(h, now); !ok || d != 250*time.Millisecond {
		t.Errorf("ms wins: got %v %v", d, ok)
	}
	h = http.Header{}
	h.Set("retry-after", now.Add(5*time.Second).Format(http.TimeFormat))
	if d, ok := parseRetryAfter(h, now); !ok || d != 5*time.Second {
		t.Errorf("http-date: got %v %v", d, ok)
	}
	if _, ok := parseRetryAfter(http.Header{}, now); ok {
		t.Error("absent header should report ok=false")
	}
}
