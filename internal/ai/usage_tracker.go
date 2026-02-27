package ai

import (
	"sync"
	"time"
)

// ProviderUsageRecord tracks usage metrics for a single provider.
type ProviderUsageRecord struct {
	Provider          string    `json:"provider"`
	TotalRequests     int64     `json:"total_requests"`
	TotalInputTokens  int64     `json:"total_input_tokens"`
	TotalOutputTokens int64     `json:"total_output_tokens"`
	TotalCost         float64   `json:"total_cost"`
	TotalLatencyMs    int64     `json:"total_latency_ms"`
	LastUsed          time.Time `json:"last_used"`
	ErrorCount        int64     `json:"error_count"`
}

// ModelUsageRecord tracks usage metrics for a specific model.
type ModelUsageRecord struct {
	Model             string    `json:"model"`
	Provider          string    `json:"provider"`
	TotalRequests     int64     `json:"total_requests"`
	TotalInputTokens  int64     `json:"total_input_tokens"`
	TotalOutputTokens int64     `json:"total_output_tokens"`
	TotalCost         float64   `json:"total_cost"`
	TotalLatencyMs    int64     `json:"total_latency_ms"`
	AvgLatencyMs      float64   `json:"avg_latency_ms"`
	LastUsed          time.Time `json:"last_used"`
	ErrorCount        int64     `json:"error_count"`
}

// UsageSnapshot holds a point-in-time summary of all usage data.
type UsageSnapshot struct {
	Providers map[string]*ProviderUsageRecord `json:"providers"`
	Models    map[string]*ModelUsageRecord    `json:"models"`
	Since     time.Time                       `json:"since"`
	Snapshot  time.Time                       `json:"snapshot"`
}

// UsageTracker tracks AI provider usage metrics in memory.
// Thread-safe via mutex.
type UsageTracker struct {
	mu        sync.RWMutex
	providers map[string]*ProviderUsageRecord
	models    map[string]*ModelUsageRecord
	startTime time.Time
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
func (ut *UsageTracker) RecordUsage(provider, model string, inputTokens, outputTokens int, latencyMs int64) {
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
