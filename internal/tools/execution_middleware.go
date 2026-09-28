package tools

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/ai"
)

// Built-in middleware implementations

// LoggingMiddleware logs tool execution for monitoring
type LoggingMiddleware struct {
	logger func(format string, args ...interface{})
}

func NewLoggingMiddleware() *LoggingMiddleware {
	return &LoggingMiddleware{
		logger: log.Printf,
	}
}

func (lm *LoggingMiddleware) BeforeExecution(ctx context.Context, call *ai.ToolCall) error {
	lm.logger("Executing tool: %s", call.Name)
	return nil
}

func (lm *LoggingMiddleware) AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error {
	success := result.Error == nil && result.Result != nil && result.Result.Success
	lm.logger("Tool %s completed: success=%t duration=%v", call.Name, success, result.Duration)
	return nil
}

// SecurityMiddleware enforces tool execution policies
type SecurityMiddleware struct {
	allowedTools map[string]bool
}

func NewSecurityMiddleware(allowedTools []string) *SecurityMiddleware {
	allowed := make(map[string]bool)
	for _, tool := range allowedTools {
		allowed[tool] = true
	}
	return &SecurityMiddleware{
		allowedTools: allowed,
	}
}

func (sm *SecurityMiddleware) BeforeExecution(ctx context.Context, call *ai.ToolCall) error {
	if len(sm.allowedTools) > 0 && !sm.allowedTools[call.Name] {
		return fmt.Errorf("tool '%s' not allowed by security policy", call.Name)
	}
	return nil
}

func (sm *SecurityMiddleware) AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error {
	// Post-execution security checks can be added here
	return nil
}

// MetricsMiddleware collects execution metrics
type MetricsMiddleware struct {
	executionCount map[string]int
	totalDuration  map[string]time.Duration
	mu             sync.RWMutex
}

func NewMetricsMiddleware() *MetricsMiddleware {
	return &MetricsMiddleware{
		executionCount: make(map[string]int),
		totalDuration:  make(map[string]time.Duration),
	}
}

func (mm *MetricsMiddleware) BeforeExecution(ctx context.Context, call *ai.ToolCall) error {
	return nil
}

func (mm *MetricsMiddleware) AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	mm.executionCount[call.Name]++
	mm.totalDuration[call.Name] += result.Duration

	return nil
}

// GetMetrics returns execution metrics
func (mm *MetricsMiddleware) GetMetrics() map[string]interface{} {
	mm.mu.RLock()
	defer mm.mu.RUnlock()

	metrics := make(map[string]interface{})
	for tool, count := range mm.executionCount {
		avgDuration := mm.totalDuration[tool] / time.Duration(count)
		metrics[tool] = map[string]interface{}{
			"count":            count,
			"total_duration":   mm.totalDuration[tool].String(),
			"average_duration": avgDuration.String(),
		}
	}

	return metrics
}
