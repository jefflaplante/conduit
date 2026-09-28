package tools

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/debuglog"
)

// ExecuteToolCalls executes multiple tool calls with parallel support
func (e *ExecutionEngine) ExecuteToolCalls(ctx context.Context, calls []ai.ToolCall) ([]*ExecutionResult, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	// conduit-31jg.39: each call gets its own deadline in executeSingle
	// (callTimeout) instead of one e.timeout shared by the whole batch.
	results := make([]*ExecutionResult, len(calls))

	if len(calls) == 1 {
		// Single tool execution
		results[0] = e.executeSingle(ctx, calls[0])
	} else {
		// Parallel execution with controlled concurrency
		results = e.executeParallel(ctx, calls)
	}

	return results, nil
}

// executeSingle executes a single tool call
func (e *ExecutionEngine) executeSingle(ctx context.Context, call ai.ToolCall) (execResult *ExecutionResult) {
	start := time.Now()

	// conduit-31jg.47: middleware, hooks and event callbacks run outside
	// Registry.ExecuteTool's recover. A panic before the tool ran becomes an
	// error result; after it ran, the tool's result is kept (reporting a
	// failure would invite a retry of a side-effecting call).
	toolRan := false
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[ExecutionEngine] PANIC in execution pipeline for tool %q (tool_ran=%v): %v\n%s",
				call.Name, toolRan, rec, debug.Stack())
			if toolRan && execResult != nil && execResult.Result != nil {
				return
			}
			execResult = pipelinePanicResult(call, start, rec)
		}
	}()

	// Log tool name only to journal (never args at INFO)
	log.Printf("[ExecutionEngine] > Tool: %s", call.Name)

	// Capture full details in ring buffer (private, in-memory only)
	if e.debugBuffer != nil {
		e.debugBuffer.Add(debuglog.ToolStart(call.Name, call.Args))
	}

	// Verbose mode: also log args to journal (opt-in)
	if e.verboseLogging {
		log.Printf("[ExecutionEngine] Tool args: %v", call.Args)
	}

	// Create result structure
	execResult = &ExecutionResult{
		ToolCall:   &call,
		ExecutedAt: start,
	}

	// Notify tool event callback of start
	if cb := getToolEventCallback(ctx); cb != nil {
		cb(ToolEventInfo{
			ToolName:  call.Name,
			EventType: "start",
			Args:      call.Args,
		})
	}

	// Run pre-execution middleware
	for _, mw := range e.middleware {
		if err := mw.BeforeExecution(ctx, &call); err != nil {
			execResult.Error = fmt.Errorf("middleware error: %w", err)
			execResult.Duration = time.Since(start)
			// Notify callback of error
			if cb := getToolEventCallback(ctx); cb != nil {
				cb(ToolEventInfo{
					ToolName:  call.Name,
					EventType: "error",
					Error:     err.Error(),
					Duration:  execResult.Duration,
				})
			}
			return execResult
		}
	}

	// Execute tool under its own deadline (conduit-31jg.39), capped to the
	// remaining shutdown drain budget once draining (conduit-31jg.88).
	timeout, draining := drainCappedTimeout(ctx, e.callTimeout(call))
	callCtx, cancelCall := context.WithTimeout(ctx, timeout)
	result, err := e.registry.ExecuteTool(callCtx, call.Name, call.Args)
	cancelCall()
	toolRan = true
	execResult.Result = result
	execResult.Error = err
	execResult.Duration = time.Since(start)

	// Handle execution errors gracefully
	if err != nil {
		log.Printf("[ExecutionEngine] < Tool: %s (%s) ERROR", call.Name, execResult.Duration)
		log.Printf("Tool execution failed: tool=%s error=%v", call.Name, err)
		// Record error in ring buffer
		if e.debugBuffer != nil {
			e.debugBuffer.Add(debuglog.ToolError(call.Name, execResult.Duration, err.Error()))
		}
		// Track consecutive failures for pivot detection (per-turn, conduit-31jg.13)
		chainStateFrom(ctx).recordOutcome(call, result, err)
		// Create a user-friendly error result
		if execResult.Result == nil {
			execResult.Result = &ToolResult{
				Success: false,
				Error:   err.Error(),
				Content: fmt.Sprintf("Tool '%s' failed: %s", call.Name, err.Error()),
			}
		}
		// Notify callback of error
		if cb := getToolEventCallback(ctx); cb != nil {
			cb(ToolEventInfo{
				ToolName:  call.Name,
				EventType: "error",
				Error:     err.Error(),
				Duration:  execResult.Duration,
			})
		}
	} else {
		log.Printf("[ExecutionEngine] < Tool: %s (%s)", call.Name, execResult.Duration)
		// Record completion in ring buffer (truncated result)
		if e.debugBuffer != nil {
			summary := ""
			if result != nil {
				summary = result.Content
				if len(summary) > 500 {
					summary = summary[:500] + "…"
				}
			}
			e.debugBuffer.Add(debuglog.ToolComplete(call.Name, execResult.Duration, summary))
		}
		// Notify callback of completion
		if cb := getToolEventCallback(ctx); cb != nil {
			resultStr := ""
			if result != nil {
				resultStr = result.Content
			}
			cb(ToolEventInfo{
				ToolName:  call.Name,
				EventType: "complete",
				Result:    resultStr,
				Duration:  execResult.Duration,
			})
		}
		// conduit-31jg.13: per-turn pattern + failure tracking. A result
		// with Success=false counts as a failure even with a nil error.
		chainStateFrom(ctx).recordOutcome(call, result, nil)
	}

	if draining {
		addDrainHint(execResult, timeout) // conduit-31jg.88
	}

	// Run post-execution middleware
	for _, mw := range e.middleware {
		mw.AfterExecution(ctx, &call, execResult)
	}

	// Fire reflection hook (best-effort, never blocks or fails the tool call)
	if e.afterExecHook != nil {
		e.afterExecHook(ctx, call.Name, execResult)
	}

	return execResult
}

// pipelinePanicResult builds the error result for a panic in the execution
// pipeline (conduit-31jg.47).
func pipelinePanicResult(call ai.ToolCall, start time.Time, rec interface{}) *ExecutionResult {
	err := fmt.Errorf("tool %q: panic in execution pipeline: %v", call.Name, rec)
	return &ExecutionResult{
		ToolCall:   &call,
		Error:      err,
		Duration:   time.Since(start),
		ExecutedAt: start,
		Result: &ToolResult{
			Success: false,
			Error:   err.Error(),
			Content: fmt.Sprintf("Tool '%s' failed: %s", call.Name, err.Error()),
		},
	}
}
