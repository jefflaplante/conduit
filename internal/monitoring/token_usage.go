// Package monitoring — token_usage.go provides an in-memory rolling-window
// counter for AI token consumption. The counter is used by the fuel-gauge API
// surfaced on the gateway so an agent can self-inspect how much of its
// per-hour / per-day budget it has spent.
//
// Design notes:
//   - Rolling hour: 60 per-minute buckets in a ring, indexed by
//     time.Now().Unix() / 60 % 60. Each Record() clears the minute bucket it
//     lands in when that bucket belongs to a prior hour, then accumulates.
//   - Rolling day: 24 per-hour buckets, same pattern.
//   - All token counts are aggregated (input + output). Cache read/write tokens
//     are tracked separately so a caller can distinguish "raw traffic" from
//     "billable input". No persistence — values reset on process restart.
//   - Thread-safe via a single RWMutex; expected traffic (a few calls/sec at
//     peak) makes a more elaborate lock-free structure unnecessary.
//
// This package intentionally does not import internal/ai to avoid a cycle;
// the ai.Router is wired via a thin observer callback in router.go.
package monitoring

import (
	"sync"
	"time"
)

const (
	tokenMinuteBuckets = 60 // rolling hour = 60 minute buckets
	tokenHourBuckets   = 24 // rolling day  = 24 hour  buckets
)

// minuteBucket aggregates counts for a single minute slot.
type minuteBucket struct {
	// minuteKey is the Unix minute (time.Unix() / 60) this bucket last
	// accumulated into. Used to detect stale data when the ring wraps.
	minuteKey    int64
	requests     int64
	inputTokens  int64
	outputTokens int64
	cacheRead    int64
	cacheWrite   int64
	errors       int64
}

// hourBucket aggregates counts for a single hour slot.
type hourBucket struct {
	hourKey      int64
	requests     int64
	inputTokens  int64
	outputTokens int64
	cacheRead    int64
	cacheWrite   int64
	errors       int64
}

// TokenWindowTracker records API token usage in rolling hour and day windows.
// Zero value is not usable; construct with NewTokenWindowTracker.
type TokenWindowTracker struct {
	mu sync.RWMutex

	minutes [tokenMinuteBuckets]minuteBucket
	hours   [tokenHourBuckets]hourBucket

	createdAt time.Time
	// now is a hook for tests; defaults to time.Now.
	now func() time.Time
}

// NewTokenWindowTracker creates an empty rolling-window tracker.
func NewTokenWindowTracker() *TokenWindowTracker {
	return &TokenWindowTracker{
		createdAt: time.Now(),
		now:       time.Now,
	}
}

// setClock overrides the clock for tests.
func (t *TokenWindowTracker) setClock(fn func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = fn
}

// Record observes one API call's token usage. inputTokens, outputTokens,
// cacheReadTokens, cacheWriteTokens are counts from the provider usage
// response. Zero values are allowed. A negative value is treated as 0.
func (t *TokenWindowTracker) Record(inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int) {
	in := clampNonNeg(inputTokens)
	out := clampNonNeg(outputTokens)
	cr := clampNonNeg(cacheReadTokens)
	cw := clampNonNeg(cacheWriteTokens)

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	minKey := now.Unix() / 60
	hourKey := now.Unix() / 3600

	mIdx := int(minKey % tokenMinuteBuckets)
	if t.minutes[mIdx].minuteKey != minKey {
		t.minutes[mIdx] = minuteBucket{minuteKey: minKey}
	}
	t.minutes[mIdx].requests++
	t.minutes[mIdx].inputTokens += int64(in)
	t.minutes[mIdx].outputTokens += int64(out)
	t.minutes[mIdx].cacheRead += int64(cr)
	t.minutes[mIdx].cacheWrite += int64(cw)

	hIdx := int(hourKey % tokenHourBuckets)
	if t.hours[hIdx].hourKey != hourKey {
		t.hours[hIdx] = hourBucket{hourKey: hourKey}
	}
	t.hours[hIdx].requests++
	t.hours[hIdx].inputTokens += int64(in)
	t.hours[hIdx].outputTokens += int64(out)
	t.hours[hIdx].cacheRead += int64(cr)
	t.hours[hIdx].cacheWrite += int64(cw)
}

// OnUsage implements ai.UsageObserver. Arguments mirror UsageTracker.RecordUsage
// so the tracker can be wired as a direct observer.
func (t *TokenWindowTracker) OnUsage(provider, model string, inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int, latencyMs int64) {
	_ = provider
	_ = model
	_ = latencyMs
	t.Record(inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens)
}

// OnError implements ai.UsageObserver.
func (t *TokenWindowTracker) OnError(provider, model string) {
	_ = provider
	_ = model
	t.RecordError()
}

// RecordError observes one failed API call; tokens counted as zero, errors
// incremented in both windows.
func (t *TokenWindowTracker) RecordError() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	minKey := now.Unix() / 60
	hourKey := now.Unix() / 3600

	mIdx := int(minKey % tokenMinuteBuckets)
	if t.minutes[mIdx].minuteKey != minKey {
		t.minutes[mIdx] = minuteBucket{minuteKey: minKey}
	}
	t.minutes[mIdx].errors++
	t.minutes[mIdx].requests++

	hIdx := int(hourKey % tokenHourBuckets)
	if t.hours[hIdx].hourKey != hourKey {
		t.hours[hIdx] = hourBucket{hourKey: hourKey}
	}
	t.hours[hIdx].errors++
	t.hours[hIdx].requests++
}

// TokenWindowSnapshot is a JSON-friendly view of rolling window counts.
type TokenWindowSnapshot struct {
	Requests     int64 `json:"requests"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CacheRead    int64 `json:"cache_read_tokens"`
	CacheWrite   int64 `json:"cache_write_tokens"`
	Errors       int64 `json:"errors"`
}

// Total returns input + output + cache tokens combined.
func (s TokenWindowSnapshot) Total() int64 {
	return s.InputTokens + s.OutputTokens + s.CacheRead + s.CacheWrite
}

// TokenUsageSnapshot is a point-in-time report of rolling token consumption.
type TokenUsageSnapshot struct {
	Hour      TokenWindowSnapshot `json:"hour"`
	Day       TokenWindowSnapshot `json:"day"`
	CreatedAt time.Time           `json:"tracker_created_at"`
	TakenAt   time.Time           `json:"taken_at"`
}

// Snapshot returns the current rolling-hour and rolling-day totals.
// Stale buckets (those with keys older than the current window) are skipped.
func (t *TokenWindowTracker) Snapshot() TokenUsageSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := t.now()
	currentMin := now.Unix() / 60
	currentHour := now.Unix() / 3600

	// Hour window: sum minute buckets where minuteKey is within the last 60.
	var hour TokenWindowSnapshot
	for i := 0; i < tokenMinuteBuckets; i++ {
		b := t.minutes[i]
		// Empty / uninitialised bucket.
		if b.minuteKey == 0 {
			continue
		}
		// A bucket is "live" if it's within the last 60 minutes (inclusive of
		// the current minute). Older buckets are ring-recycled data from a
		// previous hour.
		if b.minuteKey <= currentMin && b.minuteKey > currentMin-tokenMinuteBuckets {
			hour.Requests += b.requests
			hour.InputTokens += b.inputTokens
			hour.OutputTokens += b.outputTokens
			hour.CacheRead += b.cacheRead
			hour.CacheWrite += b.cacheWrite
			hour.Errors += b.errors
		}
	}

	// Day window: sum hour buckets where hourKey is within the last 24.
	var day TokenWindowSnapshot
	for i := 0; i < tokenHourBuckets; i++ {
		b := t.hours[i]
		if b.hourKey == 0 {
			continue
		}
		if b.hourKey <= currentHour && b.hourKey > currentHour-tokenHourBuckets {
			day.Requests += b.requests
			day.InputTokens += b.inputTokens
			day.OutputTokens += b.outputTokens
			day.CacheRead += b.cacheRead
			day.CacheWrite += b.cacheWrite
			day.Errors += b.errors
		}
	}

	return TokenUsageSnapshot{
		Hour:      hour,
		Day:       day,
		CreatedAt: t.createdAt,
		TakenAt:   now,
	}
}

// Reset clears all rolling-window data. Primarily for tests.
func (t *TokenWindowTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.minutes = [tokenMinuteBuckets]minuteBucket{}
	t.hours = [tokenHourBuckets]hourBucket{}
	t.createdAt = t.now()
}

func clampNonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
