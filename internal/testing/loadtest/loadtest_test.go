package loadtest

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"conduit/internal/ai"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// delayProvider wraps ai.MockProvider and adds configurable latency.
type delayProvider struct {
	*ai.MockProvider
	delay   time.Duration
	counter atomic.Int64
}

func newDelayProvider(name string, delay time.Duration) *delayProvider {
	return &delayProvider{
		MockProvider: ai.NewMockProvider(name),
		delay:        delay,
	}
}

func (d *delayProvider) GenerateResponse(ctx context.Context, req *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	d.counter.Add(1)
	select {
	case <-time.After(d.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return d.MockProvider.GenerateResponse(ctx, req)
}

// errorProvider returns an error for every request.
type errorProvider struct {
	name string
	err  error
}

func (p *errorProvider) Name() string { return p.name }
func (p *errorProvider) GenerateResponse(_ context.Context, _ *ai.GenerateRequest) (*ai.GenerateResponse, error) {
	return nil, p.err
}

// --- LoadTestConfig tests ---

func TestLoadTestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  LoadTestConfig
		wantErr bool
	}{
		{
			name: "valid minimal config",
			config: LoadTestConfig{
				Concurrency: 1,
				Duration:    1 * time.Second,
			},
		},
		{
			name: "valid with mix",
			config: LoadTestConfig{
				Concurrency: 5,
				Duration:    10 * time.Second,
				RequestMix:  map[string]int{"simple": 70, "tool_use": 30},
			},
		},
		{
			name: "zero concurrency",
			config: LoadTestConfig{
				Concurrency: 0,
				Duration:    1 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "zero duration",
			config: LoadTestConfig{
				Concurrency: 1,
				Duration:    0,
			},
			wantErr: true,
		},
		{
			name: "negative rps",
			config: LoadTestConfig{
				Concurrency:       1,
				Duration:          1 * time.Second,
				RequestsPerSecond: -1,
			},
			wantErr: true,
		},
		{
			name: "mix does not sum to 100",
			config: LoadTestConfig{
				Concurrency: 1,
				Duration:    1 * time.Second,
				RequestMix:  map[string]int{"simple": 50, "tool_use": 40},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- LoadGenerator tests ---

func TestLoadGenerator_BasicRun(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 2,
		Duration:    200 * time.Millisecond,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())

	assert.Greater(t, result.TotalRequests, 0, "should have completed at least one request")
	assert.Equal(t, result.Successes, result.TotalRequests, "all requests should succeed")
	assert.Equal(t, 0, result.Failures)
	assert.Greater(t, result.RequestsPerSecond, 0.0)
	assert.True(t, result.P50Latency > 0)
	assert.True(t, result.P99Latency >= result.P50Latency)
	assert.True(t, result.MinLatency <= result.MaxLatency)
}

func TestLoadGenerator_WithRequestMix(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})
	lg.RegisterGenerator(&ToolUseGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 2,
		Duration:    300 * time.Millisecond,
		RequestMix:  map[string]int{"simple": 70, "tool_use": 30},
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())

	assert.Greater(t, result.TotalRequests, 0)
	// Both generators should have been used
	assert.Contains(t, result.GeneratorStats, "simple")
	assert.Contains(t, result.GeneratorStats, "tool_use")
}

func TestLoadGenerator_Stop(t *testing.T) {
	provider := newDelayProvider("test", 50*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 2,
		Duration:    10 * time.Second, // long duration
	})
	require.NoError(t, err)

	// Stop after a short delay
	go func() {
		time.Sleep(200 * time.Millisecond)
		lg.Stop()
	}()

	start := time.Now()
	result := lg.Run(context.Background())
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second, "should stop well before the 10s duration")
	assert.Greater(t, result.TotalRequests, 0)
}

func TestLoadGenerator_ContextCancellation(t *testing.T) {
	provider := newDelayProvider("test", 50*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 2,
		Duration:    10 * time.Second,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	result := lg.Run(ctx)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second)
	assert.Greater(t, result.TotalRequests, 0)
}

func TestLoadGenerator_WithErrors(t *testing.T) {
	provider := &errorProvider{name: "err", err: fmt.Errorf("connection refused")}

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 1,
		Duration:    100 * time.Millisecond,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())

	assert.Greater(t, result.TotalRequests, 0)
	assert.Equal(t, result.TotalRequests, result.Failures, "all should be failures")
	assert.Equal(t, 0, result.Successes)
	assert.Greater(t, len(result.ErrorCounts), 0)
}

func TestLoadGenerator_RampUp(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency:  4,
		Duration:     500 * time.Millisecond,
		RampUpPeriod: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())
	assert.Greater(t, result.TotalRequests, 0)
}

func TestLoadGenerator_RateLimited(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency:       1,
		Duration:          500 * time.Millisecond,
		RequestsPerSecond: 20,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())

	// With 20 RPS for 500ms, we expect roughly 10 requests (allow some tolerance)
	assert.Greater(t, result.TotalRequests, 3, "should complete at least a few requests")
	assert.Less(t, result.TotalRequests, 25, "should be rate limited to approximately 10 requests")
}

func TestLoadGenerator_MultiTurnGenerator(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&MultiTurnGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 1,
		Duration:    200 * time.Millisecond,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())
	assert.Greater(t, result.TotalRequests, 0)
	assert.Contains(t, result.GeneratorStats, "multi_turn")
}

func TestLoadGenerator_AllGenerators(t *testing.T) {
	provider := newDelayProvider("test", 1*time.Millisecond)

	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})
	lg.RegisterGenerator(&ToolUseGenerator{})
	lg.RegisterGenerator(&MultiTurnGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 3,
		Duration:    300 * time.Millisecond,
		RequestMix:  map[string]int{"simple": 40, "tool_use": 30, "multi_turn": 30},
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())
	assert.Greater(t, result.TotalRequests, 0)
	assert.Equal(t, 0, result.Failures)
}

func TestLoadGenerator_ThreadSafety(t *testing.T) {
	// Ensure the load generator is safe for concurrent access
	provider := newDelayProvider("test", 1*time.Millisecond)
	lg := NewLoadGenerator(provider)
	lg.RegisterGenerator(&SimpleChatGenerator{})

	err := lg.Configure(LoadTestConfig{
		Concurrency: 10,
		Duration:    200 * time.Millisecond,
	})
	require.NoError(t, err)

	result := lg.Run(context.Background())
	assert.Greater(t, result.TotalRequests, 0)
	assert.Equal(t, result.Successes+result.Failures, result.TotalRequests)
}

// --- Result formatting tests ---

func TestLoadTestResult_FormatSummary(t *testing.T) {
	result := &LoadTestResult{
		TotalRequests:     100,
		Successes:         95,
		Failures:          5,
		Duration:          10 * time.Second,
		P50Latency:        50 * time.Millisecond,
		P95Latency:        200 * time.Millisecond,
		P99Latency:        500 * time.Millisecond,
		MinLatency:        10 * time.Millisecond,
		MaxLatency:        800 * time.Millisecond,
		AvgLatency:        80 * time.Millisecond,
		RequestsPerSecond: 10.0,
		ErrorCounts:       map[string]int{"connection refused": 3, "timeout": 2},
		GeneratorStats: map[string]*ModelStats{
			"simple": {Requests: 70, Successes: 68, Failures: 2, TotalLatency: 4 * time.Second},
		},
		Results: generateSyntheticResults(100),
	}

	summary := result.FormatSummary()
	assert.Contains(t, summary, "Load Test Results")
	assert.Contains(t, summary, "100 requests")
	assert.Contains(t, summary, "95 (95.0%)")
	assert.Contains(t, summary, "10.00 req/s")
	assert.Contains(t, summary, "P50")
	assert.Contains(t, summary, "P95")
	assert.Contains(t, summary, "P99")
	assert.Contains(t, summary, "simple")
	assert.Contains(t, summary, "connection refused")
	assert.Contains(t, summary, "Histogram")
}

func TestLoadTestResult_FormatSummary_Empty(t *testing.T) {
	result := &LoadTestResult{
		ErrorCounts:    make(map[string]int),
		GeneratorStats: make(map[string]*ModelStats),
	}
	summary := result.FormatSummary()
	assert.Contains(t, summary, "0 requests")
}

func TestLoadTestResult_FormatHistogram(t *testing.T) {
	result := &LoadTestResult{
		Results: generateSyntheticResults(50),
	}
	hist := result.FormatHistogram()
	assert.Contains(t, hist, "Histogram")
	assert.Contains(t, hist, "#")
}

// --- Generator tests ---

func TestSimpleChatGenerator(t *testing.T) {
	gen := &SimpleChatGenerator{}
	assert.Equal(t, "simple", gen.Name())
	req := gen.Generate()
	assert.Len(t, req.Messages, 1)
	assert.Equal(t, "user", req.Messages[0].Role)
	assert.NotEmpty(t, req.Messages[0].Content)
	assert.Equal(t, 100, req.MaxTokens)
}

func TestToolUseGenerator(t *testing.T) {
	gen := &ToolUseGenerator{}
	assert.Equal(t, "tool_use", gen.Name())
	req := gen.Generate()
	assert.Len(t, req.Messages, 1)
	assert.Greater(t, len(req.Tools), 0)
	assert.Equal(t, 500, req.MaxTokens)
}

func TestMultiTurnGenerator(t *testing.T) {
	gen := &MultiTurnGenerator{}
	assert.Equal(t, "multi_turn", gen.Name())
	req := gen.Generate()
	assert.Greater(t, len(req.Messages), 1)
	assert.Equal(t, 300, req.MaxTokens)
}

func TestMixedGenerator(t *testing.T) {
	simple := &SimpleChatGenerator{}
	tool := &ToolUseGenerator{}

	mg := NewMixedGenerator(map[RequestGenerator]int{
		simple: 80,
		tool:   20,
	})
	assert.Equal(t, "mixed", mg.Name())

	// Generate many requests — should get both types
	names := make(map[string]bool)
	for i := 0; i < 100; i++ {
		req := mg.Generate()
		if len(req.Tools) > 0 {
			names["tool_use"] = true
		} else {
			names["simple"] = true
		}
	}
	assert.True(t, names["simple"], "should generate simple requests")
	assert.True(t, names["tool_use"], "should generate tool_use requests")
}

// --- Percentile tests ---

func TestPercentile(t *testing.T) {
	durations := []time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
		6 * time.Millisecond,
		7 * time.Millisecond,
		8 * time.Millisecond,
		9 * time.Millisecond,
		10 * time.Millisecond,
	}

	p50 := percentile(durations, 50)
	assert.Equal(t, 5*time.Millisecond, p50)

	p99 := percentile(durations, 99)
	assert.Equal(t, 10*time.Millisecond, p99)

	p0 := percentile(durations, 0)
	assert.Equal(t, 1*time.Millisecond, p0) // ceil(0) = 0, clamped to 0 -> index 0

	empty := percentile([]time.Duration{}, 50)
	assert.Equal(t, time.Duration(0), empty)
}

// --- helpers ---

func generateSyntheticResults(n int) []RequestResult {
	results := make([]RequestResult, n)
	base := time.Now()
	for i := 0; i < n; i++ {
		results[i] = RequestResult{
			GeneratorName: "simple",
			Latency:       time.Duration(i+1) * time.Millisecond,
			Success:       true,
			StartTime:     base.Add(time.Duration(i) * 100 * time.Millisecond),
		}
	}
	return results
}

// --- Optimizer tests ---

func TestAnalyzeResults_NoResults(t *testing.T) {
	opts := AnalyzeResults(nil)
	assert.Nil(t, opts)

	opts = AnalyzeResults(&LoadTestResult{})
	assert.Nil(t, opts)
}

func TestAnalyzeResults_HighErrorRate(t *testing.T) {
	result := &LoadTestResult{
		TotalRequests:  100,
		Successes:      70,
		Failures:       30,
		ErrorCounts:    map[string]int{"some error": 30},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(result)
	found := false
	for _, o := range opts {
		if o.Category == CategoryErrorRate {
			found = true
			assert.Contains(t, o.Description, "30.0%")
		}
	}
	assert.True(t, found, "should detect high error rate")
}

func TestAnalyzeResults_ConnectionPoolExhaustion(t *testing.T) {
	result := &LoadTestResult{
		TotalRequests:  100,
		Successes:      90,
		Failures:       10,
		ErrorCounts:    map[string]int{"connection refused: dial tcp": 10},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(result)
	found := false
	for _, o := range opts {
		if o.Category == CategoryConnectionPool {
			found = true
		}
	}
	assert.True(t, found, "should detect connection pool exhaustion")
}

func TestAnalyzeResults_RateLimitSaturation(t *testing.T) {
	result := &LoadTestResult{
		TotalRequests:  100,
		Successes:      80,
		Failures:       20,
		ErrorCounts:    map[string]int{"429 Too Many Requests": 20},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(result)
	found := false
	for _, o := range opts {
		if o.Category == CategoryRateLimit {
			found = true
			assert.Contains(t, strings.ToLower(o.Description), "rate limit")
		}
	}
	assert.True(t, found, "should detect rate limit saturation")
}

func TestAnalyzeResults_LatencySpikes(t *testing.T) {
	result := &LoadTestResult{
		TotalRequests:  100,
		Successes:      100,
		P50Latency:     50 * time.Millisecond,
		P95Latency:     800 * time.Millisecond,
		P99Latency:     6 * time.Second,
		ErrorCounts:    map[string]int{},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(result)
	latencyCount := 0
	for _, o := range opts {
		if o.Category == CategoryLatency {
			latencyCount++
		}
	}
	assert.GreaterOrEqual(t, latencyCount, 1, "should detect at least one latency issue")
}

func TestAnalyzeResults_MemoryPressure(t *testing.T) {
	// Build results where the last quarter is much slower than the first
	n := 100
	results := make([]RequestResult, n)
	base := time.Now()
	for i := 0; i < n; i++ {
		latency := 10 * time.Millisecond // fast early on
		if i >= n*3/4 {
			latency = 100 * time.Millisecond // slow later
		}
		results[i] = RequestResult{
			GeneratorName: "simple",
			Latency:       latency,
			Success:       true,
			StartTime:     base.Add(time.Duration(i) * 50 * time.Millisecond),
		}
	}

	lr := &LoadTestResult{
		TotalRequests:  n,
		Successes:      n,
		Results:        results,
		ErrorCounts:    map[string]int{},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(lr)
	found := false
	for _, o := range opts {
		if o.Category == CategoryMemory {
			found = true
		}
	}
	assert.True(t, found, "should detect memory pressure / latency degradation")
}

func TestAnalyzeResults_SortedByImpact(t *testing.T) {
	// Create results that trigger multiple optimizations
	result := &LoadTestResult{
		TotalRequests:  100,
		Successes:      60,
		Failures:       40,
		P50Latency:     50 * time.Millisecond,
		P95Latency:     800 * time.Millisecond,
		P99Latency:     6 * time.Second,
		ErrorCounts:    map[string]int{"connection refused": 20, "429 Too Many Requests": 20},
		GeneratorStats: map[string]*ModelStats{},
	}
	opts := AnalyzeResults(result)
	assert.Greater(t, len(opts), 1, "should have multiple optimizations")

	// Verify sorted by impact
	impactOrder := map[ImpactLevel]int{
		ImpactCritical: 0,
		ImpactHigh:     1,
		ImpactMedium:   2,
		ImpactLow:      3,
	}
	for i := 1; i < len(opts); i++ {
		assert.LessOrEqual(t, impactOrder[opts[i-1].Impact], impactOrder[opts[i].Impact],
			"optimizations should be sorted by impact severity")
	}
}

func TestContainsCI(t *testing.T) {
	assert.True(t, containsCI("Connection Refused", "connection refused"))
	assert.True(t, containsCI("429 Too Many Requests", "429"))
	assert.True(t, containsCI("RATE LIMIT exceeded", "rate limit"))
	assert.False(t, containsCI("success", "error"))
	assert.False(t, containsCI("", "something"))
}
