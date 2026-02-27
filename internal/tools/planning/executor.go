package planning

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// ParallelExecutor executes tool plans with advanced optimization and monitoring
type ParallelExecutor struct {
	planner     *ExecutionPlanner
	cache       *ResultCache
	metrics     *MetricsCollector
	semaphore   chan struct{}
	maxParallel int
	retryConfig *RetryConfig
}

// RetryConfig defines retry behavior for tool execution
type RetryConfig struct {
	MaxRetries      int           `json:"max_retries"`
	BaseDelay       time.Duration `json:"base_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
	BackoffStrategy string        `json:"backoff_strategy"` // "exponential", "linear", "fixed"
	RetryableErrors []string      `json:"retryable_errors"`
}

// StepExecutionResult contains the result of executing a single step
type StepExecutionResult struct {
	StepID string
	Result *StepResult
	Error  error
}

// ToolExecutor defines the interface for executing individual tools
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error)
}

// ToolResult represents the result of a tool execution (from existing codebase)
type ToolResult struct {
	Success      bool                   `json:"success"`
	Content      string                 `json:"content"`
	Error        string                 `json:"error,omitempty"`
	Data         map[string]interface{} `json:"data,omitempty"`
	FallbackUsed bool                   `json:"fallback_used,omitempty"`
	CacheHit     bool                   `json:"cache_hit,omitempty"`
	Retries      int                    `json:"retries,omitempty"`
}

// NewParallelExecutor creates a new parallel executor
func NewParallelExecutor(planner *ExecutionPlanner, cache *ResultCache, metrics *MetricsCollector, maxParallel int) *ParallelExecutor {
	return &ParallelExecutor{
		planner:     planner,
		cache:       cache,
		metrics:     metrics,
		semaphore:   make(chan struct{}, maxParallel),
		maxParallel: maxParallel,
		retryConfig: &RetryConfig{
			MaxRetries:      3,
			BaseDelay:       time.Second,
			MaxDelay:        time.Second * 30,
			BackoffStrategy: "exponential",
			RetryableErrors: []string{"timeout", "network", "rate_limit", "temporary"},
		},
	}
}

// ExecutePlan executes an optimized execution plan
func (pe *ParallelExecutor) ExecutePlan(ctx context.Context, plan *ExecutionPlan, toolExecutor ToolExecutor) (*PlanResult, error) {
	log.Printf("Executing plan %s with %d steps in %d parallel groups",
		plan.ID, len(plan.Steps), len(plan.Parallel))

	result := &PlanResult{
		PlanID:      plan.ID,
		StepResults: make(map[string]*StepResult),
		StartTime:   time.Now(),
		TotalSteps:  len(plan.Steps),
		Success:     true,
	}

	// Execute parallel groups in sequence
	for groupIndex, parallelGroup := range plan.Parallel {
		log.Printf("Executing parallel group %d with %d steps", groupIndex, len(parallelGroup))

		groupResults, err := pe.executeParallelGroup(ctx, parallelGroup, plan, result, toolExecutor)
		if err != nil {
			result.Success = false
			log.Printf("Parallel group %d failed: %v", groupIndex, err)
			// Continue with remaining groups for partial results
		}

		// Merge group results into plan results
		for stepID, stepResult := range groupResults {
			result.StepResults[stepID] = stepResult
			if !stepResult.Success {
				result.Success = false
				result.FailedSteps = append(result.FailedSteps, stepID)
			}
			if stepResult.CacheHit {
				result.CacheHits++
			}
		}
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	// Record plan execution metrics
	if pe.metrics != nil {
		pe.metrics.RecordPlanExecution(result, plan.Estimated, []string{plan.OptimizedFor})
	}

	log.Printf("Plan %s completed: success=%v, duration=%v, cache_hits=%d/%d",
		plan.ID, result.Success, result.Duration, result.CacheHits, result.TotalSteps)

	return result, nil
}

// executeParallelGroup executes a group of steps that can run in parallel
func (pe *ParallelExecutor) executeParallelGroup(ctx context.Context, stepIDs []string, plan *ExecutionPlan, prevResults *PlanResult, toolExecutor ToolExecutor) (map[string]*StepResult, error) {
	results := make(map[string]*StepResult)

	if len(stepIDs) == 1 {
		// Single step execution
		step := plan.GetStep(stepIDs[0])
		result, err := pe.executeStepWithOptimizations(ctx, step, prevResults, toolExecutor)
		if err != nil {
			return nil, fmt.Errorf("step %s failed: %w", step.ID, err)
		}
		results[step.ID] = result
		return results, nil
	}

	// Parallel execution
	var wg sync.WaitGroup
	resultChan := make(chan StepExecutionResult, len(stepIDs))
	errorChan := make(chan error, len(stepIDs))

	for _, stepID := range stepIDs {
		step := plan.GetStep(stepID)

		wg.Add(1)
		go func(s ExecutionStep) {
			defer wg.Done()

			// Acquire semaphore for concurrency control
			select {
			case pe.semaphore <- struct{}{}:
				defer func() { <-pe.semaphore }()
			case <-ctx.Done():
				errorChan <- ctx.Err()
				return
			}

			result, err := pe.executeStepWithOptimizations(ctx, s, prevResults, toolExecutor)
			resultChan <- StepExecutionResult{
				StepID: s.ID,
				Result: result,
				Error:  err,
			}
		}(step)
	}

	// Wait for all steps to complete
	go func() {
		wg.Wait()
		close(resultChan)
		close(errorChan)
	}()

	// Collect results
	var errors []error
	for sr := range resultChan {
		if sr.Error != nil {
			errors = append(errors, fmt.Errorf("step %s: %w", sr.StepID, sr.Error))
		} else {
			results[sr.StepID] = sr.Result
		}
	}

	// Collect any context errors
	for err := range errorChan {
		errors = append(errors, err)
	}

	// Return first error if any occurred
	if len(errors) > 0 {
		return results, errors[0] // Return partial results even on error
	}

	return results, nil
}

// executeStepWithOptimizations executes a single step with caching, retries, and fallbacks
func (pe *ParallelExecutor) executeStepWithOptimizations(ctx context.Context, step ExecutionStep, prevResults *PlanResult, toolExecutor ToolExecutor) (*StepResult, error) {
	startTime := time.Now()

	// Check cache first
	if step.CacheKey != "" && pe.cache != nil {
		if cachedResult, found := pe.cache.Get(ctx, step.CacheKey); found {
			log.Printf("Cache hit for step %s (tool: %s)", step.ID, step.ToolName)

			// Update cache hit timestamp
			cachedResult.ExecutedAt = startTime
			cachedResult.CacheHit = true

			return cachedResult, nil
		}
	}

	// Execute with retries
	result, err := pe.executeStepWithRetry(ctx, step, toolExecutor)

	// If main step failed and fallbacks exist, try fallbacks
	if err != nil && len(step.Fallbacks) > 0 {
		log.Printf("Step %s failed, trying %d fallbacks", step.ID, len(step.Fallbacks))

		for i, fallback := range step.Fallbacks {
			fallbackResult, fallbackErr := pe.executeStepWithRetry(ctx, fallback, toolExecutor)
			if fallbackErr == nil {
				// Fallback succeeded
				result = fallbackResult
				result.FallbackUsed = true
				err = nil
				log.Printf("Fallback %d succeeded for step %s", i+1, step.ID)
				break
			}
			log.Printf("Fallback %d failed for step %s: %v", i+1, step.ID, fallbackErr)
		}
	}

	duration := time.Since(startTime)

	// Create step result
	stepResult := &StepResult{
		StepID:     step.ID,
		ToolName:   step.ToolName,
		Success:    err == nil && result != nil,
		Duration:   duration,
		ExecutedAt: startTime,
		CacheHit:   false,
	}

	if result != nil {
		stepResult.Content = result.Content
		stepResult.Data = result.Data
		if !result.Success && result.Error != "" {
			stepResult.Error = result.Error
			stepResult.Success = false
		}
	}

	if err != nil {
		stepResult.Error = err.Error()
		stepResult.Success = false
	}

	// Cache successful results
	if stepResult.Success && step.CacheKey != "" && pe.cache != nil {
		if cacheErr := pe.cache.Set(ctx, step.CacheKey, step.ToolName, step.Args, stepResult); cacheErr != nil {
			log.Printf("Failed to cache result for step %s: %v", step.ID, cacheErr)
		}
	}

	// Record metrics
	if pe.metrics != nil {
		cost := step.CostWeight * duration.Seconds() // Simple cost calculation
		pe.metrics.RecordExecution(step.ToolName, duration, stepResult.Success, stepResult.CacheHit, stepResult.Retries, stepResult.FallbackUsed, cost, err)
	}

	return stepResult, err
}

// executeStepWithRetry executes a step with retry logic
func (pe *ParallelExecutor) executeStepWithRetry(ctx context.Context, step ExecutionStep, toolExecutor ToolExecutor) (*ToolResult, error) {
	var lastErr error
	retries := 0

	maxRetries := step.Retries
	if maxRetries <= 0 {
		maxRetries = pe.retryConfig.MaxRetries
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Calculate delay for retry
			delay := pe.calculateRetryDelay(attempt)

			log.Printf("Retrying step %s (attempt %d/%d) after %v", step.ID, attempt, maxRetries, delay)

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			retries++
		}

		// Create timeout context for this attempt
		timeoutCtx, cancel := context.WithTimeout(ctx, step.Timeout)

		result, err := toolExecutor.ExecuteTool(timeoutCtx, step.ToolName, step.Args)
		cancel() // Clean up timeout context

		if err == nil {
			// Success
			if pe.metrics != nil && retries > 0 {
				// Record successful retry
			}
			return result, nil
		}

		lastErr = err

		// Check if error is retryable
		if !pe.isRetryableError(err) {
			log.Printf("Non-retryable error for step %s: %v", step.ID, err)
			break
		}

		log.Printf("Retryable error for step %s (attempt %d): %v", step.ID, attempt+1, err)
	}

	// All retries exhausted
	log.Printf("Step %s failed after %d retries: %v", step.ID, retries, lastErr)
	return nil, fmt.Errorf("failed after %d retries: %w", retries, lastErr)
}

// calculateRetryDelay calculates the delay for a retry attempt
func (pe *ParallelExecutor) calculateRetryDelay(attempt int) time.Duration {
	baseDelay := pe.retryConfig.BaseDelay
	maxDelay := pe.retryConfig.MaxDelay

	var delay time.Duration

	switch pe.retryConfig.BackoffStrategy {
	case "exponential":
		// Exponential backoff: baseDelay * 2^attempt
		delay = time.Duration(int64(baseDelay) << uint(attempt-1))
	case "linear":
		// Linear backoff: baseDelay * attempt
		delay = time.Duration(int64(baseDelay) * int64(attempt))
	case "fixed":
		// Fixed delay
		delay = baseDelay
	default:
		// Default to exponential
		delay = time.Duration(int64(baseDelay) << uint(attempt-1))
	}

	// Cap at maximum delay
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add small jitter to avoid thundering herd
	jitter := time.Duration(float64(delay) * 0.1 * (0.5 - float64(time.Now().UnixNano()%2)))
	delay += jitter

	return delay
}

// isRetryableError checks if an error should trigger a retry
func (pe *ParallelExecutor) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	for _, retryableError := range pe.retryConfig.RetryableErrors {
		if contains(errStr, retryableError) {
			return true
		}
	}

	// Default retryable patterns
	retryablePatterns := []string{
		"timeout",
		"deadline exceeded",
		"connection refused",
		"temporary failure",
		"rate limit",
		"503",
		"502",
		"500", // Server errors are often retryable
	}

	for _, pattern := range retryablePatterns {
		if contains(errStr, pattern) {
			return true
		}
	}

	return false
}

// SetRetryConfig updates the retry configuration
func (pe *ParallelExecutor) SetRetryConfig(config *RetryConfig) {
	pe.retryConfig = config
	log.Printf("Updated retry configuration: max_retries=%d, base_delay=%v", config.MaxRetries, config.BaseDelay)
}

// GetMetrics returns current execution metrics
func (pe *ParallelExecutor) GetMetrics() *MetricsCollector {
	return pe.metrics
}

// SetMaxParallel updates the maximum parallel execution limit
func (pe *ParallelExecutor) SetMaxParallel(maxParallel int) {
	pe.maxParallel = maxParallel
	// Recreate semaphore with new capacity
	pe.semaphore = make(chan struct{}, maxParallel)
	log.Printf("Updated max parallel execution to %d", maxParallel)
}
