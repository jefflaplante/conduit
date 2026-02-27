package tools

import (
	"context"
	"fmt"
	"log"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/planning"
)

// EnhancedExecutionEngine extends the basic execution engine with advanced planning capabilities
type EnhancedExecutionEngine struct {
	*ExecutionEngine // Embed the basic execution engine
	planningEngine   *planning.PlanningEngine
	registry         *Registry // Tool registry for execution
	enabled          bool      // Whether planning is enabled
}

// NewEnhancedExecutionEngine creates a new enhanced execution engine with planning
func NewEnhancedExecutionEngine(registry *Registry, maxParallel int, timeout time.Duration, planningEnabled bool) *EnhancedExecutionEngine {
	// Create basic execution engine with default chain limit
	basicEngine := NewExecutionEngine(registry, maxParallel, timeout, 25)

	enhanced := &EnhancedExecutionEngine{
		ExecutionEngine: basicEngine,
		registry:        registry,
		enabled:         planningEnabled,
	}

	if planningEnabled {
		// Initialize planning engine with tool executor adapter
		toolExecutor := &RegistryToolExecutor{registry: registry}
		enhanced.planningEngine = planning.NewPlanningEngine(toolExecutor)

		log.Printf("Enhanced execution engine initialized with planning enabled")
	} else {
		log.Printf("Enhanced execution engine initialized with planning disabled")
	}

	return enhanced
}

// NewEnhancedExecutionEngineWithConfig creates an enhanced engine with custom planning configuration
func NewEnhancedExecutionEngineWithConfig(registry *Registry, maxParallel int, timeout time.Duration, planningConfig *planning.PlanningConfig) *EnhancedExecutionEngine {
	basicEngine := NewExecutionEngine(registry, maxParallel, timeout, 25)

	enhanced := &EnhancedExecutionEngine{
		ExecutionEngine: basicEngine,
		registry:        registry,
		enabled:         planningConfig.Enabled,
	}

	if planningConfig.Enabled {
		toolExecutor := &RegistryToolExecutor{registry: registry}
		enhanced.planningEngine = planning.NewPlanningEngineWithConfig(planningConfig, toolExecutor)

		log.Printf("Enhanced execution engine initialized with custom planning config")
	}

	return enhanced
}

// ExecuteToolCalls executes tool calls with optional planning optimization
func (ee *EnhancedExecutionEngine) ExecuteToolCalls(ctx context.Context, calls []ai.ToolCall) ([]*ExecutionResult, error) {
	if !ee.enabled || ee.planningEngine == nil || len(calls) < 2 {
		// Fall back to basic execution for simple cases
		log.Printf("Using basic execution for %d tool calls", len(calls))
		return ee.ExecutionEngine.ExecuteToolCalls(ctx, calls)
	}

	// Use planning engine for complex tool chains
	log.Printf("Using planned execution for %d tool calls", len(calls))
	return ee.executeWithPlanning(ctx, calls)
}

// executeWithPlanning executes tool calls using the planning engine
func (ee *EnhancedExecutionEngine) executeWithPlanning(ctx context.Context, calls []ai.ToolCall) ([]*ExecutionResult, error) {
	toolExecutor := &RegistryToolExecutor{registry: ee.registry}

	// Execute with planning
	planningResult, err := ee.planningEngine.PlanAndExecute(ctx, calls, toolExecutor)
	if err != nil {
		log.Printf("Planned execution failed, falling back to basic: %v", err)
		return ee.ExecutionEngine.ExecuteToolCalls(ctx, calls)
	}

	// Convert planning results to execution results
	executionResults := ee.convertPlanningResults(calls, planningResult)

	log.Printf("Planned execution completed: %d steps, %v execution time, %.1f%% cache hit rate",
		planningResult.TotalSteps, planningResult.ExecutionTime,
		float64(planningResult.CacheHits)/float64(planningResult.TotalSteps)*100)

	return executionResults, nil
}

// convertPlanningResults converts planning results to execution results format
func (ee *EnhancedExecutionEngine) convertPlanningResults(calls []ai.ToolCall, planningResult *planning.PlanningResult) []*ExecutionResult {
	results := make([]*ExecutionResult, len(calls))

	for i, call := range calls {
		stepID := fmt.Sprintf("step_%d", i)

		// Find corresponding step result
		var stepResult *planning.StepResult
		if planningResult.Result != nil {
			stepResult = planningResult.Result.StepResults[stepID]
		}

		// Create execution result
		execResult := &ExecutionResult{
			ToolCall:   &call,
			ExecutedAt: time.Now(),
		}

		if stepResult != nil {
			execResult.Duration = stepResult.Duration

			// Convert step result to tool result
			toolResult := &ToolResult{
				Success: stepResult.Success,
				Content: stepResult.Content,
				Data:    stepResult.Data,
			}

			if stepResult.Error != "" {
				toolResult.Error = stepResult.Error
				execResult.Error = fmt.Errorf("%s", stepResult.Error)
			}

			execResult.Result = toolResult
		} else {
			// Fallback for missing results
			execResult.Result = &ToolResult{
				Success: false,
				Error:   "Step result not found in planning execution",
			}
			execResult.Error = fmt.Errorf("step result not found")
		}

		results[i] = execResult
	}

	return results
}

// GetPlanningMetrics returns performance metrics from the planning engine
func (ee *EnhancedExecutionEngine) GetPlanningMetrics() *planning.PerformanceSummary {
	if ee.planningEngine == nil {
		return nil
	}
	return ee.planningEngine.GetPerformanceMetrics()
}

// GetCacheMetrics returns cache metrics from the planning engine
func (ee *EnhancedExecutionEngine) GetCacheMetrics() *planning.CacheMetrics {
	if ee.planningEngine == nil {
		return nil
	}
	return ee.planningEngine.GetCacheMetrics()
}

// SetPlanningStrategy sets the planning optimization strategy
func (ee *EnhancedExecutionEngine) SetPlanningStrategy(strategy planning.PlanningStrategy) {
	if ee.planningEngine != nil {
		ee.planningEngine.SetStrategy(strategy)
	}
}

// EnablePlanning enables or disables the planning engine
func (ee *EnhancedExecutionEngine) EnablePlanning(enabled bool) {
	ee.enabled = enabled
	if ee.planningEngine != nil {
		ee.planningEngine.Enable(enabled)
	}
	log.Printf("Planning execution enabled: %v", enabled)
}

// IsPlanningEnabled returns whether planning is currently enabled
func (ee *EnhancedExecutionEngine) IsPlanningEnabled() bool {
	return ee.enabled && ee.planningEngine != nil && ee.planningEngine.IsEnabled()
}

// ClearCache clears the planning engine cache
func (ee *EnhancedExecutionEngine) ClearCache(ctx context.Context) error {
	if ee.planningEngine == nil {
		return fmt.Errorf("planning engine not available")
	}
	return ee.planningEngine.ClearCache(ctx)
}

// InvalidateCache invalidates cache entries matching a pattern
func (ee *EnhancedExecutionEngine) InvalidateCache(ctx context.Context, pattern string) error {
	if ee.planningEngine == nil {
		return fmt.Errorf("planning engine not available")
	}
	return ee.planningEngine.InvalidateCache(ctx, pattern)
}

// GetToolProfile returns the performance profile for a specific tool
func (ee *EnhancedExecutionEngine) GetToolProfile(toolName string) *planning.ToolProfile {
	if ee.planningEngine == nil {
		return nil
	}
	return ee.planningEngine.GetToolProfile(toolName)
}

// UpdateToolProfile updates the performance profile for a tool
func (ee *EnhancedExecutionEngine) UpdateToolProfile(toolName string, profile *planning.ToolProfile) {
	if ee.planningEngine != nil {
		ee.planningEngine.UpdateToolProfile(toolName, profile)
	}
}

// ExportMetrics exports all planning metrics
func (ee *EnhancedExecutionEngine) ExportMetrics() ([]byte, error) {
	if ee.planningEngine == nil {
		return nil, fmt.Errorf("planning engine not available")
	}
	return ee.planningEngine.ExportMetrics()
}

// ResetMetrics resets all planning metrics
func (ee *EnhancedExecutionEngine) ResetMetrics() {
	if ee.planningEngine != nil {
		ee.planningEngine.ResetMetrics()
	}
}

// RegistryToolExecutor adapts the Registry to the ToolExecutor interface
type RegistryToolExecutor struct {
	registry *Registry
}

// ExecuteTool implements the ToolExecutor interface
func (rte *RegistryToolExecutor) ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*planning.ToolResult, error) {
	// Use the registry to execute the tool
	result, err := rte.registry.ExecuteTool(ctx, name, args)
	if err != nil {
		return nil, err
	}

	// Convert tools.ToolResult to planning.ToolResult
	planningResult := &planning.ToolResult{
		Success: result.Success,
		Content: result.Content,
		Error:   result.Error,
		Data:    result.Data,
	}

	return planningResult, nil
}

// Enhanced middleware for planning metrics collection

// PlanningMetricsMiddleware collects metrics for the planning system
type PlanningMetricsMiddleware struct {
	planningEngine *planning.PlanningEngine
}

// NewPlanningMetricsMiddleware creates middleware that feeds metrics to the planning engine
func NewPlanningMetricsMiddleware(planningEngine *planning.PlanningEngine) *PlanningMetricsMiddleware {
	return &PlanningMetricsMiddleware{
		planningEngine: planningEngine,
	}
}

func (pmm *PlanningMetricsMiddleware) BeforeExecution(ctx context.Context, call *ai.ToolCall) error {
	// No pre-execution actions needed
	return nil
}

func (pmm *PlanningMetricsMiddleware) AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error {
	if pmm.planningEngine == nil {
		return nil
	}

	// Extract metrics data
	success := result.Error == nil && result.Result != nil && result.Result.Success
	cost := result.Duration.Seconds() * 0.001 // Simple cost calculation

	// Get planning metrics collector
	metricsCollector := pmm.planningEngine.GetPerformanceMetrics()
	if metricsCollector != nil {
		// This would need to be adapted based on the actual metrics interface
		// For now, we log the metric collection
		log.Printf("Collected metrics for tool %s: duration=%v, success=%v, cost=%f",
			call.Name, result.Duration, success, cost)
	}

	return nil
}

// Factory function to create enhanced execution engine with default settings

// CreateEnhancedExecutionEngine creates an enhanced execution engine with sensible defaults
func CreateEnhancedExecutionEngine(registry *Registry) *EnhancedExecutionEngine {
	config := planning.GetDefaultConfig()

	// Adjust config based on available tools
	availableTools := registry.GetAvailableTools()
	if len(availableTools) > 10 {
		config.MaxParallel = 8 // More parallel execution for more tools
	}

	return NewEnhancedExecutionEngineWithConfig(
		registry,
		config.MaxParallel,
		config.ExecutionTimeout,
		config,
	)
}

// CreatePlanningEngineForRegistry creates a standalone planning engine for a registry
func CreatePlanningEngineForRegistry(registry *Registry) *planning.PlanningEngine {
	toolExecutor := &RegistryToolExecutor{registry: registry}
	return planning.NewPlanningEngine(toolExecutor)
}
