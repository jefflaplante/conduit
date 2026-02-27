// Package loadtest provides a load testing framework for the conduit gateway.
// It generates configurable request loads against AI providers using mock or real
// backends, measures latency percentiles and throughput, and supports multiple
// request generation strategies.
package loadtest

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"conduit/internal/ai"
)

// RequestGenerator defines a pluggable interface for generating test requests.
// Implementations produce different kinds of AI requests to simulate realistic
// load patterns (simple chat, tool use, multi-turn conversations, etc.).
type RequestGenerator interface {
	// Name returns a human-readable name for this generator.
	Name() string
	// Generate produces a new GenerateRequest for load testing.
	Generate() *ai.GenerateRequest
}

// LoadTestConfig holds all configuration for a load test run.
type LoadTestConfig struct {
	// Concurrency is the number of parallel virtual users.
	Concurrency int
	// Duration is the total test duration.
	Duration time.Duration
	// RequestsPerSecond is the target RPS (0 = unlimited).
	RequestsPerSecond int
	// RampUpPeriod is the time to linearly ramp from 0 to full concurrency.
	RampUpPeriod time.Duration
	// RequestMix maps generator names to their percentage weight (must sum to 100).
	RequestMix map[string]int
}

// Validate checks that the configuration is internally consistent.
func (c *LoadTestConfig) Validate() error {
	if c.Concurrency < 1 {
		return fmt.Errorf("concurrency must be >= 1, got %d", c.Concurrency)
	}
	if c.Duration <= 0 {
		return fmt.Errorf("duration must be > 0, got %v", c.Duration)
	}
	if c.RequestsPerSecond < 0 {
		return fmt.Errorf("requests per second must be >= 0, got %d", c.RequestsPerSecond)
	}
	if len(c.RequestMix) > 0 {
		total := 0
		for _, pct := range c.RequestMix {
			if pct < 0 {
				return fmt.Errorf("request mix percentage must be >= 0")
			}
			total += pct
		}
		if total != 100 {
			return fmt.Errorf("request mix percentages must sum to 100, got %d", total)
		}
	}
	return nil
}

// RequestResult captures the outcome of a single request.
type RequestResult struct {
	GeneratorName string
	Latency       time.Duration
	Success       bool
	Error         error
	StartTime     time.Time
	Usage         ai.Usage
}

// ModelStats holds per-model statistics gathered during a load test.
type ModelStats struct {
	Requests     int
	Successes    int
	Failures     int
	TotalLatency time.Duration
	MinLatency   time.Duration
	MaxLatency   time.Duration
}

// LoadTestResult contains the aggregated results of a load test run.
type LoadTestResult struct {
	// Summary metrics
	TotalRequests int
	Successes     int
	Failures      int
	Duration      time.Duration

	// Latency percentiles
	P50Latency time.Duration
	P95Latency time.Duration
	P99Latency time.Duration
	MinLatency time.Duration
	MaxLatency time.Duration
	AvgLatency time.Duration

	// Throughput
	RequestsPerSecond float64

	// Error breakdown
	ErrorCounts map[string]int

	// Per-generator statistics
	GeneratorStats map[string]*ModelStats

	// Raw results for further analysis
	Results []RequestResult
}

// LoadGenerator orchestrates the execution of load tests. It is safe for
// concurrent use from multiple goroutines.
type LoadGenerator struct {
	mu         sync.RWMutex
	config     LoadTestConfig
	provider   ai.Provider
	generators map[string]RequestGenerator
	stopped    atomic.Bool
	stopCh     chan struct{}
}

// NewLoadGenerator creates a new LoadGenerator that will send requests to the
// given AI provider.
func NewLoadGenerator(provider ai.Provider) *LoadGenerator {
	return &LoadGenerator{
		provider:   provider,
		generators: make(map[string]RequestGenerator),
		stopCh:     make(chan struct{}),
	}
}

// Configure sets the load test configuration. Must be called before Run.
func (lg *LoadGenerator) Configure(config LoadTestConfig) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.config = config
	return nil
}

// RegisterGenerator adds a request generator that can be used in the request mix.
func (lg *LoadGenerator) RegisterGenerator(gen RequestGenerator) {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.generators[gen.Name()] = gen
}

// Stop signals the load test to terminate early. Safe to call from any goroutine.
func (lg *LoadGenerator) Stop() {
	if lg.stopped.CompareAndSwap(false, true) {
		close(lg.stopCh)
	}
}

// Run executes the load test and returns aggregated results. The context can
// be used for cancellation in addition to Stop().
func (lg *LoadGenerator) Run(ctx context.Context) *LoadTestResult {
	lg.mu.RLock()
	config := lg.config
	generators := lg.generators
	lg.mu.RUnlock()

	// Build weighted generator picker
	picker := buildGeneratorPicker(config.RequestMix, generators)
	if picker == nil {
		// No mix specified or no generators: use all generators equally
		picker = buildEqualPicker(generators)
	}

	// Results collection
	resultsCh := make(chan RequestResult, config.Concurrency*100)
	var wg sync.WaitGroup

	// Rate limiter (if RPS target is set)
	var rateLimiter <-chan time.Time
	if config.RequestsPerSecond > 0 {
		ticker := time.NewTicker(time.Second / time.Duration(config.RequestsPerSecond))
		defer ticker.Stop()
		rateLimiter = ticker.C
	}

	// Create a merged context that respects both ctx and Stop()
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	go func() {
		select {
		case <-lg.stopCh:
			runCancel()
		case <-runCtx.Done():
		}
	}()

	// Timer for overall duration
	deadline := time.After(config.Duration)
	start := time.Now()

	// Launch virtual users with ramp-up
	launchInterval := time.Duration(0)
	if config.RampUpPeriod > 0 && config.Concurrency > 1 {
		launchInterval = config.RampUpPeriod / time.Duration(config.Concurrency)
	}

	for i := 0; i < config.Concurrency; i++ {
		// Ramp-up delay
		if launchInterval > 0 && i > 0 {
			select {
			case <-time.After(launchInterval):
			case <-runCtx.Done():
				break
			}
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			lg.virtualUser(runCtx, picker, rateLimiter, resultsCh)
		}()
	}

	// Wait for deadline or cancellation
	select {
	case <-deadline:
		runCancel()
	case <-runCtx.Done():
	}

	// Wait for all virtual users to finish
	wg.Wait()
	close(resultsCh)

	elapsed := time.Since(start)

	// Collect all results
	var results []RequestResult
	for r := range resultsCh {
		results = append(results, r)
	}

	return aggregateResults(results, elapsed)
}

// virtualUser simulates a single concurrent user sending requests in a loop.
func (lg *LoadGenerator) virtualUser(ctx context.Context, picker *generatorPicker, rateLimiter <-chan time.Time, results chan<- RequestResult) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Rate limiting
		if rateLimiter != nil {
			select {
			case <-rateLimiter:
			case <-ctx.Done():
				return
			}
		}

		gen := picker.pick()
		if gen == nil {
			return
		}

		req := gen.Generate()
		startTime := time.Now()
		resp, err := lg.provider.GenerateResponse(ctx, req)
		latency := time.Since(startTime)

		result := RequestResult{
			GeneratorName: gen.Name(),
			Latency:       latency,
			StartTime:     startTime,
			Success:       err == nil,
			Error:         err,
		}
		if resp != nil {
			result.Usage = resp.Usage
		}

		// Don't count context cancellation as a real error
		if ctx.Err() != nil {
			return
		}

		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
	}
}

// aggregateResults computes summary statistics from raw request results.
func aggregateResults(results []RequestResult, elapsed time.Duration) *LoadTestResult {
	lr := &LoadTestResult{
		TotalRequests:  len(results),
		Duration:       elapsed,
		ErrorCounts:    make(map[string]int),
		GeneratorStats: make(map[string]*ModelStats),
		Results:        results,
	}

	if len(results) == 0 {
		return lr
	}

	// Collect latencies for percentile calculation
	latencies := make([]time.Duration, 0, len(results))
	var totalLatency time.Duration

	for _, r := range results {
		if r.Success {
			lr.Successes++
		} else {
			lr.Failures++
			if r.Error != nil {
				errKey := r.Error.Error()
				// Truncate long error messages for grouping
				if len(errKey) > 100 {
					errKey = errKey[:100] + "..."
				}
				lr.ErrorCounts[errKey]++
			}
		}

		latencies = append(latencies, r.Latency)
		totalLatency += r.Latency

		// Per-generator stats
		gs, ok := lr.GeneratorStats[r.GeneratorName]
		if !ok {
			gs = &ModelStats{
				MinLatency: r.Latency,
				MaxLatency: r.Latency,
			}
			lr.GeneratorStats[r.GeneratorName] = gs
		}
		gs.Requests++
		gs.TotalLatency += r.Latency
		if r.Success {
			gs.Successes++
		} else {
			gs.Failures++
		}
		if r.Latency < gs.MinLatency {
			gs.MinLatency = r.Latency
		}
		if r.Latency > gs.MaxLatency {
			gs.MaxLatency = r.Latency
		}
	}

	// Sort latencies for percentiles
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	lr.MinLatency = latencies[0]
	lr.MaxLatency = latencies[len(latencies)-1]
	lr.AvgLatency = totalLatency / time.Duration(len(latencies))
	lr.P50Latency = percentile(latencies, 50)
	lr.P95Latency = percentile(latencies, 95)
	lr.P99Latency = percentile(latencies, 99)

	// Throughput
	if elapsed > 0 {
		lr.RequestsPerSecond = float64(lr.TotalRequests) / elapsed.Seconds()
	}

	return lr
}

// percentile returns the p-th percentile value from a sorted duration slice.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(float64(p)/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// FormatSummary returns a human-readable summary of the load test results.
func (lr *LoadTestResult) FormatSummary() string {
	var b strings.Builder

	b.WriteString("=== Load Test Results ===\n\n")
	fmt.Fprintf(&b, "Duration:    %v\n", lr.Duration.Round(time.Millisecond))
	fmt.Fprintf(&b, "Total:       %d requests\n", lr.TotalRequests)
	fmt.Fprintf(&b, "Successes:   %d (%.1f%%)\n", lr.Successes, safePercent(lr.Successes, lr.TotalRequests))
	fmt.Fprintf(&b, "Failures:    %d (%.1f%%)\n", lr.Failures, safePercent(lr.Failures, lr.TotalRequests))
	fmt.Fprintf(&b, "Throughput:  %.2f req/s\n\n", lr.RequestsPerSecond)

	b.WriteString("--- Latency ---\n")
	fmt.Fprintf(&b, "Min:  %v\n", lr.MinLatency.Round(time.Microsecond))
	fmt.Fprintf(&b, "Avg:  %v\n", lr.AvgLatency.Round(time.Microsecond))
	fmt.Fprintf(&b, "P50:  %v\n", lr.P50Latency.Round(time.Microsecond))
	fmt.Fprintf(&b, "P95:  %v\n", lr.P95Latency.Round(time.Microsecond))
	fmt.Fprintf(&b, "P99:  %v\n", lr.P99Latency.Round(time.Microsecond))
	fmt.Fprintf(&b, "Max:  %v\n\n", lr.MaxLatency.Round(time.Microsecond))

	if len(lr.GeneratorStats) > 0 {
		b.WriteString("--- Per-Generator ---\n")
		for name, gs := range lr.GeneratorStats {
			avg := time.Duration(0)
			if gs.Requests > 0 {
				avg = gs.TotalLatency / time.Duration(gs.Requests)
			}
			fmt.Fprintf(&b, "  %s: %d req, %d ok, %d err, avg %v\n",
				name, gs.Requests, gs.Successes, gs.Failures, avg.Round(time.Microsecond))
		}
		b.WriteString("\n")
	}

	if len(lr.ErrorCounts) > 0 {
		b.WriteString("--- Errors ---\n")
		for errMsg, count := range lr.ErrorCounts {
			fmt.Fprintf(&b, "  [%d] %s\n", count, errMsg)
		}
		b.WriteString("\n")
	}

	// Latency histogram
	b.WriteString(lr.FormatHistogram())

	return b.String()
}

// FormatHistogram returns an ASCII histogram of latency distribution.
func (lr *LoadTestResult) FormatHistogram() string {
	if len(lr.Results) == 0 {
		return ""
	}

	// Build histogram buckets
	latencies := make([]time.Duration, 0, len(lr.Results))
	for _, r := range lr.Results {
		latencies = append(latencies, r.Latency)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	const numBuckets = 10
	minLat := latencies[0]
	maxLat := latencies[len(latencies)-1]
	bucketWidth := (maxLat - minLat) / time.Duration(numBuckets)
	if bucketWidth == 0 {
		bucketWidth = time.Microsecond
	}

	buckets := make([]int, numBuckets)
	for _, lat := range latencies {
		idx := int((lat - minLat) / bucketWidth)
		if idx >= numBuckets {
			idx = numBuckets - 1
		}
		buckets[idx]++
	}

	// Find max count for scaling
	maxCount := 0
	for _, c := range buckets {
		if c > maxCount {
			maxCount = c
		}
	}

	var b strings.Builder
	b.WriteString("--- Latency Histogram ---\n")
	const barWidth = 40
	for i, count := range buckets {
		low := minLat + time.Duration(i)*bucketWidth
		high := low + bucketWidth
		barLen := 0
		if maxCount > 0 {
			barLen = count * barWidth / maxCount
		}
		bar := strings.Repeat("#", barLen)
		fmt.Fprintf(&b, "  %8v - %8v [%4d] %s\n",
			low.Round(time.Microsecond), high.Round(time.Microsecond), count, bar)
	}

	return b.String()
}

func safePercent(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

// --- Built-in request generators ---

// SimpleChatGenerator produces simple single-message chat requests.
type SimpleChatGenerator struct{}

func (g *SimpleChatGenerator) Name() string { return "simple" }
func (g *SimpleChatGenerator) Generate() *ai.GenerateRequest {
	prompts := []string{
		"Hello, how are you?",
		"What is the weather today?",
		"Tell me a joke.",
		"What time is it?",
		"Say hello.",
	}
	return &ai.GenerateRequest{
		Messages: []ai.ChatMessage{
			{Role: "user", Content: prompts[rand.Intn(len(prompts))]},
		},
		MaxTokens: 100,
	}
}

// ToolUseGenerator produces requests that include tool definitions.
type ToolUseGenerator struct{}

func (g *ToolUseGenerator) Name() string { return "tool_use" }
func (g *ToolUseGenerator) Generate() *ai.GenerateRequest {
	prompts := []string{
		"Search the web for Go performance tips.",
		"Read the file at /tmp/test.txt.",
		"Run the command ls -la.",
	}
	return &ai.GenerateRequest{
		Messages: []ai.ChatMessage{
			{Role: "user", Content: prompts[rand.Intn(len(prompts))]},
		},
		Tools: []ai.Tool{
			{
				Name:        "WebSearch",
				Description: "Search the web",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string"},
					},
				},
			},
			{
				Name:        "Read",
				Description: "Read a file",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
		MaxTokens: 500,
	}
}

// MultiTurnGenerator produces multi-turn conversation requests.
type MultiTurnGenerator struct{}

func (g *MultiTurnGenerator) Name() string { return "multi_turn" }
func (g *MultiTurnGenerator) Generate() *ai.GenerateRequest {
	conversations := [][]ai.ChatMessage{
		{
			{Role: "user", Content: "What is Go?"},
			{Role: "assistant", Content: "Go is a statically typed, compiled programming language."},
			{Role: "user", Content: "What are goroutines?"},
		},
		{
			{Role: "user", Content: "Explain HTTP."},
			{Role: "assistant", Content: "HTTP is Hypertext Transfer Protocol, used for web communication."},
			{Role: "user", Content: "What about HTTP/2?"},
		},
		{
			{Role: "user", Content: "What is SQLite?"},
			{Role: "assistant", Content: "SQLite is a lightweight embedded database."},
			{Role: "user", Content: "How does WAL mode work?"},
			{Role: "assistant", Content: "WAL (Write-Ahead Logging) allows concurrent reads during writes."},
			{Role: "user", Content: "What are its drawbacks?"},
		},
	}
	return &ai.GenerateRequest{
		Messages:  conversations[rand.Intn(len(conversations))],
		MaxTokens: 300,
	}
}

// MixedGenerator delegates to other generators based on a weighted random selection.
type MixedGenerator struct {
	generators  []RequestGenerator
	weights     []int
	totalWeight int
}

func NewMixedGenerator(gens map[RequestGenerator]int) *MixedGenerator {
	mg := &MixedGenerator{}
	for g, w := range gens {
		mg.generators = append(mg.generators, g)
		mg.weights = append(mg.weights, w)
		mg.totalWeight += w
	}
	return mg
}

func (g *MixedGenerator) Name() string { return "mixed" }
func (g *MixedGenerator) Generate() *ai.GenerateRequest {
	r := rand.Intn(g.totalWeight)
	cumulative := 0
	for i, w := range g.weights {
		cumulative += w
		if r < cumulative {
			return g.generators[i].Generate()
		}
	}
	return g.generators[len(g.generators)-1].Generate()
}

// --- Internal helpers ---

// generatorPicker picks generators according to a weighted distribution.
type generatorPicker struct {
	generators  []RequestGenerator
	weights     []int
	totalWeight int
}

func (p *generatorPicker) pick() RequestGenerator {
	if len(p.generators) == 0 {
		return nil
	}
	r := rand.Intn(p.totalWeight)
	cumulative := 0
	for i, w := range p.weights {
		cumulative += w
		if r < cumulative {
			return p.generators[i]
		}
	}
	return p.generators[len(p.generators)-1]
}

func buildGeneratorPicker(mix map[string]int, generators map[string]RequestGenerator) *generatorPicker {
	if len(mix) == 0 {
		return nil
	}
	p := &generatorPicker{}
	for name, pct := range mix {
		gen, ok := generators[name]
		if !ok {
			continue
		}
		p.generators = append(p.generators, gen)
		p.weights = append(p.weights, pct)
		p.totalWeight += pct
	}
	if len(p.generators) == 0 {
		return nil
	}
	return p
}

func buildEqualPicker(generators map[string]RequestGenerator) *generatorPicker {
	if len(generators) == 0 {
		return nil
	}
	p := &generatorPicker{}
	for _, gen := range generators {
		p.generators = append(p.generators, gen)
		p.weights = append(p.weights, 1)
		p.totalWeight++
	}
	return p
}
