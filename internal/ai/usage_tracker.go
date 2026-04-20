package ai

import (
	"sync"
	"time"
)

// ProviderUsageRecord tracks usage metrics for a single provider.
type ProviderUsageRecord struct {
	Provider              string    `json:"provider"`
	TotalRequests         int64     `json:"total_requests"`
	TotalInputTokens      int64     `json:"total_input_tokens"`
	TotalOutputTokens     int64     `json:"total_output_tokens"`
	TotalCacheWriteTokens int64     `json:"total_cache_write_tokens"`
	TotalCacheReadTokens  int64     `json:"total_cache_read_tokens"`
	CacheSavings          float64   `json:"cache_savings"`
	TotalCost             float64   `json:"total_cost"`
	TotalLatencyMs        int64     `json:"total_latency_ms"`
	LastUsed              time.Time `json:"last_used"`
	ErrorCount            int64     `json:"error_count"`
}

// ModelUsageRecord tracks usage metrics for a specific model.
type ModelUsageRecord struct {
	Model                 string    `json:"model"`
	Provider              string    `json:"provider"`
	TotalRequests         int64     `json:"total_requests"`
	TotalInputTokens      int64     `json:"total_input_tokens"`
	TotalOutputTokens     int64     `json:"total_output_tokens"`
	TotalCacheWriteTokens int64     `json:"total_cache_write_tokens"`
	TotalCacheReadTokens  int64     `json:"total_cache_read_tokens"`
	CacheHitRate          float64   `json:"cache_hit_rate"`
	TotalCost             float64   `json:"total_cost"`
	TotalLatencyMs        int64     `json:"total_latency_ms"`
	AvgLatencyMs          float64   `json:"avg_latency_ms"`
	LastUsed              time.Time `json:"last_used"`
	ErrorCount            int64     `json:"error_count"`
}

// UsageSnapshot holds a point-in-time summary of all usage data.
type UsageSnapshot struct {
	Providers map[string]*ProviderUsageRecord `json:"providers"`
	Models    map[string]*ModelUsageRecord    `json:"models"`
	Since     time.Time                       `json:"since"`
	Snapshot  time.Time                       `json:"snapshot"`
}

// UsageObserver is notified after every successful or failed RecordUsage
// / RecordError call. It is intended as a lightweight fan-out hook so
// external systems (e.g. the monitoring.TokenWindowTracker that powers the
// fuel gauge) can shadow every recording without each call site needing to
// know about them.
//
// Implementations must be safe for concurrent invocation.
type UsageObserver interface {
	OnUsage(provider, model string, inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int, latencyMs int64)
	OnError(provider, model string)
}

// UsageTracker tracks AI provider usage metrics in memory.
// Thread-safe via mutex.
type UsageTracker struct {
	mu        sync.RWMutex
	providers map[string]*ProviderUsageRecord
	models    map[string]*ModelUsageRecord
	startTime time.Time
	observer  UsageObserver
}

// NewUsageTracker creates a new usage tracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		providers: make(map[string]*ProviderUsageRecord),
		models:    make(map[string]*ModelUsageRecord),
		startTime: time.Now(),
	}
}

// RecordUsage records a successful API call's usage metrics.
func (ut *UsageTracker) RecordUsage(provider, model string, inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int, latencyMs int64) {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	now := time.Now()
	cost := CalculateCost(model, inputTokens, outputTokens)

	// Update provider record
	pr, ok := ut.providers[provider]
	if !ok {
		pr = &ProviderUsageRecord{Provider: provider}
		ut.providers[provider] = pr
	}
	pr.TotalRequests++
	pr.TotalInputTokens += int64(inputTokens)
	pr.TotalOutputTokens += int64(outputTokens)
	pr.TotalCost += cost
	pr.TotalLatencyMs += latencyMs
	pr.LastUsed = now

	// Track cache metrics for provider
	pr.TotalCacheWriteTokens += int64(cacheWriteTokens)
	pr.TotalCacheReadTokens += int64(cacheReadTokens)

	// Calculate savings (cache reads are 0.1x cost vs normal input)
	if cacheReadTokens > 0 {
		baseCost := CalculateCost(model, cacheReadTokens, 0)
		actualCost := baseCost * 0.1
		pr.CacheSavings += (baseCost - actualCost)
	}

	// Update model record
	mr, ok := ut.models[model]
	if !ok {
		mr = &ModelUsageRecord{Model: model, Provider: provider}
		ut.models[model] = mr
	}
	mr.TotalRequests++
	mr.TotalInputTokens += int64(inputTokens)
	mr.TotalOutputTokens += int64(outputTokens)
	mr.TotalCost += cost
	mr.TotalLatencyMs += latencyMs
	mr.AvgLatencyMs = float64(mr.TotalLatencyMs) / float64(mr.TotalRequests)
	mr.LastUsed = now

	// Track cache metrics for model
	mr.TotalCacheWriteTokens += int64(cacheWriteTokens)
	mr.TotalCacheReadTokens += int64(cacheReadTokens)

	// Update cache hit rate
	totalInputForRate := mr.TotalInputTokens + mr.TotalCacheWriteTokens + mr.TotalCacheReadTokens
	if totalInputForRate > 0 {
		mr.CacheHitRate = float64(mr.TotalCacheReadTokens) / float64(totalInputForRate)
	}

	// Fan out to observer (e.g. rolling-window fuel-gauge tracker). Copy
	// of the observer pointer is grabbed under the main lock; the call
	// itself is made while holding the lock which is fine because observer
	// implementations are expected to be cheap and non-reentrant.
	if ut.observer != nil {
		ut.observer.OnUsage(provider, model, inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens, latencyMs)
	}
}

// SetObserver installs an observer callback. Pass nil to clear.
// Only one observer is supported; if multiple fan-outs are needed callers
// should compose them into a single UsageObserver implementation.
func (ut *UsageTracker) SetObserver(o UsageObserver) {
	ut.mu.Lock()
	defer ut.mu.Unlock()
	ut.observer = o
}

// RecordError records an API call error.
func (ut *UsageTracker) RecordError(provider, model string) {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	pr, ok := ut.providers[provider]
	if !ok {
		pr = &ProviderUsageRecord{Provider: provider}
		ut.providers[provider] = pr
	}
	pr.ErrorCount++
	pr.TotalRequests++

	if model != "" {
		mr, ok := ut.models[model]
		if !ok {
			mr = &ModelUsageRecord{Model: model, Provider: provider}
			ut.models[model] = mr
		}
		mr.ErrorCount++
		mr.TotalRequests++
	}

	if ut.observer != nil {
		ut.observer.OnError(provider, model)
	}
}

// GetSnapshot returns a point-in-time copy of all usage data.
func (ut *UsageTracker) GetSnapshot() UsageSnapshot {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	snapshot := UsageSnapshot{
		Providers: make(map[string]*ProviderUsageRecord),
		Models:    make(map[string]*ModelUsageRecord),
		Since:     ut.startTime,
		Snapshot:  time.Now(),
	}

	for k, v := range ut.providers {
		cp := *v
		snapshot.Providers[k] = &cp
	}
	for k, v := range ut.models {
		cp := *v
		snapshot.Models[k] = &cp
	}

	return snapshot
}

// GetProviderUsage returns usage for a specific provider.
func (ut *UsageTracker) GetProviderUsage(provider string) (*ProviderUsageRecord, bool) {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	pr, ok := ut.providers[provider]
	if !ok {
		return nil, false
	}
	cp := *pr
	return &cp, true
}

// GetModelUsage returns usage for a specific model.
func (ut *UsageTracker) GetModelUsage(model string) (*ModelUsageRecord, bool) {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	mr, ok := ut.models[model]
	if !ok {
		return nil, false
	}
	cp := *mr
	return &cp, true
}

// TotalCost returns the total estimated cost across all providers.
func (ut *UsageTracker) TotalCost() float64 {
	ut.mu.RLock()
	defer ut.mu.RUnlock()

	var total float64
	for _, pr := range ut.providers {
		total += pr.TotalCost
	}
	return total
}

// Reset clears all usage data and resets the start time.
func (ut *UsageTracker) Reset() {
	ut.mu.Lock()
	defer ut.mu.Unlock()

	ut.providers = make(map[string]*ProviderUsageRecord)
	ut.models = make(map[string]*ModelUsageRecord)
	ut.startTime = time.Now()
}
