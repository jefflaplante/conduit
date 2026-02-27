package planning

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"conduit/internal/ai"
)

// ExecutionPlan represents an optimized execution plan for tool calls
type ExecutionPlan struct {
	ID           string              `json:"id"`
	Steps        []ExecutionStep     `json:"steps"`
	Dependencies map[string][]string `json:"dependencies"`
	Parallel     [][]string          `json:"parallel"`
	Estimated    EstimatedMetrics    `json:"estimated"`
	CreatedAt    time.Time           `json:"created_at"`
	OptimizedFor string              `json:"optimized_for"` // "speed", "reliability", "cost"
}

// ExecutionStep represents a single step in an execution plan
type ExecutionStep struct {
	ID         string                 `json:"id"`
	ToolName   string                 `json:"tool_name"`
	Args       map[string]interface{} `json:"args"`
	CacheKey   string                 `json:"cache_key,omitempty"`
	Timeout    time.Duration          `json:"timeout"`
	Retries    int                    `json:"retries"`
	Fallbacks  []ExecutionStep        `json:"fallbacks,omitempty"`
	Priority   int                    `json:"priority"`    // 0=highest, higher numbers = lower priority
	Complexity float64                `json:"complexity"`  // 0-1, estimated computational complexity
	CostWeight float64                `json:"cost_weight"` // relative cost of this operation
}

// EstimatedMetrics provides execution estimates for planning
type EstimatedMetrics struct {
	Duration    time.Duration `json:"duration"`
	Cost        float64       `json:"cost"`
	Reliability float64       `json:"reliability"` // 0-1 probability of success
	CacheHit    float64       `json:"cache_hit"`   // 0-1 probability of cache hit
}

// PlanResult represents the result of executing an execution plan
type PlanResult struct {
	PlanID        string                 `json:"plan_id"`
	StepResults   map[string]*StepResult `json:"step_results"`
	StartTime     time.Time              `json:"start_time"`
	EndTime       time.Time              `json:"end_time"`
	Duration      time.Duration          `json:"duration"`
	Success       bool                   `json:"success"`
	CacheHits     int                    `json:"cache_hits"`
	TotalSteps    int                    `json:"total_steps"`
	FailedSteps   []string               `json:"failed_steps,omitempty"`
	Optimizations []string               `json:"optimizations,omitempty"`
}

// StepResult represents the result of executing a single step
type StepResult struct {
	StepID       string                 `json:"step_id"`
	ToolName     string                 `json:"tool_name"`
	Success      bool                   `json:"success"`
	Content      string                 `json:"content"`
	Data         map[string]interface{} `json:"data,omitempty"`
	Error        string                 `json:"error,omitempty"`
	Duration     time.Duration          `json:"duration"`
	ExecutedAt   time.Time              `json:"executed_at"`
	CacheHit     bool                   `json:"cache_hit"`
	Retries      int                    `json:"retries"`
	FallbackUsed bool                   `json:"fallback_used"`
}

// PlanningStrategy defines how execution plans should be optimized
type PlanningStrategy string

const (
	StrategySpeed       PlanningStrategy = "speed"       // Minimize execution time
	StrategyReliability PlanningStrategy = "reliability" // Maximize success probability
	StrategyCost        PlanningStrategy = "cost"        // Minimize execution cost
	StrategyBalanced    PlanningStrategy = "balanced"    // Balance all factors
)

// ExecutionPlanner creates optimized execution plans for tool calls
type ExecutionPlanner struct {
	optimizer    *ExecutionOptimizer
	analyzer     *DependencyAnalyzer
	cache        *ResultCache
	metrics      *MetricsCollector
	strategy     PlanningStrategy
	toolProfiles map[string]*ToolProfile
}

// ToolProfile contains performance characteristics for a specific tool
type ToolProfile struct {
	Name                 string        `json:"name"`
	AverageLatency       time.Duration `json:"average_latency"`
	SuccessRate          float64       `json:"success_rate"`
	CostPerCall          float64       `json:"cost_per_call"`
	CacheCompatible      bool          `json:"cache_compatible"`
	DefaultCacheTTL      time.Duration `json:"default_cache_ttl"`
	ParallelSafe         bool          `json:"parallel_safe"`
	RequiredDependencies []string      `json:"required_dependencies"`
	ConflictsWith        []string      `json:"conflicts_with"`
	MaxRetries           int           `json:"max_retries"`
	TimeoutDuration      time.Duration `json:"timeout_duration"`
	Complexity           float64       `json:"complexity"` // 0-1 scale
}

// NewExecutionPlanner creates a new execution planner with dependencies
func NewExecutionPlanner(optimizer *ExecutionOptimizer, analyzer *DependencyAnalyzer, cache *ResultCache, metrics *MetricsCollector) *ExecutionPlanner {
	planner := &ExecutionPlanner{
		optimizer:    optimizer,
		analyzer:     analyzer,
		cache:        cache,
		metrics:      metrics,
		strategy:     StrategyBalanced,
		toolProfiles: make(map[string]*ToolProfile),
	}

	// Initialize with default tool profiles
	planner.initializeDefaultProfiles()

	return planner
}

// SetStrategy sets the planning optimization strategy
func (p *ExecutionPlanner) SetStrategy(strategy PlanningStrategy) {
	p.strategy = strategy
	log.Printf("Execution planner strategy set to: %s", strategy)
}

// PlanExecution creates an optimized execution plan for the given tool calls
func (p *ExecutionPlanner) PlanExecution(ctx context.Context, toolCalls []ai.ToolCall) (*ExecutionPlan, error) {
	if len(toolCalls) == 0 {
		return nil, fmt.Errorf("no tool calls provided for planning")
	}

	// Generate unique plan ID
	planID := fmt.Sprintf("plan_%d", time.Now().UnixNano())

	log.Printf("Creating execution plan %s for %d tool calls", planID, len(toolCalls))

	// Analyze dependencies between tool calls
	deps, err := p.analyzer.AnalyzeDependencies(ctx, toolCalls)
	if err != nil {
		return nil, fmt.Errorf("dependency analysis failed: %w", err)
	}

	// Create execution steps with metadata
	steps := make([]ExecutionStep, len(toolCalls))
	for i, call := range toolCalls {
		steps[i] = p.createExecutionStep(fmt.Sprintf("step_%d", i), call)
	}

	// Check for cache opportunities
	for i := range steps {
		if p.cache != nil {
			steps[i].CacheKey = p.generateCacheKey(toolCalls[i])
		}
	}

	// Optimize execution order and parallelization
	plan := &ExecutionPlan{
		ID:           planID,
		Steps:        steps,
		Dependencies: deps,
		CreatedAt:    time.Now(),
		OptimizedFor: string(p.strategy),
	}

	// Optimize the plan
	optimizedPlan, err := p.optimizer.OptimizeExecution(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("execution optimization failed: %w", err)
	}

	// Estimate metrics for the optimized plan
	optimizedPlan.Estimated = p.estimateMetrics(optimizedPlan)

	log.Printf("Created optimized execution plan: %d steps, %d parallel groups, estimated duration: %v",
		len(optimizedPlan.Steps), len(optimizedPlan.Parallel), optimizedPlan.Estimated.Duration)

	return optimizedPlan, nil
}

// createExecutionStep creates an execution step from a tool call
func (p *ExecutionPlanner) createExecutionStep(stepID string, call ai.ToolCall) ExecutionStep {
	profile := p.getToolProfile(call.Name)

	step := ExecutionStep{
		ID:         stepID,
		ToolName:   call.Name,
		Args:       call.Args,
		Timeout:    profile.TimeoutDuration,
		Retries:    profile.MaxRetries,
		Priority:   p.calculatePriority(call),
		Complexity: profile.Complexity,
		CostWeight: profile.CostPerCall,
	}

	// Add fallbacks based on tool characteristics
	if p.shouldAddFallbacks(call.Name) {
		step.Fallbacks = p.generateFallbacks(call)
	}

	// Generate cache key if cache is available
	if p.cache != nil {
		step.CacheKey = p.generateCacheKey(call)
	}

	return step
}

// getToolProfile returns the performance profile for a tool
func (p *ExecutionPlanner) getToolProfile(toolName string) *ToolProfile {
	if profile, exists := p.toolProfiles[toolName]; exists {
		return profile
	}

	// Return default profile if not found
	return p.getDefaultProfile(toolName)
}

// calculatePriority determines execution priority based on tool characteristics and strategy
func (p *ExecutionPlanner) calculatePriority(call ai.ToolCall) int {
	profile := p.getToolProfile(call.Name)

	switch p.strategy {
	case StrategySpeed:
		// High latency tools get lower priority (higher numbers)
		if profile.AverageLatency > time.Second {
			return 2
		}
		return 0

	case StrategyReliability:
		// Less reliable tools get higher priority for early failure detection
		if profile.SuccessRate < 0.9 {
			return 0
		}
		return 1

	case StrategyCost:
		// Expensive tools get lower priority
		if profile.CostPerCall > 0.01 {
			return 2
		}
		return 0

	default: // StrategyBalanced
		// Balance latency, cost, and reliability
		score := (profile.AverageLatency.Seconds()/10.0 + profile.CostPerCall*100 + (1.0-profile.SuccessRate)*10) / 3.0
		if score > 1.0 {
			return 2
		} else if score > 0.5 {
			return 1
		}
		return 0
	}
}

// shouldAddFallbacks determines if a tool should have fallback strategies
func (p *ExecutionPlanner) shouldAddFallbacks(toolName string) bool {
	profile := p.getToolProfile(toolName)

	// Add fallbacks for unreliable or expensive tools
	return profile.SuccessRate < 0.95 || profile.CostPerCall > 0.01
}

// generateFallbacks creates fallback execution steps for a tool call
func (p *ExecutionPlanner) generateFallbacks(call ai.ToolCall) []ExecutionStep {
	var fallbacks []ExecutionStep

	// Tool-specific fallback strategies
	switch call.Name {
	case "web_search":
		// Fallback to different search engines or reduced result count
		fallback := ExecutionStep{
			ID:       fmt.Sprintf("%s_fallback_1", call.ID),
			ToolName: "web_search",
			Args:     p.createFallbackArgs(call.Args, "count", 5), // Reduce result count
			Timeout:  time.Second * 15,                            // Shorter timeout
			Retries:  1,
			Priority: 3, // Lower priority
		}
		fallbacks = append(fallbacks, fallback)

	case "web_fetch":
		// Fallback with different extraction mode
		if _, hasMode := call.Args["extractMode"]; !hasMode {
			fallback := ExecutionStep{
				ID:       fmt.Sprintf("%s_fallback_1", call.ID),
				ToolName: "web_fetch",
				Args:     p.createFallbackArgs(call.Args, "extractMode", "text"),
				Timeout:  time.Second * 10,
				Retries:  1,
				Priority: 3,
			}
			fallbacks = append(fallbacks, fallback)
		}

	case "memory_search":
		// Fallback with broader query
		if query, hasQuery := call.Args["query"].(string); hasQuery && len(query) > 10 {
			// Create simpler query by taking first few words
			words := strings.Fields(query)
			if len(words) > 3 {
				simpleQuery := strings.Join(words[:3], " ")
				fallback := ExecutionStep{
					ID:       fmt.Sprintf("%s_fallback_1", call.ID),
					ToolName: "memory_search",
					Args:     p.createFallbackArgs(call.Args, "query", simpleQuery),
					Timeout:  time.Second * 5,
					Retries:  1,
					Priority: 3,
				}
				fallbacks = append(fallbacks, fallback)
			}
		}
	}

	return fallbacks
}

// createFallbackArgs creates modified arguments for fallback steps
func (p *ExecutionPlanner) createFallbackArgs(originalArgs map[string]interface{}, key string, value interface{}) map[string]interface{} {
	fallbackArgs := make(map[string]interface{})
	for k, v := range originalArgs {
		fallbackArgs[k] = v
	}
	fallbackArgs[key] = value
	return fallbackArgs
}

// generateCacheKey generates a cache key for a tool call
func (p *ExecutionPlanner) generateCacheKey(call ai.ToolCall) string {
	profile := p.getToolProfile(call.Name)
	if !profile.CacheCompatible {
		return ""
	}

	return p.cache.GenerateKey(call.Name, call.Args)
}

// estimateMetrics calculates estimated execution metrics for a plan
func (p *ExecutionPlanner) estimateMetrics(plan *ExecutionPlan) EstimatedMetrics {
	var totalDuration time.Duration
	var totalCost float64
	var reliabilityProduct float64 = 1.0
	var cacheHitProbability float64

	// Calculate metrics for each parallel group
	for _, group := range plan.Parallel {
		groupDuration := time.Duration(0)
		groupCost := float64(0)
		groupReliability := float64(1)
		groupCacheHits := 0

		for _, stepID := range group {
			step := plan.GetStep(stepID)
			profile := p.getToolProfile(step.ToolName)

			// Duration is the maximum in a parallel group
			if profile.AverageLatency > groupDuration {
				groupDuration = profile.AverageLatency
			}

			// Cost and reliability accumulate
			groupCost += profile.CostPerCall
			groupReliability *= profile.SuccessRate

			// Check cache probability
			if step.CacheKey != "" && p.cache != nil {
				if p.cache.HasCached(step.CacheKey) {
					groupCacheHits++
				}
			}
		}

		totalDuration += groupDuration
		totalCost += groupCost
		reliabilityProduct *= groupReliability

		if len(group) > 0 {
			cacheHitProbability += float64(groupCacheHits) / float64(len(group))
		}
	}

	// Average cache hit probability across groups
	if len(plan.Parallel) > 0 {
		cacheHitProbability /= float64(len(plan.Parallel))
	}

	return EstimatedMetrics{
		Duration:    totalDuration,
		Cost:        totalCost,
		Reliability: reliabilityProduct,
		CacheHit:    cacheHitProbability,
	}
}

// GetStep retrieves a step by ID from the execution plan
func (p *ExecutionPlan) GetStep(stepID string) ExecutionStep {
	for _, step := range p.Steps {
		if step.ID == stepID {
			return step
		}
	}
	return ExecutionStep{} // Return empty step if not found
}

// ToToolResults converts plan results to tool execution results
func (pr *PlanResult) ToToolResults() []*ai.ToolCall {
	var toolCalls []*ai.ToolCall

	for _, stepResult := range pr.StepResults {
		// Create a tool call result structure
		toolCall := &ai.ToolCall{
			ID:   stepResult.StepID,
			Name: stepResult.ToolName,
			Args: make(map[string]interface{}),
		}

		// Add result data to args for AI consumption
		toolCall.Args["success"] = stepResult.Success
		toolCall.Args["content"] = stepResult.Content
		toolCall.Args["data"] = stepResult.Data
		if stepResult.Error != "" {
			toolCall.Args["error"] = stepResult.Error
		}

		toolCalls = append(toolCalls, toolCall)
	}

	return toolCalls
}

// initializeDefaultProfiles sets up default tool performance profiles
func (p *ExecutionPlanner) initializeDefaultProfiles() {
	// Web search tool profile
	p.toolProfiles["web_search"] = &ToolProfile{
		Name:                 "web_search",
		AverageLatency:       time.Second * 2,
		SuccessRate:          0.95,
		CostPerCall:          0.005, // Relatively expensive
		CacheCompatible:      true,
		DefaultCacheTTL:      time.Hour,
		ParallelSafe:         true,
		RequiredDependencies: []string{},
		ConflictsWith:        []string{},
		MaxRetries:           3,
		TimeoutDuration:      time.Second * 10,
		Complexity:           0.7,
	}

	// Web fetch tool profile
	p.toolProfiles["web_fetch"] = &ToolProfile{
		Name:                 "web_fetch",
		AverageLatency:       time.Second * 3,
		SuccessRate:          0.90,
		CostPerCall:          0.002,
		CacheCompatible:      true,
		DefaultCacheTTL:      time.Minute * 30,
		ParallelSafe:         true,
		RequiredDependencies: []string{},
		ConflictsWith:        []string{},
		MaxRetries:           2,
		TimeoutDuration:      time.Second * 15,
		Complexity:           0.6,
	}

	// Memory search tool profile
	p.toolProfiles["memory_search"] = &ToolProfile{
		Name:                 "memory_search",
		AverageLatency:       time.Millisecond * 500,
		SuccessRate:          0.98,
		CostPerCall:          0.0001, // Very cheap
		CacheCompatible:      true,
		DefaultCacheTTL:      time.Minute * 10,
		ParallelSafe:         true,
		RequiredDependencies: []string{},
		ConflictsWith:        []string{},
		MaxRetries:           1,
		TimeoutDuration:      time.Second * 5,
		Complexity:           0.3,
	}

	// File operation tool profiles (fast and reliable)
	for _, toolName := range []string{"read_file", "write_file", "list_files"} {
		p.toolProfiles[toolName] = &ToolProfile{
			Name:                 toolName,
			AverageLatency:       time.Millisecond * 100,
			SuccessRate:          0.99,
			CostPerCall:          0.0001,
			CacheCompatible:      false, // File operations shouldn't be cached
			ParallelSafe:         true,
			RequiredDependencies: []string{},
			ConflictsWith:        []string{},
			MaxRetries:           1,
			TimeoutDuration:      time.Second * 5,
			Complexity:           0.2,
		}
	}

	// Command execution profile (potentially slow and unreliable)
	p.toolProfiles["exec"] = &ToolProfile{
		Name:                 "exec",
		AverageLatency:       time.Second * 5,
		SuccessRate:          0.85,
		CostPerCall:          0.001,
		CacheCompatible:      false, // Commands shouldn't be cached
		ParallelSafe:         false, // Commands might conflict
		RequiredDependencies: []string{},
		ConflictsWith:        []string{},
		MaxRetries:           2,
		TimeoutDuration:      time.Second * 30,
		Complexity:           0.8,
	}

	log.Printf("Initialized %d default tool profiles", len(p.toolProfiles))
}

// getDefaultProfile creates a default profile for unknown tools
func (p *ExecutionPlanner) getDefaultProfile(toolName string) *ToolProfile {
	return &ToolProfile{
		Name:                 toolName,
		AverageLatency:       time.Second * 2,
		SuccessRate:          0.90,
		CostPerCall:          0.001,
		CacheCompatible:      false,
		DefaultCacheTTL:      time.Minute * 5,
		ParallelSafe:         true,
		RequiredDependencies: []string{},
		ConflictsWith:        []string{},
		MaxRetries:           2,
		TimeoutDuration:      time.Second * 10,
		Complexity:           0.5,
	}
}

// UpdateToolProfile allows runtime updates to tool performance profiles
func (p *ExecutionPlanner) UpdateToolProfile(toolName string, profile *ToolProfile) {
	p.toolProfiles[toolName] = profile
	log.Printf("Updated tool profile for %s", toolName)
}

// GetToolProfiles returns all current tool profiles
func (p *ExecutionPlanner) GetToolProfiles() map[string]*ToolProfile {
	profiles := make(map[string]*ToolProfile)
	for name, profile := range p.toolProfiles {
		profiles[name] = profile
	}
	return profiles
}
