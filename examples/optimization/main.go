package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
	"conduit/internal/config"
	"conduit/internal/tools"
	"conduit/internal/tools/planning"
)

// Example demonstrating advanced tool optimization features
func main() {
	fmt.Println("Conduit Go - Advanced Tool Optimization Example")
	fmt.Println("====================================================")

	// Example 1: Basic enhanced execution
	fmt.Println("\n1. Basic Enhanced Execution")
	basicExample()

	// Example 2: Different optimization strategies
	fmt.Println("\n2. Optimization Strategies")
	strategyExample()

	// Example 3: Cache management
	fmt.Println("\n3. Cache Management")
	cacheExample()

	// Example 4: Performance monitoring
	fmt.Println("\n4. Performance Monitoring")
	metricsExample()

	// Example 5: Custom tool profiles
	fmt.Println("\n5. Custom Tool Profiles")
	customProfileExample()
}

func basicExample() {
	// Create a mock registry (in real usage, use your existing registry)
	registry := createMockRegistry()

	// Create enhanced execution engine with planning
	enhancedEngine := tools.CreateEnhancedExecutionEngine(registry)

	// Complex tool chain that benefits from optimization
	toolCalls := []ai.ToolCall{
		{
			ID:   "search_1",
			Name: "web_search",
			Args: map[string]interface{}{
				"query": "Conduit AI framework",
				"count": 10,
			},
		},
		{
			ID:   "search_2",
			Name: "memory_search",
			Args: map[string]interface{}{
				"query": "Conduit documentation",
			},
		},
		{
			ID:   "fetch_1",
			Name: "web_fetch",
			Args: map[string]interface{}{
				"url": "https://github.com/conduit/conduit",
			},
		},
		{
			ID:   "file_1",
			Name: "read_file",
			Args: map[string]interface{}{
				"path": "/tmp/example.txt",
			},
		},
	}

	ctx := context.Background()
	start := time.Now()

	// Execute with automatic optimization
	results, err := enhancedEngine.ExecuteToolCalls(ctx, toolCalls)

	duration := time.Since(start)

	if err != nil {
		log.Printf("Execution failed: %v", err)
		return
	}

	fmt.Printf("Executed %d tools in %v\n", len(results), duration)

	// The enhanced engine automatically:
	// - Detects that web_search and memory_search can run in parallel
	// - Runs web_fetch after web_search (potential dependency)
	// - Runs read_file in parallel with other operations
	// - Caches results from web_search and memory_search
	// - Applies retry logic for any failures

	for i, result := range results {
		status := "SUCCESS"
		if result.Error != nil {
			status = "FAILED"
		}
		fmt.Printf("  Tool %d (%s): %s (%v)\n",
			i+1, result.ToolCall.Name, status, result.Duration)
	}
}

func strategyExample() {
	registry := createMockRegistry()
	enhancedEngine := tools.CreateEnhancedExecutionEngine(registry)

	toolCalls := []ai.ToolCall{
		{ID: "1", Name: "web_search", Args: map[string]interface{}{"query": "test"}},
		{ID: "2", Name: "web_fetch", Args: map[string]interface{}{"url": "https://example.com"}},
	}

	ctx := context.Background()

	strategies := []planning.PlanningStrategy{
		planning.StrategySpeed,
		planning.StrategyReliability,
		planning.StrategyCost,
		planning.StrategyBalanced,
	}

	for _, strategy := range strategies {
		fmt.Printf("\nTesting strategy: %s\n", strategy)

		enhancedEngine.SetPlanningStrategy(strategy)

		start := time.Now()
		results, err := enhancedEngine.ExecuteToolCalls(ctx, toolCalls)
		duration := time.Since(start)

		if err != nil {
			fmt.Printf("  Failed: %v\n", err)
			continue
		}

		fmt.Printf("  Completed in %v with %d tools\n", duration, len(results))

		// Speed strategy: prioritizes fast operations, aggressive parallelization
		// Reliability: more retries, longer timeouts, conservative parallelization
		// Cost: prioritizes cheap operations, maximizes cache usage
		// Balanced: balances all factors
	}
}

func cacheExample() {
	registry := createMockRegistry()
	enhancedEngine := tools.CreateEnhancedExecutionEngine(registry)

	toolCall := ai.ToolCall{
		ID:   "cache_test",
		Name: "web_search",
		Args: map[string]interface{}{
			"query": "cacheable search query",
			"count": 5,
		},
	}

	ctx := context.Background()

	fmt.Println("First execution (cache miss):")
	start := time.Now()
	_, _ = enhancedEngine.ExecuteToolCalls(ctx, []ai.ToolCall{toolCall})
	duration1 := time.Since(start)
	fmt.Printf("  Duration: %v\n", duration1)

	fmt.Println("Second execution (cache hit):")
	start = time.Now()
	_, _ = enhancedEngine.ExecuteToolCalls(ctx, []ai.ToolCall{toolCall})
	duration2 := time.Since(start)
	fmt.Printf("  Duration: %v\n", duration2)

	if duration2 < duration1 {
		fmt.Printf("  Cache speedup: %.1fx faster\n", float64(duration1)/float64(duration2))
	}

	// Get cache metrics
	cacheMetrics := enhancedEngine.GetCacheMetrics()
	if cacheMetrics != nil {
		fmt.Printf("Cache hit rate: %.1f%%\n",
			float64(cacheMetrics.Hits)/float64(cacheMetrics.Hits+cacheMetrics.Misses)*100)
	}

	// Clear cache
	fmt.Println("\nClearing cache...")
	enhancedEngine.ClearCache(ctx)

	// Invalidate specific tool cache
	fmt.Println("Invalidating web_search cache...")
	enhancedEngine.InvalidateCache(ctx, "web_search")
}

func metricsExample() {
	registry := createMockRegistry()
	enhancedEngine := tools.CreateEnhancedExecutionEngine(registry)

	// Execute some tools to generate metrics
	toolCalls := []ai.ToolCall{
		{ID: "1", Name: "web_search", Args: map[string]interface{}{"query": "metrics test"}},
		{ID: "2", Name: "memory_search", Args: map[string]interface{}{"query": "test"}},
		{ID: "3", Name: "read_file", Args: map[string]interface{}{"path": "/tmp/test"}},
	}

	ctx := context.Background()
	enhancedEngine.ExecuteToolCalls(ctx, toolCalls)

	// Get performance metrics
	summary := enhancedEngine.GetPlanningMetrics()
	if summary != nil {
		fmt.Printf("Total executions: %d\n", summary.TotalExecutions)
		fmt.Printf("Overall success rate: %.1f%%\n", summary.OverallSuccessRate*100)
		fmt.Printf("Average latency: %v\n", summary.AverageLatency)
		fmt.Printf("Cache hit rate: %.1f%%\n", summary.CacheHitRate*100)
		fmt.Printf("Total cost: $%.4f\n", summary.TotalCost)

		// Top performing tools
		fmt.Println("\nTop performing tools:")
		for i, tool := range summary.TopPerformers {
			if i >= 3 {
				break
			}
			fmt.Printf("  %d. %s (Score: %.2f, Success: %.1f%%)\n",
				i+1, tool.ToolName, tool.Score, tool.SuccessRate*100)
		}

		// Performance recommendations
		fmt.Println("\nPerformance recommendations:")
		for i, rec := range summary.Recommendations {
			if i >= 3 {
				break
			}
			fmt.Printf("  %s: %s (Priority: %s)\n",
				rec.Type, rec.Description, rec.Priority)
		}
	}

	// Export metrics data
	metricsData, err := enhancedEngine.ExportMetrics()
	if err == nil {
		fmt.Printf("\nMetrics data exported (%d bytes)\n", len(metricsData))
	}
}

func customProfileExample() {
	registry := createMockRegistry()
	enhancedEngine := tools.CreateEnhancedExecutionEngine(registry)

	// Get existing tool profile
	existingProfile := enhancedEngine.GetToolProfile("web_search")
	if existingProfile != nil {
		fmt.Printf("Current web_search profile:\n")
		fmt.Printf("  Average latency: %v\n", existingProfile.AverageLatency)
		fmt.Printf("  Success rate: %.1f%%\n", existingProfile.SuccessRate*100)
		fmt.Printf("  Cost per call: $%.4f\n", existingProfile.CostPerCall)
	}

	// Create custom tool profile for better optimization
	customProfile := &planning.ToolProfile{
		Name:            "custom_api_tool",
		AverageLatency:  time.Millisecond * 800, // Relatively fast
		SuccessRate:     0.97,                   // Very reliable
		CostPerCall:     0.003,                  // Moderate cost
		CacheCompatible: true,                   // Results can be cached
		DefaultCacheTTL: time.Minute * 15,       // 15 minute cache
		ParallelSafe:    true,                   // Safe for parallel execution
		MaxRetries:      2,                      // Moderate retries
		TimeoutDuration: time.Second * 8,        // 8 second timeout
		Complexity:      0.6,                    // Medium complexity
	}

	// Update tool profile
	enhancedEngine.UpdateToolProfile("custom_api_tool", customProfile)
	fmt.Printf("\nUpdated custom tool profile\n")

	// The planning engine will now use these characteristics for optimization:
	// - High success rate = prioritized in reliability strategy
	// - Cache compatible = results will be cached for 15 minutes
	// - Parallel safe = can run with other tools
	// - Moderate cost = balanced in cost optimization
}

// Mock registry for examples (replace with your actual registry)
func createMockRegistry() *tools.Registry {
	// This is a simplified mock - in real usage, use your configured registry
	// with actual tool implementations

	// Basic sandbox config
	sandboxCfg := config.SandboxConfig{
		AllowedPaths: []string{"/tmp", "/home/user"},
		WorkspaceDir: "/tmp",
	}

	// Create minimal tool services
	services := &tools.ToolServices{
		SessionStore:  nil, // Would be your actual session store
		ConfigMgr:     nil, // Would be your actual config manager
		WebClient:     nil, // Would be your actual HTTP client
		SkillsManager: nil, // Would be your skills manager
	}

	// Create basic tools config
	toolsCfg := config.ToolsConfig{
		Sandbox:      sandboxCfg,
		EnabledTools: []string{"web_search", "web_fetch", "memory_search", "read_file", "write_file"},
	}

	registry := tools.NewRegistry(toolsCfg)
	registry.SetServices(services)
	return registry
}
