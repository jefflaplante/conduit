package planning

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// ExecutionOptimizer optimizes execution plans for performance, reliability, or cost
type ExecutionOptimizer struct {
	strategy         PlanningStrategy
	maxParallel      int
	optimizations    []string
	performanceModel *PerformanceModel
}

// PerformanceModel predicts execution characteristics
type PerformanceModel struct {
	ConcurrencyPenalty  float64       `json:"concurrency_penalty"` // Performance loss per parallel task
	NetworkLatencyBase  time.Duration `json:"network_latency_base"`
	CPUIntensivePenalty float64       `json:"cpu_intensive_penalty"` // Extra latency for CPU tasks
	IOContention        float64       `json:"io_contention"`         // File I/O contention factor
	CacheHitSpeedup     float64       `json:"cache_hit_speedup"`     // Speed multiplier for cache hits
}

// OptimizationResult contains details about applied optimizations
type OptimizationResult struct {
	OriginalDuration     time.Duration `json:"original_duration"`
	OptimizedDuration    time.Duration `json:"optimized_duration"`
	Speedup              float64       `json:"speedup"`
	OptimizationsApplied []string      `json:"optimizations_applied"`
	ParallelGroups       int           `json:"parallel_groups"`
	MaxConcurrency       int           `json:"max_concurrency"`
	EstimatedSavings     float64       `json:"estimated_savings"` // Cost or time savings
}

// NewExecutionOptimizer creates a new execution optimizer
func NewExecutionOptimizer(strategy PlanningStrategy, maxParallel int) *ExecutionOptimizer {
	return &ExecutionOptimizer{
		strategy:      strategy,
		maxParallel:   maxParallel,
		optimizations: []string{},
		performanceModel: &PerformanceModel{
			ConcurrencyPenalty:  0.1, // 10% penalty per concurrent task
			NetworkLatencyBase:  time.Millisecond * 50,
			CPUIntensivePenalty: 0.2,  // 20% penalty for CPU-intensive tasks
			IOContention:        0.15, // 15% penalty for I/O contention
			CacheHitSpeedup:     0.95, // 95% time reduction for cache hits
		},
	}
}

// OptimizeExecution creates an optimized execution plan
func (eo *ExecutionOptimizer) OptimizeExecution(ctx context.Context, plan *ExecutionPlan) (*ExecutionPlan, error) {
	log.Printf("Optimizing execution plan %s with strategy: %s", plan.ID, eo.strategy)

	// Create optimized copy
	optimized := eo.copyPlan(plan)
	optimized.OptimizedFor = string(eo.strategy)

	// Clear previous optimizations
	eo.optimizations = []string{}

	// Apply optimization strategies
	switch eo.strategy {
	case StrategySpeed:
		if err := eo.optimizeForSpeed(optimized); err != nil {
			return nil, err
		}
	case StrategyReliability:
		if err := eo.optimizeForReliability(optimized); err != nil {
			return nil, err
		}
	case StrategyCost:
		if err := eo.optimizeForCost(optimized); err != nil {
			return nil, err
		}
	case StrategyBalanced:
		if err := eo.optimizeBalanced(optimized); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown optimization strategy: %s", eo.strategy)
	}

	// Create parallel execution groups
	parallelGroups, err := eo.createParallelGroups(optimized)
	if err != nil {
		return nil, err
	}
	optimized.Parallel = parallelGroups

	log.Printf("Optimization complete: %d groups, %s applied",
		len(optimized.Parallel), eo.optimizations)

	return optimized, nil
}

// optimizeForSpeed prioritizes execution time reduction
func (eo *ExecutionOptimizer) optimizeForSpeed(plan *ExecutionPlan) error {
	log.Printf("Optimizing for speed")

	// 1. Prioritize fast, cacheable operations first
	eo.reorderByLatency(plan, true) // ascending order - fastest first
	eo.optimizations = append(eo.optimizations, "latency_reordering")

	// 2. Aggressive parallelization
	eo.enableMaxParallelization(plan)
	eo.optimizations = append(eo.optimizations, "max_parallelization")

	// 3. Reduce timeouts for speed
	eo.adjustTimeouts(plan, 0.7) // 70% of default timeouts
	eo.optimizations = append(eo.optimizations, "reduced_timeouts")

	// 4. Fewer retries for speed
	eo.adjustRetries(plan, 0.5) // Half the default retries
	eo.optimizations = append(eo.optimizations, "reduced_retries")

	// 5. Cache-first strategy
	eo.prioritizeCacheableOperations(plan)
	eo.optimizations = append(eo.optimizations, "cache_prioritization")

	return nil
}

// optimizeForReliability prioritizes success probability
func (eo *ExecutionOptimizer) optimizeForReliability(plan *ExecutionPlan) error {
	log.Printf("Optimizing for reliability")

	// 1. Prioritize most reliable operations first
	eo.reorderByReliability(plan)
	eo.optimizations = append(eo.optimizations, "reliability_reordering")

	// 2. Conservative parallelization to avoid resource contention
	eo.limitParallelization(plan, 0.6) // 60% of max parallelization
	eo.optimizations = append(eo.optimizations, "conservative_parallelization")

	// 3. Longer timeouts for reliability
	eo.adjustTimeouts(plan, 1.5) // 150% of default timeouts
	eo.optimizations = append(eo.optimizations, "extended_timeouts")

	// 4. More retries for reliability
	eo.adjustRetries(plan, 1.5) // 150% of default retries
	eo.optimizations = append(eo.optimizations, "increased_retries")

	// 5. Add fallback strategies
	eo.enhanceFallbacks(plan)
	eo.optimizations = append(eo.optimizations, "enhanced_fallbacks")

	// 6. Sequential execution for critical operations
	eo.forceSequentialForCritical(plan)
	eo.optimizations = append(eo.optimizations, "sequential_critical")

	return nil
}

// optimizeForCost prioritizes cost reduction
func (eo *ExecutionOptimizer) optimizeForCost(plan *ExecutionPlan) error {
	log.Printf("Optimizing for cost")

	// 1. Prioritize cheap operations first
	eo.reorderByCost(plan)
	eo.optimizations = append(eo.optimizations, "cost_reordering")

	// 2. Maximize cache utilization
	eo.prioritizeCacheableOperations(plan)
	eo.optimizations = append(eo.optimizations, "cache_maximization")

	// 3. Reduce timeouts to avoid long expensive operations
	eo.adjustTimeouts(plan, 0.8) // 80% of default timeouts
	eo.optimizations = append(eo.optimizations, "cost_conscious_timeouts")

	// 4. Fewer retries to avoid repeated costs
	eo.adjustRetries(plan, 0.7) // 70% of default retries
	eo.optimizations = append(eo.optimizations, "cost_conscious_retries")

	// 5. Intelligent parallelization based on cost/benefit
	eo.costAwareParallelization(plan)
	eo.optimizations = append(eo.optimizations, "cost_aware_parallelization")

	return nil
}

// optimizeBalanced balances speed, reliability, and cost
func (eo *ExecutionOptimizer) optimizeBalanced(plan *ExecutionPlan) error {
	log.Printf("Optimizing for balanced performance")

	// 1. Multi-criteria reordering
	eo.reorderByScore(plan)
	eo.optimizations = append(eo.optimizations, "balanced_reordering")

	// 2. Moderate parallelization
	eo.limitParallelization(plan, 0.8) // 80% of max parallelization
	eo.optimizations = append(eo.optimizations, "balanced_parallelization")

	// 3. Standard timeouts
	// No adjustment - keep defaults

	// 4. Standard retries with smart fallbacks
	eo.smartFallbacks(plan)
	eo.optimizations = append(eo.optimizations, "smart_fallbacks")

	// 5. Cache optimization
	eo.prioritizeCacheableOperations(plan)
	eo.optimizations = append(eo.optimizations, "cache_optimization")

	return nil
}

// Optimization implementation functions

func (eo *ExecutionOptimizer) reorderByLatency(plan *ExecutionPlan, ascending bool) {
	sort.Slice(plan.Steps, func(i, j int) bool {
		// Get average latencies (would come from performance profiles)
		latencyI := eo.estimateLatency(plan.Steps[i])
		latencyJ := eo.estimateLatency(plan.Steps[j])

		if ascending {
			return latencyI < latencyJ
		}
		return latencyI > latencyJ
	})
}

func (eo *ExecutionOptimizer) reorderByReliability(plan *ExecutionPlan) {
	sort.Slice(plan.Steps, func(i, j int) bool {
		// Most reliable first (higher success rate)
		reliabilityI := eo.estimateReliability(plan.Steps[i])
		reliabilityJ := eo.estimateReliability(plan.Steps[j])
		return reliabilityI > reliabilityJ
	})
}

func (eo *ExecutionOptimizer) reorderByCost(plan *ExecutionPlan) {
	sort.Slice(plan.Steps, func(i, j int) bool {
		// Cheapest first (lower cost)
		return plan.Steps[i].CostWeight < plan.Steps[j].CostWeight
	})
}

func (eo *ExecutionOptimizer) reorderByScore(plan *ExecutionPlan) {
	sort.Slice(plan.Steps, func(i, j int) bool {
		// Balanced score considering latency, cost, and reliability
		scoreI := eo.calculateBalancedScore(plan.Steps[i])
		scoreJ := eo.calculateBalancedScore(plan.Steps[j])
		return scoreI > scoreJ // Higher score = better
	})
}

func (eo *ExecutionOptimizer) enableMaxParallelization(plan *ExecutionPlan) {
	for i := range plan.Steps {
		// Mark all non-conflicting steps as parallel-safe
		plan.Steps[i].Priority = 0 // Same priority = can be parallel
	}
}

func (eo *ExecutionOptimizer) limitParallelization(plan *ExecutionPlan, factor float64) {
	// Reduce parallelization by spreading priorities
	maxParallel := int(float64(eo.maxParallel) * factor)
	currentGroup := 0
	stepsInGroup := 0

	for i := range plan.Steps {
		if stepsInGroup >= maxParallel {
			currentGroup++
			stepsInGroup = 0
		}
		plan.Steps[i].Priority = currentGroup
		stepsInGroup++
	}
}

func (eo *ExecutionOptimizer) costAwareParallelization(plan *ExecutionPlan) {
	// Group expensive operations to avoid parallel cost spikes
	expensiveThreshold := 0.01 // Cost threshold

	for i := range plan.Steps {
		if plan.Steps[i].CostWeight > expensiveThreshold {
			// Force expensive operations into higher priority groups (sequential)
			plan.Steps[i].Priority = i
		}
	}
}

func (eo *ExecutionOptimizer) adjustTimeouts(plan *ExecutionPlan, factor float64) {
	for i := range plan.Steps {
		newTimeout := time.Duration(float64(plan.Steps[i].Timeout) * factor)
		plan.Steps[i].Timeout = newTimeout
	}
}

func (eo *ExecutionOptimizer) adjustRetries(plan *ExecutionPlan, factor float64) {
	for i := range plan.Steps {
		newRetries := int(float64(plan.Steps[i].Retries) * factor)
		if newRetries < 1 {
			newRetries = 1 // Minimum 1 retry
		}
		plan.Steps[i].Retries = newRetries
	}
}

func (eo *ExecutionOptimizer) prioritizeCacheableOperations(plan *ExecutionPlan) {
	for i := range plan.Steps {
		if plan.Steps[i].CacheKey != "" {
			// Lower priority number = higher priority execution
			plan.Steps[i].Priority = 0
		}
	}
}

func (eo *ExecutionOptimizer) enhanceFallbacks(plan *ExecutionPlan) {
	for i := range plan.Steps {
		if len(plan.Steps[i].Fallbacks) == 0 && eo.shouldHaveFallback(plan.Steps[i]) {
			// Add simple fallback strategy
			fallback := ExecutionStep{
				ID:       fmt.Sprintf("%s_fallback", plan.Steps[i].ID),
				ToolName: plan.Steps[i].ToolName,
				Args:     eo.createSimplifiedArgs(plan.Steps[i].Args),
				Timeout:  plan.Steps[i].Timeout / 2,
				Retries:  1,
				Priority: plan.Steps[i].Priority + 10, // Much lower priority
			}
			plan.Steps[i].Fallbacks = append(plan.Steps[i].Fallbacks, fallback)
		}
	}
}

func (eo *ExecutionOptimizer) forceSequentialForCritical(plan *ExecutionPlan) {
	criticalTools := []string{"write_file", "exec"} // Tools that should run sequentially

	priority := 0
	for i := range plan.Steps {
		for _, criticalTool := range criticalTools {
			if plan.Steps[i].ToolName == criticalTool {
				plan.Steps[i].Priority = priority
				priority++
				break
			}
		}
	}
}

func (eo *ExecutionOptimizer) smartFallbacks(plan *ExecutionPlan) {
	for i := range plan.Steps {
		if eo.estimateReliability(plan.Steps[i]) < 0.9 && len(plan.Steps[i].Fallbacks) == 0 {
			// Add smart fallback for unreliable operations
			fallback := eo.createSmartFallback(plan.Steps[i])
			if fallback.ToolName != "" {
				plan.Steps[i].Fallbacks = append(plan.Steps[i].Fallbacks, fallback)
			}
		}
	}
}

// createParallelGroups creates parallel execution groups based on dependencies and priorities
func (eo *ExecutionOptimizer) createParallelGroups(plan *ExecutionPlan) ([][]string, error) {
	// Group steps by priority level
	priorityGroups := make(map[int][]string)

	for _, step := range plan.Steps {
		priority := step.Priority
		priorityGroups[priority] = append(priorityGroups[priority], step.ID)
	}

	// Sort priorities and create ordered groups
	var priorities []int
	for p := range priorityGroups {
		priorities = append(priorities, p)
	}
	sort.Ints(priorities)

	var parallelGroups [][]string
	for _, priority := range priorities {
		group := priorityGroups[priority]

		// Split large groups based on dependencies and max parallelization
		subGroups := eo.splitGroupByDependencies(group, plan.Dependencies)
		parallelGroups = append(parallelGroups, subGroups...)
	}

	return parallelGroups, nil
}

func (eo *ExecutionOptimizer) splitGroupByDependencies(group []string, dependencies map[string][]string) [][]string {
	if len(group) <= eo.maxParallel {
		// Group is small enough to run in parallel
		return [][]string{group}
	}

	// Split large group into smaller parallel groups
	var subGroups [][]string
	currentGroup := []string{}

	for _, stepID := range group {
		// Check if this step has dependencies within the current group
		hasInternalDeps := false
		if deps, exists := dependencies[stepID]; exists {
			for _, dep := range deps {
				for _, currentStepID := range currentGroup {
					if dep == currentStepID {
						hasInternalDeps = true
						break
					}
				}
				if hasInternalDeps {
					break
				}
			}
		}

		if hasInternalDeps || len(currentGroup) >= eo.maxParallel {
			// Start new group
			if len(currentGroup) > 0 {
				subGroups = append(subGroups, currentGroup)
				currentGroup = []string{}
			}
		}

		currentGroup = append(currentGroup, stepID)
	}

	// Add final group
	if len(currentGroup) > 0 {
		subGroups = append(subGroups, currentGroup)
	}

	return subGroups
}

// Estimation functions for optimization

func (eo *ExecutionOptimizer) estimateLatency(step ExecutionStep) time.Duration {
	// Base latency from tool complexity
	baseLatency := time.Duration(step.Complexity * float64(time.Second))

	// Add network latency for web tools
	if eo.isNetworkTool(step.ToolName) {
		baseLatency += eo.performanceModel.NetworkLatencyBase
	}

	// Add CPU penalty for complex operations
	if step.Complexity > 0.7 {
		penalty := time.Duration(eo.performanceModel.CPUIntensivePenalty * float64(baseLatency))
		baseLatency += penalty
	}

	return baseLatency
}

func (eo *ExecutionOptimizer) estimateReliability(step ExecutionStep) float64 {
	// Base reliability from tool characteristics
	baseReliability := 1.0 - step.Complexity*0.2 // More complex = less reliable

	// Network tools are less reliable
	if eo.isNetworkTool(step.ToolName) {
		baseReliability *= 0.95
	}

	// File operations are very reliable
	if eo.isFileOperation(step.ToolName) {
		baseReliability = 0.99
	}

	return baseReliability
}

func (eo *ExecutionOptimizer) calculateBalancedScore(step ExecutionStep) float64 {
	// Weighted score: 40% speed, 35% reliability, 25% cost
	latency := eo.estimateLatency(step)
	reliability := eo.estimateReliability(step)
	cost := step.CostWeight

	speedScore := 1.0 / (1.0 + latency.Seconds()) // Lower latency = higher score
	reliabilityScore := reliability               // Higher reliability = higher score
	costScore := 1.0 / (1.0 + cost*100)           // Lower cost = higher score

	return 0.4*speedScore + 0.35*reliabilityScore + 0.25*costScore
}

// Helper functions

func (eo *ExecutionOptimizer) isNetworkTool(toolName string) bool {
	networkTools := []string{"web_search", "web_fetch", "message"}
	for _, tool := range networkTools {
		if toolName == tool {
			return true
		}
	}
	return false
}

func (eo *ExecutionOptimizer) isFileOperation(toolName string) bool {
	fileTools := []string{"read_file", "write_file", "list_files"}
	for _, tool := range fileTools {
		if toolName == tool {
			return true
		}
	}
	return false
}

func (eo *ExecutionOptimizer) shouldHaveFallback(step ExecutionStep) bool {
	return eo.estimateReliability(step) < 0.9 || step.CostWeight > 0.01
}

func (eo *ExecutionOptimizer) createSimplifiedArgs(args map[string]interface{}) map[string]interface{} {
	simplified := make(map[string]interface{})

	// Copy args but with simplified parameters
	for key, value := range args {
		switch key {
		case "count", "limit":
			// Reduce result count for fallback
			if count, ok := value.(float64); ok && count > 5 {
				simplified[key] = 5.0
			} else {
				simplified[key] = value
			}
		case "maxChars":
			// Reduce content size for fallback
			if maxChars, ok := value.(float64); ok && maxChars > 1000 {
				simplified[key] = 1000.0
			} else {
				simplified[key] = value
			}
		default:
			simplified[key] = value
		}
	}

	return simplified
}

func (eo *ExecutionOptimizer) createSmartFallback(step ExecutionStep) ExecutionStep {
	fallback := ExecutionStep{
		ID:       fmt.Sprintf("%s_smart_fallback", step.ID),
		ToolName: step.ToolName,
		Args:     eo.createSimplifiedArgs(step.Args),
		Timeout:  step.Timeout / 2,
		Retries:  1,
		Priority: step.Priority + 5,
	}

	// Tool-specific smart fallbacks
	switch step.ToolName {
	case "web_search":
		// Fallback to basic search
		fallback.Args["count"] = 3
		delete(fallback.Args, "freshness") // Remove time filters

	case "web_fetch":
		// Fallback to text extraction only
		fallback.Args["extractMode"] = "text"
		fallback.Args["maxChars"] = 1000

	case "memory_search":
		// Fallback to simpler query
		if query, ok := step.Args["query"].(string); ok {
			// Use first 3 words only
			words := strings.Fields(query)
			if len(words) > 3 {
				fallback.Args["query"] = strings.Join(words[:3], " ")
			}
		}
	}

	return fallback
}

func (eo *ExecutionOptimizer) copyPlan(original *ExecutionPlan) *ExecutionPlan {
	copied := &ExecutionPlan{
		ID:           original.ID,
		Steps:        make([]ExecutionStep, len(original.Steps)),
		Dependencies: make(map[string][]string),
		CreatedAt:    original.CreatedAt,
		OptimizedFor: original.OptimizedFor,
	}

	// Deep copy steps
	copy(copied.Steps, original.Steps)

	// Deep copy dependencies
	for key, deps := range original.Dependencies {
		copied.Dependencies[key] = make([]string, len(deps))
		copy(copied.Dependencies[key], deps)
	}

	return copied
}

// GetOptimizationResult returns details about the optimization process
func (eo *ExecutionOptimizer) GetOptimizationResult() *OptimizationResult {
	return &OptimizationResult{
		OptimizationsApplied: append([]string{}, eo.optimizations...),
		ParallelGroups:       len(eo.optimizations), // Placeholder
		MaxConcurrency:       eo.maxParallel,
	}
}

// SetPerformanceModel updates the performance model parameters
func (eo *ExecutionOptimizer) SetPerformanceModel(model *PerformanceModel) {
	eo.performanceModel = model
	log.Printf("Updated performance model")
}
