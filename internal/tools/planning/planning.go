package planning

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
)

// PlanningEngine provides the main interface for advanced tool execution planning and optimization
type PlanningEngine struct {
	planner   *ExecutionPlanner
	optimizer *ExecutionOptimizer
	analyzer  *DependencyAnalyzer
	cache     *ResultCache
	metrics   *MetricsCollector
	executor  *ParallelExecutor
	config    *PlanningConfig
	enabled   bool
}

// PlanningConfig configures the planning engine behavior
type PlanningConfig struct {
	Enabled               bool             `json:"enabled"`
	MaxParallel           int              `json:"max_parallel"`
	DefaultStrategy       PlanningStrategy `json:"default_strategy"`
	CacheEnabled          bool             `json:"cache_enabled"`
	CacheMaxSizeMB        int              `json:"cache_max_size_mb"`
	MetricsEnabled        bool             `json:"metrics_enabled"`
	RetryConfig           *RetryConfig     `json:"retry_config"`
	PlanningTimeout       time.Duration    `json:"planning_timeout"`
	ExecutionTimeout      time.Duration    `json:"execution_timeout"`
	OptimizationThreshold int              `json:"optimization_threshold"` // Min tool calls to trigger optimization
}

// PlanningResult contains the complete result of planned execution
type PlanningResult struct {
	Plan                 *ExecutionPlan `json:"plan"`
	Result               *PlanResult    `json:"result"`
	CacheHits            int            `json:"cache_hits"`
	TotalSteps           int            `json:"total_steps"`
	ExecutionTime        time.Duration  `json:"execution_time"`
	PlanningTime         time.Duration  `json:"planning_time"`
	OptimizationsApplied []string       `json:"optimizations_applied"`
	EstimateAccuracy     float64        `json:"estimate_accuracy"`
}

// NewPlanningEngine creates a new planning engine with default configuration
func NewPlanningEngine(toolExecutor ToolExecutor) *PlanningEngine {
	config := &PlanningConfig{
		Enabled:               true,
		MaxParallel:           5,
		DefaultStrategy:       StrategyBalanced,
		CacheEnabled:          true,
		CacheMaxSizeMB:        100,
		MetricsEnabled:        true,
		PlanningTimeout:       time.Second * 30,
		ExecutionTimeout:      time.Minute * 10,
		OptimizationThreshold: 2, // Optimize when 2+ tool calls
		RetryConfig: &RetryConfig{
			MaxRetries:      3,
			BaseDelay:       time.Second,
			MaxDelay:        time.Second * 30,
			BackoffStrategy: "exponential",
			RetryableErrors: []string{"timeout", "network", "rate_limit"},
		},
	}

	return NewPlanningEngineWithConfig(config, toolExecutor)
}

// NewPlanningEngineWithConfig creates a planning engine with custom configuration
func NewPlanningEngineWithConfig(config *PlanningConfig, toolExecutor ToolExecutor) *PlanningEngine {
	engine := &PlanningEngine{
		config:  config,
		enabled: config.Enabled,
	}

	if !config.Enabled {
		log.Printf("Planning engine disabled by configuration")
		return engine
	}

	// Initialize components
	engine.initializeComponents(toolExecutor)

	log.Printf("Planning engine initialized with strategy: %s, max_parallel: %d, cache: %v",
		config.DefaultStrategy, config.MaxParallel, config.CacheEnabled)

	return engine
}

// initializeComponents sets up all planning engine components
func (pe *PlanningEngine) initializeComponents(toolExecutor ToolExecutor) {
	// Initialize metrics collector
	if pe.config.MetricsEnabled {
		pe.metrics = NewMetricsCollector()
	}

	// Initialize cache
	if pe.config.CacheEnabled {
		storage := NewMemoryStorage()
		pe.cache = NewResultCache(storage, pe.config.CacheMaxSizeMB)
	}

	// Initialize dependency analyzer
	pe.analyzer = NewDependencyAnalyzer()

	// Initialize execution optimizer
	pe.optimizer = NewExecutionOptimizer(pe.config.DefaultStrategy, pe.config.MaxParallel)

	// Initialize execution planner
	pe.planner = NewExecutionPlanner(pe.optimizer, pe.analyzer, pe.cache, pe.metrics)

	// Initialize parallel executor
	pe.executor = NewParallelExecutor(pe.planner, pe.cache, pe.metrics, pe.config.MaxParallel)
	if pe.config.RetryConfig != nil {
		pe.executor.SetRetryConfig(pe.config.RetryConfig)
	}
}

// PlanAndExecute is the main entry point for planning and executing tool calls
func (pe *PlanningEngine) PlanAndExecute(ctx context.Context, toolCalls []ai.ToolCall, toolExecutor ToolExecutor) (*PlanningResult, error) {
	if !pe.enabled || len(toolCalls) < pe.config.OptimizationThreshold {
		// Fall back to simple execution for small tool sets
		return pe.executeSimple(ctx, toolCalls, toolExecutor)
	}

	planningStart := time.Now()

	// Create planning context with timeout
	planningCtx, cancel := context.WithTimeout(ctx, pe.config.PlanningTimeout)
	defer cancel()

	// Create execution plan
	plan, err := pe.planner.PlanExecution(planningCtx, toolCalls)
	if err != nil {
		log.Printf("Planning failed, falling back to simple execution: %v", err)
		return pe.executeSimple(ctx, toolCalls, toolExecutor)
	}

	planningTime := time.Since(planningStart)
	executionStart := time.Now()

	// Execute plan
	executionCtx, cancel := context.WithTimeout(ctx, pe.config.ExecutionTimeout)
	defer cancel()

	result, err := pe.executor.ExecutePlan(executionCtx, plan, toolExecutor)
	if err != nil {
		return nil, err
	}

	executionTime := time.Since(executionStart)

	// Calculate estimate accuracy
	estimateAccuracy := 1.0
	if plan.Estimated.Duration > 0 {
		estimateAccuracy = 1.0 - abs(float64(result.Duration-plan.Estimated.Duration))/float64(plan.Estimated.Duration)
		if estimateAccuracy < 0 {
			estimateAccuracy = 0
		}
	}

	// Prepare result
	planningResult := &PlanningResult{
		Plan:                 plan,
		Result:               result,
		CacheHits:            result.CacheHits,
		TotalSteps:           result.TotalSteps,
		ExecutionTime:        executionTime,
		PlanningTime:         planningTime,
		OptimizationsApplied: []string{plan.OptimizedFor},
		EstimateAccuracy:     estimateAccuracy,
	}

	log.Printf("Planned execution completed: %d steps, %v planning time, %v execution time, %.1f%% cache hit rate",
		result.TotalSteps, planningTime, executionTime, float64(result.CacheHits)/float64(result.TotalSteps)*100)

	return planningResult, nil
}

// executeSimple performs simple execution without planning (fallback)
func (pe *PlanningEngine) executeSimple(ctx context.Context, toolCalls []ai.ToolCall, toolExecutor ToolExecutor) (*PlanningResult, error) {
	start := time.Now()

	result := &PlanResult{
		PlanID:      "simple_exec",
		StepResults: make(map[string]*StepResult),
		StartTime:   start,
		TotalSteps:  len(toolCalls),
		Success:     true,
	}

	// Execute tools sequentially
	for i, call := range toolCalls {
		stepStart := time.Now()

		toolResult, err := toolExecutor.ExecuteTool(ctx, call.Name, call.Args)

		stepID := fmt.Sprintf("step_%d", i)
		stepResult := &StepResult{
			StepID:     stepID,
			ToolName:   call.Name,
			Success:    err == nil && toolResult != nil && toolResult.Success,
			Duration:   time.Since(stepStart),
			ExecutedAt: stepStart,
		}

		if toolResult != nil {
			stepResult.Content = toolResult.Content
			stepResult.Data = toolResult.Data
			if !toolResult.Success {
				stepResult.Error = toolResult.Error
			}
		}

		if err != nil {
			stepResult.Error = err.Error()
			stepResult.Success = false
			result.Success = false
			result.FailedSteps = append(result.FailedSteps, stepID)
		}

		result.StepResults[stepID] = stepResult

		// Record metrics if available
		if pe.metrics != nil {
			cost := stepResult.Duration.Seconds() * 0.001 // Simple cost calculation
			pe.metrics.RecordExecution(call.Name, stepResult.Duration, stepResult.Success, false, 0, false, cost, err)
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return &PlanningResult{
		Plan:                 nil, // No plan for simple execution
		Result:               result,
		CacheHits:            0,
		TotalSteps:           len(toolCalls),
		ExecutionTime:        result.Duration,
		PlanningTime:         0,
		OptimizationsApplied: []string{"simple_execution"},
		EstimateAccuracy:     1.0,
	}, nil
}

// GetPerformanceMetrics returns current performance metrics
func (pe *PlanningEngine) GetPerformanceMetrics() *PerformanceSummary {
	if pe.metrics == nil {
		return nil
	}
	return pe.metrics.GetPerformanceSummary()
}

// GetCacheMetrics returns cache performance metrics
func (pe *PlanningEngine) GetCacheMetrics() *CacheMetrics {
	if pe.cache == nil {
		return nil
	}
	return pe.cache.GetMetrics()
}

// SetStrategy changes the optimization strategy
func (pe *PlanningEngine) SetStrategy(strategy PlanningStrategy) {
	if pe.planner != nil {
		pe.planner.SetStrategy(strategy)
	}
	pe.config.DefaultStrategy = strategy
	log.Printf("Planning strategy updated to: %s", strategy)
}

// ClearCache clears the result cache
func (pe *PlanningEngine) ClearCache(ctx context.Context) error {
	if pe.cache == nil {
		return nil
	}
	return pe.cache.Clear(ctx)
}

// InvalidateCache invalidates cache entries matching a pattern
func (pe *PlanningEngine) InvalidateCache(ctx context.Context, pattern string) error {
	if pe.cache == nil {
		return nil
	}
	return pe.cache.Invalidate(ctx, pattern)
}

// GetToolProfile returns performance profile for a specific tool
func (pe *PlanningEngine) GetToolProfile(toolName string) *ToolProfile {
	if pe.planner == nil {
		return nil
	}
	profiles := pe.planner.GetToolProfiles()
	return profiles[toolName]
}

// UpdateToolProfile updates the performance profile for a tool
func (pe *PlanningEngine) UpdateToolProfile(toolName string, profile *ToolProfile) {
	if pe.planner != nil {
		pe.planner.UpdateToolProfile(toolName, profile)
	}
}

// GetConfig returns the current planning configuration
func (pe *PlanningEngine) GetConfig() *PlanningConfig {
	return pe.config
}

// UpdateConfig updates the planning configuration
func (pe *PlanningEngine) UpdateConfig(config *PlanningConfig) {
	pe.config = config

	if config.Enabled != pe.enabled {
		pe.enabled = config.Enabled
		log.Printf("Planning engine enabled: %v", pe.enabled)
	}

	if pe.executor != nil {
		pe.executor.SetMaxParallel(config.MaxParallel)
	}

	if pe.planner != nil {
		pe.planner.SetStrategy(config.DefaultStrategy)
	}
}

// IsEnabled returns whether the planning engine is enabled
func (pe *PlanningEngine) IsEnabled() bool {
	return pe.enabled
}

// Enable enables or disables the planning engine
func (pe *PlanningEngine) Enable(enabled bool) {
	pe.enabled = enabled
	pe.config.Enabled = enabled
	log.Printf("Planning engine enabled: %v", enabled)
}

// ExportMetrics exports all metrics data in JSON format
func (pe *PlanningEngine) ExportMetrics() ([]byte, error) {
	if pe.metrics == nil {
		return nil, fmt.Errorf("metrics not enabled")
	}
	return pe.metrics.Export()
}

// ResetMetrics clears all metrics data
func (pe *PlanningEngine) ResetMetrics() {
	if pe.metrics != nil {
		pe.metrics.Reset()
	}
}

// Helper functions

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// ValidateConfig validates a planning configuration
func ValidateConfig(config *PlanningConfig) error {
	if config.MaxParallel < 1 {
		return fmt.Errorf("max_parallel must be at least 1")
	}

	if config.CacheMaxSizeMB < 1 {
		return fmt.Errorf("cache_max_size_mb must be at least 1")
	}

	if config.PlanningTimeout < time.Millisecond {
		return fmt.Errorf("planning_timeout must be positive")
	}

	if config.ExecutionTimeout < time.Millisecond {
		return fmt.Errorf("execution_timeout must be positive")
	}

	validStrategies := map[PlanningStrategy]bool{
		StrategySpeed:       true,
		StrategyReliability: true,
		StrategyCost:        true,
		StrategyBalanced:    true,
	}

	if !validStrategies[config.DefaultStrategy] {
		return fmt.Errorf("invalid default_strategy: %s", config.DefaultStrategy)
	}

	return nil
}

// GetDefaultConfig returns a default planning configuration
func GetDefaultConfig() *PlanningConfig {
	return &PlanningConfig{
		Enabled:               true,
		MaxParallel:           5,
		DefaultStrategy:       StrategyBalanced,
		CacheEnabled:          true,
		CacheMaxSizeMB:        100,
		MetricsEnabled:        true,
		PlanningTimeout:       time.Second * 30,
		ExecutionTimeout:      time.Minute * 10,
		OptimizationThreshold: 2,
		RetryConfig: &RetryConfig{
			MaxRetries:      3,
			BaseDelay:       time.Second,
			MaxDelay:        time.Second * 30,
			BackoffStrategy: "exponential",
			RetryableErrors: []string{"timeout", "network", "rate_limit", "temporary"},
		},
	}
}
