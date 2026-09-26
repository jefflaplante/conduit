package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Anthropic retry/backoff (conduit-31jg.46).
//
// The Anthropic provider had no retry for 429 rate_limit_error, 529
// overloaded_error or transient 5xx (the OpenAI provider does, openai.go),
// and the router only retries quota (→ fallback model) and timeouts. A busy
// Anthropic backend therefore failed the whole turn on the first 529.
//
// Retries are bounded, jittered, honour retry-after, and never outlive the
// caller's ctx deadline (conduit-10ip: the turn's retries share one chain
// deadline — a backoff that would leave less than minAttemptBudget for the
// retried call is skipped and the original error surfaces instead, so the
// router's fallback logic still gets a chance to run).

// retryPolicy bounds provider-level retries.
type retryPolicy struct {
	maxRetries       int           // retries after the first attempt
	baseDelay        time.Duration // first backoff step (doubles per retry)
	maxDelay         time.Duration // cap for backoff AND for honoured retry-after
	minAttemptBudget time.Duration // ctx time that must remain after the sleep
}

var defaultAnthropicRetryPolicy = retryPolicy{
	maxRetries:       3,
	baseDelay:        time.Second,
	maxDelay:         30 * time.Second,
	minAttemptBudget: 10 * time.Second,
}

// errRetryBudget reports that the ctx deadline leaves no room for a retry.
var errRetryBudget = errors.New("retry skipped: not enough time left before the caller's deadline")

// delay returns the backoff before retry number n (1-based). A server-sent
// retry-after wins over computed backoff; one longer than maxDelay is not
// honoured by sleeping — ok=false, so the caller surfaces the error (and the
// router can fall back) instead of blocking the turn.
func (p retryPolicy) delay(n int, retryAfter time.Duration, hasRetryAfter bool) (time.Duration, bool) {
	if hasRetryAfter {
		if retryAfter > p.maxDelay {
			return 0, false
		}
		// Small positive jitter so concurrent sessions don't retry in lockstep.
		return retryAfter + time.Duration(rand.Int64N(int64(retryAfter/10)+1)), true
	}
	d := p.baseDelay << (n - 1)
	if d <= 0 || d > p.maxDelay {
		d = p.maxDelay
	}
	// Equal jitter: [d/2, d].
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1)), true
}

// sleep waits d unless ctx ends first or the ctx deadline would leave less
// than minAttemptBudget for the retried call.
func (p retryPolicy) sleep(ctx context.Context, d time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d+p.minAttemptBudget {
		return errRetryBudget
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// parseRetryAfter reads retry-after-ms, then retry-after (delta-seconds or
// HTTP-date).
func parseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := strings.TrimSpace(h.Get("retry-after"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// isRetryableAnthropicStatus: request timeout, lock conflict, rate limit,
// transient server errors and 529 overloaded (the Anthropic SDK set).
func isRetryableAnthropicStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// anthropicStreamError is an `event: error` received mid-stream.
// conduit-31jg.12 surfaced these; conduit-31jg.46 types them so the retry
// logic can tell overloaded_error from, say, invalid_request_error. The
// message format is unchanged.
type anthropicStreamError struct {
	Type    string
	Message string
}

func (e *anthropicStreamError) Error() string {
	return fmt.Sprintf("anthropic stream error: %s: %s", e.Type, e.Message)
}

func asAnthropicStreamError(err error) (*anthropicStreamError, bool) {
	var se *anthropicStreamError
	ok := errors.As(err, &se)
	return se, ok
}

// isRetryableAnthropicErrorType reports whether an Anthropic error type is
// transient.
func isRetryableAnthropicErrorType(t string) bool {
	switch t {
	case "overloaded_error", "rate_limit_error", "api_error", "timeout_error":
		return true
	}
	return false
}

// anthropicHTTPError is a non-200 reply from /v1/messages.
type anthropicHTTPError struct {
	status     int
	retryAfter time.Duration
	hasRA      bool
	retryable  bool
	err        error // "API error: NNN - body" (format relied on by classifiers)
}

// sendMessages performs ONE POST to /v1/messages. On a non-200 it drains
// the body and returns an anthropicHTTPError describing whether a retry
// may help.
func (a *AnthropicProvider) sendMessages(ctx context.Context, body map[string]interface{}, stream bool) (*http.Response, *anthropicHTTPError, error) {
	httpReq, err := a.newMessagesHTTPRequest(ctx, body, stream)
	if err != nil {
		return nil, nil, err
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		// Transport errors (incl. client timeouts) are not retried here —
		// the router's bd-13p timeout retry owns those.
		return nil, nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil, nil
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	he := &anthropicHTTPError{
		status: resp.StatusCode,
		err:    fmt.Errorf("API error: %d - %s", resp.StatusCode, string(bodyBytes)),
	}
	he.retryAfter, he.hasRA = parseRetryAfter(resp.Header, time.Now())
	he.retryable = isRetryableAnthropicStatus(resp.StatusCode) &&
		!strings.EqualFold(resp.Header.Get("x-should-retry"), "false") &&
		!IsQuotaError(he.err) // quota → router fallback, not a same-provider retry
	return nil, he, nil
}

// backoff decides whether retry number n (1-based) may run after lastErr and
// sleeps accordingly. Returns false when the caller must give up and return
// lastErr.
func (a *AnthropicProvider) backoff(ctx context.Context, n int, lastErr error, retryAfter time.Duration, hasRA bool, what string) bool {
	if n > a.retry.maxRetries {
		log.Printf("[Anthropic] %s: retries exhausted (%d) — surfacing: %v (conduit-31jg.46)", what, a.retry.maxRetries, lastErr)
		return false
	}
	d, ok := a.retry.delay(n, retryAfter, hasRA)
	if !ok {
		log.Printf("[Anthropic] %s: retry-after %s exceeds cap %s — not waiting (conduit-31jg.46)", what, retryAfter, a.retry.maxDelay)
		return false
	}
	log.Printf("[Anthropic] %s: retry %d/%d in %s (retry-after=%v) (conduit-31jg.46)", what, n, a.retry.maxRetries, d.Round(time.Millisecond), hasRA)
	if err := a.retry.sleep(ctx, d); err != nil {
		log.Printf("[Anthropic] %s: retry %d skipped: %v (conduit-31jg.46)", what, n, err)
		return false
	}
	return true
}

// postMessagesWithRetry performs the non-streaming POST with bounded retry
// for retryable HTTP statuses.
func (a *AnthropicProvider) postMessagesWithRetry(ctx context.Context, body map[string]interface{}) (*http.Response, error) {
	for n := 1; ; n++ {
		resp, he, err := a.sendMessages(ctx, body, false)
		if err != nil {
			return nil, err
		}
		if he == nil {
			return resp, nil
		}
		if !he.retryable || !a.backoff(ctx, n, he.err, he.retryAfter, he.hasRA, fmt.Sprintf("HTTP %d", he.status)) {
			return nil, he.err
		}
	}
}

// streamMessagesWithRetry performs the streaming request with bounded retry
// for retryable HTTP statuses AND for retryable mid-stream error events —
// the latter only while nothing has been emitted to onDelta in this call,
// so the client never receives duplicated text. Once text has been emitted
// the error is returned (with the partial response) for the router to
// handle.
func (a *AnthropicProvider) streamMessagesWithRetry(ctx context.Context, body map[string]interface{}, onDelta StreamCallback) (*GenerateResponse, error) {
	emitted := false
	tracked := func(delta string, done bool) {
		if delta != "" {
			emitted = true
		}
		if onDelta != nil {
			onDelta(delta, done)
		}
	}
	for n := 1; ; n++ {
		resp, he, err := a.sendMessages(ctx, body, true)
		if err != nil {
			return nil, err
		}
		if he != nil {
			if !he.retryable || !a.backoff(ctx, n, he.err, he.retryAfter, he.hasRA, fmt.Sprintf("stream HTTP %d", he.status)) {
				return nil, he.err
			}
			continue
		}

		result, streamErr := a.parseSSEStream(resp.Body, tracked)
		resp.Body.Close()
		if streamErr == nil {
			return result, nil
		}
		se, typed := asAnthropicStreamError(streamErr)
		if !typed || !isRetryableAnthropicErrorType(se.Type) || emitted {
			if typed && emitted && isRetryableAnthropicErrorType(se.Type) {
				log.Printf("[Anthropic] mid-stream %s after text was emitted — not retrying here (would duplicate text) (conduit-31jg.46)", se.Type)
			}
			return result, streamErr
		}
		if !a.backoff(ctx, n, streamErr, 0, false, "mid-stream "+se.Type) {
			return result, streamErr
		}
	}
}
