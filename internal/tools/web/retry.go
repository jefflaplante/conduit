package web

// Retry/backoff for rate-limited web tools (conduit-3czd, 2026-10-20).
//
// The Brave Search API free tier enforces ~1 req/s. Conduit's agents are
// bursty: a failed WebSearch triggers a follow-up retry, or a sub-agent
// fires a few searches in a row, and we exceed 1 QPS for a second. The
// tools had no retry logic, so a single 429 turned into a hard tool error
// and the calling agent flailed instead of waiting.
//
// Pattern mirrors internal/ai/anthropic_retry.go (conduit-31jg.46):
// bounded retries, equal jitter, honoring Retry-After, and never sleeping
// past the caller's ctx deadline (with a small attempt budget so the
// retry actually has time to run).

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// webRetryPolicy bounds retries for web API tools.
type webRetryPolicy struct {
	maxRetries       int           // retries after the first attempt
	baseDelay        time.Duration // first backoff step (doubles per retry)
	maxDelay         time.Duration // cap for backoff AND for honoured retry-after
	minAttemptBudget time.Duration // ctx time that must remain after the sleep
}

// webDefaultRetryPolicy: 3 retries, 500ms → 1s → 2s (jittered, capped 15s).
// A 429 retry costs at most ~3.5s on top of the request itself — cheap
// insurance against a hard tool failure. minAttemptBudget is small because
// a Brave GET is fast; if the caller's deadline is nearly exhausted we
// surface the original error instead of a doomed retry.
var webDefaultRetryPolicy = webRetryPolicy{
	maxRetries:       3,
	baseDelay:        500 * time.Millisecond,
	maxDelay:         15 * time.Second,
	minAttemptBudget: 3 * time.Second,
}

// errWebRetryBudget reports that the ctx deadline leaves no room for a retry.
var errWebRetryBudget = errors.New("retry skipped: not enough time left before the caller's deadline")

// delay returns the backoff before retry number n (1-based). A server-sent
// Retry-After wins over computed backoff; one longer than maxDelay is not
// honoured by sleeping — ok=false, so the caller surfaces the error (rate
// limit resets measured in minutes are not worth blocking a tool call).
func (p webRetryPolicy) delay(n int, retryAfter time.Duration, hasRetryAfter bool) (time.Duration, bool) {
	if hasRetryAfter {
		if retryAfter <= 0 || retryAfter > p.maxDelay {
			return 0, false
		}
		return retryAfter, true
	}
	if n < 1 {
		n = 1 // delay is 1-based; guard against shift-panic on caller error
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
func (p webRetryPolicy) sleep(ctx context.Context, d time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < d+p.minAttemptBudget {
		return errWebRetryBudget
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

// parseRetryAfter reads retry-after-ms, then Retry-After (delta-seconds or
// HTTP-date). Adapted from internal/ai/anthropic_retry.go.
func parseRetryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
			return time.Duration(ms * float64(time.Millisecond)), true
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
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

// webRetryableError marks an HTTP failure worth retrying (429, transient
// 5xx). It carries the server's Retry-After hint so the policy can honor it.
type webRetryableError struct {
	status        int
	retryAfter    time.Duration
	hasRetryAfter bool
	err           error // message surfaced to the agent if all retries fail
}

func (e *webRetryableError) Error() string { return e.err.Error() }

// shouldRetryStatus reports whether an HTTP status is worth retrying.
// 429 is the headline case (~1 req/s free tier); the 5xx set matches the
// Anthropic retry policy.
func shouldRetryStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
