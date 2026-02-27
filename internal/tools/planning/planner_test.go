package planning

import (
	"context"
	"testing"
	"time"

	"conduit/internal/ai"
)

func TestExecutionPlanner_PlanExecution(t *testing.T) {
	// Create test dependencies
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	// Test tool calls
	toolCalls := []ai.ToolCall{
		{
			ID:   "call_1",
			Name: "web_search",
			Args: map[string]interface{}{
				"query": "test query",
				"count": 5,
			},
		},
		{
			ID:   "call_2",
			Name: "web_fetch",
			Args: map[string]interface{}{
				"url": "https://example.com",
			},
		},
		{
			ID:   "call_3",
			Name: "memory_search",
			Args: map[string]interface{}{
				"query": "related information",
			},
		},
	}

	ctx := context.Background()
	plan, err := planner.PlanExecution(ctx, toolCalls)

	if err != nil {
		t.Fatalf("PlanExecution failed: %v", err)
	}

	if plan == nil {
		t.Fatal("Plan is nil")
	}

	// Verify plan properties
	if len(plan.Steps) != len(toolCalls) {
		t.Errorf("Expected %d steps, got %d", len(toolCalls), len(plan.Steps))
	}

	if plan.ID == "" {
		t.Error("Plan ID is empty")
	}

	if plan.OptimizedFor != string(StrategyBalanced) {
		t.Errorf("Expected optimization strategy %s, got %s", StrategyBalanced, plan.OptimizedFor)
	}

	// Verify parallel groups exist
	if len(plan.Parallel) == 0 {
		t.Error("No parallel groups created")
	}

	// Verify estimated metrics
	if plan.Estimated.Duration <= 0 {
		t.Error("Estimated duration should be positive")
	}

	if plan.Estimated.Reliability < 0 || plan.Estimated.Reliability > 1 {
		t.Errorf("Reliability should be between 0 and 1, got %f", plan.Estimated.Reliability)
	}
}

func TestExecutionPlanner_DifferentStrategies(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()

	toolCalls := []ai.ToolCall{
		{ID: "1", Name: "web_search", Args: map[string]interface{}{"query": "test"}},
		{ID: "2", Name: "web_fetch", Args: map[string]interface{}{"url": "https://example.com"}},
	}

	strategies := []PlanningStrategy{
		StrategySpeed,
		StrategyReliability,
		StrategyCost,
		StrategyBalanced,
	}

	ctx := context.Background()

	for _, strategy := range strategies {
		optimizer := NewExecutionOptimizer(strategy, 5)
		planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

		plan, err := planner.PlanExecution(ctx, toolCalls)
		if err != nil {
			t.Errorf("Strategy %s failed: %v", strategy, err)
			continue
		}

		if plan.OptimizedFor != string(strategy) {
			t.Errorf("Expected strategy %s, got %s", strategy, plan.OptimizedFor)
		}
	}
}

func TestExecutionPlanner_ToolProfiles(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	// Test getting tool profiles
	profiles := planner.GetToolProfiles()
	if len(profiles) == 0 {
		t.Error("No tool profiles found")
	}

	// Check specific tool profiles
	webSearchProfile := profiles["web_search"]
	if webSearchProfile == nil {
		t.Error("web_search profile not found")
	} else {
		if webSearchProfile.AverageLatency <= 0 {
			t.Error("web_search latency should be positive")
		}
		if webSearchProfile.SuccessRate <= 0 || webSearchProfile.SuccessRate > 1 {
			t.Error("web_search success rate should be between 0 and 1")
		}
	}

	// Test updating tool profile
	customProfile := &ToolProfile{
		Name:            "custom_tool",
		AverageLatency:  time.Millisecond * 500,
		SuccessRate:     0.99,
		CostPerCall:     0.001,
		CacheCompatible: true,
		ParallelSafe:    true,
		MaxRetries:      2,
		TimeoutDuration: time.Second * 5,
		Complexity:      0.3,
	}

	planner.UpdateToolProfile("custom_tool", customProfile)

	updatedProfiles := planner.GetToolProfiles()
	if updatedProfiles["custom_tool"] == nil {
		t.Error("Custom tool profile not added")
	}
}

func TestExecutionPlanner_EmptyToolCalls(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	ctx := context.Background()
	plan, err := planner.PlanExecution(ctx, []ai.ToolCall{})

	if err == nil {
		t.Error("Expected error for empty tool calls")
	}

	if plan != nil {
		t.Error("Plan should be nil for empty tool calls")
	}
}

func TestExecutionPlanner_SingleToolCall(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	toolCalls := []ai.ToolCall{
		{
			ID:   "single_call",
			Name: "read_file",
			Args: map[string]interface{}{
				"path": "/test/file.txt",
			},
		},
	}

	ctx := context.Background()
	plan, err := planner.PlanExecution(ctx, toolCalls)

	if err != nil {
		t.Fatalf("Single tool call planning failed: %v", err)
	}

	if len(plan.Steps) != 1 {
		t.Errorf("Expected 1 step, got %d", len(plan.Steps))
	}

	if len(plan.Parallel) != 1 {
		t.Errorf("Expected 1 parallel group, got %d", len(plan.Parallel))
	}
}

func TestExecutionStep_CacheKeyGeneration(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	toolCall := ai.ToolCall{
		ID:   "cache_test",
		Name: "web_search",
		Args: map[string]interface{}{
			"query": "cacheable query",
			"count": 10,
		},
	}

	step := planner.createExecutionStep("test_step", toolCall)

	if step.CacheKey == "" {
		t.Error("Cache key should be generated for cacheable tool")
	}

	// Test non-cacheable tool
	nonCacheableCall := ai.ToolCall{
		ID:   "no_cache_test",
		Name: "exec",
		Args: map[string]interface{}{
			"command": "ls -la",
		},
	}

	nonCacheableStep := planner.createExecutionStep("test_step_2", nonCacheableCall)

	if nonCacheableStep.CacheKey != "" {
		t.Error("Cache key should not be generated for non-cacheable tool")
	}
}

func TestPlanEstimation(t *testing.T) {
	cache := NewResultCache(NewMemoryStorage(), 10)
	metrics := NewMetricsCollector()
	analyzer := NewDependencyAnalyzer()
	optimizer := NewExecutionOptimizer(StrategyBalanced, 5)

	planner := NewExecutionPlanner(optimizer, analyzer, cache, metrics)

	toolCalls := []ai.ToolCall{
		{ID: "1", Name: "web_search", Args: map[string]interface{}{"query": "test"}},
		{ID: "2", Name: "memory_search", Args: map[string]interface{}{"query": "test"}},
		{ID: "3", Name: "read_file", Args: map[string]interface{}{"path": "/test"}},
	}

	ctx := context.Background()
	plan, err := planner.PlanExecution(ctx, toolCalls)

	if err != nil {
		t.Fatalf("Plan estimation failed: %v", err)
	}

	// Test that estimates are reasonable
	if plan.Estimated.Duration <= 0 {
		t.Error("Estimated duration should be positive")
	}

	if plan.Estimated.Duration > time.Minute {
		t.Error("Estimated duration seems too high for simple operations")
	}

	if plan.Estimated.Cost < 0 {
		t.Error("Estimated cost should be non-negative")
	}

	if plan.Estimated.Reliability <= 0 || plan.Estimated.Reliability > 1 {
		t.Error("Estimated reliability should be between 0 and 1")
	}
}
