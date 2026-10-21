package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fastRetryPolicy keeps retry tests quick: same shape as the default,
// but backoff measured in milliseconds.
var fastRetryPolicy = webRetryPolicy{
	maxRetries:       2,
	baseDelay:        time.Millisecond,
	maxDelay:         5 * time.Millisecond,
	minAttemptBudget: 0,
}

func TestWebSearch_Retries429ThenSucceeds(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate limited"}`))
			return
		}
		resp := BraveSearchResponse{Type: "search"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		retry:       fastRetryPolicy,
	}
	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success, "expected success after retries, error: %s", result.Error)
	assert.EqualValues(t, 3, atomic.LoadInt32(&attempts), "expected exactly 3 requests (2 failures + 1 success)")
}

func TestWebSearch_RetriesExhausted(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		retry:       fastRetryPolicy,
	}
	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "429")
	// 1 initial attempt + maxRetries retries
	assert.EqualValues(t, fastRetryPolicy.maxRetries+1, atomic.LoadInt32(&attempts))
}

func TestWebSearch_DoesNotRetryUnauthorized(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid API key"))
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "invalid-key",
		retry:       fastRetryPolicy,
	}
	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.EqualValues(t, 1, atomic.LoadInt32(&attempts), "401 must not be retried")
}

func TestWebSearch_DoesNotRetryTransportErrors(t *testing.T) {
	var attempts int32
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, errors.New("offline test transport")
	})

	tool := &WebSearchTool{
		httpClient:  &http.Client{Transport: transport},
		braveAPIKey: "test-key",
		retry:       fastRetryPolicy,
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.EqualValues(t, 1, atomic.LoadInt32(&attempts), "transport errors must not be retried")
}

func TestWebSearch_Retries5xx(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		resp := BraveSearchResponse{Type: "search"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		retry:       fastRetryPolicy,
	}
	tool.httpClient.Transport = &testTransport{server: server}

	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.EqualValues(t, 2, atomic.LoadInt32(&attempts))
}

func TestWebSearch_HonorsRetryAfterMS(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("retry-after-ms", "50")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "slow down"}`))
			return
		}
		resp := BraveSearchResponse{Type: "search"}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Policy maxDelay (200ms) must exceed the server hint (50ms), otherwise
	// the hint is (by design) not honored and the request fails fast.
	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		retry: webRetryPolicy{
			maxRetries: 2,
			baseDelay:  10 * time.Millisecond,
			maxDelay:   200 * time.Millisecond,
		},
	}
	tool.httpClient.Transport = &testTransport{server: server}

	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.EqualValues(t, 2, atomic.LoadInt32(&attempts))
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "server Retry-After hint should be honored")
}

func TestWebSearch_RetryAfterTooLargeFailsFast(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Retry-After", "3600") // 1 hour — not worth sleeping
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		retry:       fastRetryPolicy,
	}
	tool.httpClient.Transport = &testTransport{server: server}

	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Less(t, elapsed, time.Second, "huge Retry-After must fail fast, not sleep")
	assert.EqualValues(t, 1, atomic.LoadInt32(&attempts))
}

// roundTripFunc lets tests stub the transport directly.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestWebRetryPolicy_DelayBackoffSequence(t *testing.T) {
	p := webRetryPolicy{baseDelay: 100 * time.Millisecond, maxDelay: time.Second}

	// delay(n) is 1-based: n=1 → [50ms, 100ms], n=2 → [100ms, 200ms], n=3 → [200ms, 400ms]
	cases := []struct {
		n        int
		minDelay time.Duration // equal-jitter floor (half of the step)
		maxDelay time.Duration // step ceiling
	}{
		{n: 1, minDelay: 50 * time.Millisecond, maxDelay: 100 * time.Millisecond},
		{n: 2, minDelay: 100 * time.Millisecond, maxDelay: 200 * time.Millisecond},
		{n: 3, minDelay: 200 * time.Millisecond, maxDelay: 400 * time.Millisecond},
	}
	for _, tc := range cases {
		d, ok := p.delay(tc.n, 0, false)
		require.True(t, ok)
		assert.GreaterOrEqual(t, d, tc.minDelay, "n=%d: below equal-jitter floor", tc.n)
		assert.LessOrEqual(t, d, tc.maxDelay, "n=%d: above equal-jitter ceiling", tc.n)
	}

	// n=0 is clamped to the first step rather than panicking.
	d, ok := p.delay(0, 0, false)
	require.True(t, ok)
	assert.GreaterOrEqual(t, d, 50*time.Millisecond)
	assert.LessOrEqual(t, d, 100*time.Millisecond)
}

func TestWebRetryPolicy_DelayCappedAtMax(t *testing.T) {
	p := webRetryPolicy{baseDelay: 100 * time.Millisecond, maxDelay: 300 * time.Millisecond}

	d, ok := p.delay(10, 0, false)
	require.True(t, ok)
	assert.LessOrEqual(t, d, 300*time.Millisecond)
	assert.GreaterOrEqual(t, d, 150*time.Millisecond) // half of cap
}

func TestWebRetryPolicy_DelayRetryAfterWins(t *testing.T) {
	p := webRetryPolicy{baseDelay: time.Second, maxDelay: 15 * time.Second}

	d, ok := p.delay(1, 2*time.Second, true)
	require.True(t, ok)
	assert.Equal(t, 2*time.Second, d, "server Retry-After should win over computed backoff")

	// Longer than maxDelay → not honored.
	_, ok = p.delay(1, time.Hour, true)
	assert.False(t, ok)

	// Non-positive Retry-After → not honored.
	_, ok = p.delay(1, 0, true)
	assert.False(t, ok)
}

func TestWebRetryPolicy_SleepRespectsDeadlineBudget(t *testing.T) {
	p := webRetryPolicy{minAttemptBudget: 3 * time.Second}

	// Deadline too close for backoff+budget → skip the retry.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := p.sleep(ctx, 500*time.Millisecond)
	require.ErrorIs(t, err, errWebRetryBudget)

	// Generous deadline → sleeps without error.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	err = p.sleep(ctx2, 5*time.Millisecond)
	require.NoError(t, err)
}

func TestWebRetryPolicy_SleepContextCancelled(t *testing.T) {
	p := webRetryPolicy{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.sleep(ctx, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Now()

	h := http.Header{}

	// retry-after-ms wins when present
	h.Set("retry-after-ms", "250")
	h.Set("Retry-After", "5")
	d, ok := parseRetryAfter(h, now)
	require.True(t, ok)
	assert.Equal(t, 250*time.Millisecond, d)

	// delta-seconds
	h.Del("retry-after-ms")
	h.Set("Retry-After", "5")
	d, ok = parseRetryAfter(h, now)
	require.True(t, ok)
	assert.Equal(t, 5*time.Second, d)

	// HTTP-date in the future
	h.Set("Retry-After", now.Add(10*time.Second).UTC().Format(http.TimeFormat))
	d, ok = parseRetryAfter(h, now)
	require.True(t, ok)
	assert.Greater(t, d, 5*time.Second)
	assert.LessOrEqual(t, d, 10*time.Second)

	// HTTP-date in the past → zero delay, still "handled"
	h.Set("Retry-After", now.Add(-10*time.Second).UTC().Format(http.TimeFormat))
	d, ok = parseRetryAfter(h, now)
	require.True(t, ok)
	assert.Equal(t, time.Duration(0), d)

	// garbage
	h.Set("Retry-After", "soon-ish")
	_, ok = parseRetryAfter(h, now)
	assert.False(t, ok)

	// absent
	_, ok = parseRetryAfter(http.Header{}, now)
	assert.False(t, ok)
}

func TestShouldRetryStatus(t *testing.T) {
	for _, code := range []int{429, 500, 502, 503, 504} {
		assert.True(t, shouldRetryStatus(code), "status %d should be retryable", code)
	}
	for _, code := range []int{200, 301, 400, 401, 403, 404, 422} {
		assert.False(t, shouldRetryStatus(code), "status %d should not be retryable", code)
	}
}

func TestWebSearch_DefaultPolicyWhenZeroValue(t *testing.T) {
	// A tool constructed directly (as tests and older callers do) must get
	// the default policy, not a zero-value one that never sleeps.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	tool := &WebSearchTool{
		httpClient:  server.Client(),
		braveAPIKey: "test-key",
		// retry intentionally zero-value
	}
	tool.httpClient.Transport = &testTransport{server: server}

	start := time.Now()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"query": "test"})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.False(t, result.Success) // server 429s forever
	assert.GreaterOrEqual(t, elapsed, 500*time.Millisecond,
		"zero-value policy must fall back to webDefaultRetryPolicy (first backoff ≥ 250ms)")
}
