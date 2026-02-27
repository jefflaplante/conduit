package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"conduit/internal/ai"
)

// ToolRegistry interface for tool execution
type ToolRegistry interface {
	ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error)
}

// toolEventCallbackKey is the context key for tool event callbacks
type toolEventCallbackKey struct{}

// ToolEventInfo contains information about a tool execution event
type ToolEventInfo struct {
	ToolName  string
	EventType string // "start", "complete", "error"
	Args      map[string]interface{}
	Result    string
	Error     string
	Duration  time.Duration
}

// ToolEventCallback is called during tool execution to notify listeners
type ToolEventCallback func(event ToolEventInfo)

// WithToolEventCallback returns a context with a tool event callback attached
func WithToolEventCallback(ctx context.Context, cb ToolEventCallback) context.Context {
	return context.WithValue(ctx, toolEventCallbackKey{}, cb)
}

// getToolEventCallback extracts the tool event callback from context, if any
func getToolEventCallback(ctx context.Context) ToolEventCallback {
	cb, _ := ctx.Value(toolEventCallbackKey{}).(ToolEventCallback)
	return cb
}

// ExecutionEngine handles tool execution, chaining, and middleware
type ExecutionEngine struct {
	registry    ToolRegistry
	middleware  []Middleware
	maxParallel int
	timeout     time.Duration
	maxChains   int // Prevent infinite tool chains
}

// Middleware interface for tool execution pipeline
type Middleware interface {
	BeforeExecution(ctx context.Context, call *ai.ToolCall) error
	AfterExecution(ctx context.Context, call *ai.ToolCall, result *ExecutionResult) error
}

// ExecutionResult wraps tool results with metadata
type ExecutionResult struct {
	ToolCall   *ai.ToolCall  `json:"tool_call"`
	Result     *ToolResult   `json:"result"`
	Error      error         `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
	ExecutedAt time.Time     `json:"executed_at"`
}

// ConversationResponse represents the complete response after tool execution
type ConversationResponse struct {
	Content     string             `json:"content"`
	Usage       *ai.Usage          `json:"usage,omitempty"`
	Steps       int                `json:"steps"`
	ToolResults []*ExecutionResult `json:"tool_results,omitempty"`
	ChainDepth  int                `json:"chain_depth"`
}

// NewExecutionEngine creates a new tool execution engine
func NewExecutionEngine(registry ToolRegistry, maxParallel int, timeout time.Duration, maxChains int) *ExecutionEngine {
	// Default to 25 if not specified or invalid
	if maxChains <= 0 {
		maxChains = 25
	}

	return &ExecutionEngine{
		registry:    registry,
		middleware:  []Middleware{},
		maxParallel: maxParallel,
		timeout:     timeout,
		maxChains:   maxChains,
	}
}

// AddMiddleware adds middleware to the execution pipeline
func (e *ExecutionEngine) AddMiddleware(mw Middleware) {
	e.middleware = append(e.middleware, mw)
}

// ExecuteToolCalls executes multiple tool calls with parallel support
func (e *ExecutionEngine) ExecuteToolCalls(ctx context.Context, calls []ai.ToolCall) ([]*ExecutionResult, error) {
	if len(calls) == 0 {
		return nil, nil
	}

	// Add timeout to context
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

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
func (e *ExecutionEngine) executeSingle(ctx context.Context, call ai.ToolCall) *ExecutionResult {
	start := time.Now()
	log.Printf("[ExecutionEngine] Executing tool: %s with args: %v", call.Name, call.Args)

	// Create result structure
	execResult := &ExecutionResult{
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

	// Execute tool
	result, err := e.registry.ExecuteTool(ctx, call.Name, call.Args)
	execResult.Result = result
	execResult.Error = err
	execResult.Duration = time.Since(start)

	// Handle execution errors gracefully
	if err != nil {
		log.Printf("Tool execution failed: tool=%s error=%v", call.Name, err)
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
	}

	// Run post-execution middleware
	for _, mw := range e.middleware {
		mw.AfterExecution(ctx, &call, execResult)
	}

	return execResult
}

// executeParallel executes multiple tools in parallel with controlled concurrency
func (e *ExecutionEngine) executeParallel(ctx context.Context, calls []ai.ToolCall) []*ExecutionResult {
	results := make([]*ExecutionResult, len(calls))

	// Use worker pool for controlled concurrency
	semaphore := make(chan struct{}, e.maxParallel)
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(idx int, toolCall ai.ToolCall) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			results[idx] = e.executeSingle(ctx, toolCall)
		}(i, call)
	}

	wg.Wait()
	return results
}

// HandleToolCallFlow manages the complete tool calling conversation flow
func (e *ExecutionEngine) HandleToolCallFlow(
	ctx context.Context,
	provider ai.Provider,
	initialReq *ai.GenerateRequest,
	initialResp *ai.GenerateResponse,
) (*ConversationResponse, error) {
	log.Printf("[ExecutionEngine] HandleToolCallFlow called with %d tool calls", len(initialResp.ToolCalls))
	for i, tc := range initialResp.ToolCalls {
		log.Printf("[ExecutionEngine] Tool call %d: %s", i, tc.Name)
	}
	return e.handleToolCallFlowRecursive(ctx, provider, initialReq, initialResp, 0)
}

// handleToolCallFlowRecursive handles tool chaining with depth limits
func (e *ExecutionEngine) handleToolCallFlowRecursive(
	ctx context.Context,
	provider ai.Provider,
	initialReq *ai.GenerateRequest,
	initialResp *ai.GenerateResponse,
	depth int,
) (*ConversationResponse, error) {
	// Prevent infinite tool chains
	if depth >= e.maxChains {
		log.Printf("Tool chain depth limit reached: %d/%d", depth, e.maxChains)

		// Create helpful message for the LLM about hitting the limit
		limitMessage := fmt.Sprintf(
			"%s\n\n**Tool chain limit reached (%d steps).** "+
				"I've completed %d tool operations but reached the maximum allowed chain length. "+
				"This prevents runaway tool usage while still allowing complex workflows. "+
				"If you need to continue, you can:\n"+
				"- Ask me to pick up where I left off with a more focused approach\n"+
				"- Break the task into smaller steps\n"+
				"- Increase the `max_tool_chains` setting in config.json if this limit is too restrictive",
			initialResp.Content, e.maxChains, depth,
		)

		return &ConversationResponse{
			Content:    limitMessage,
			Usage:      &initialResp.Usage,
			Steps:      depth + 1,
			ChainDepth: depth,
		}, nil
	}

	// Start conversation history with initial request/response
	conversationHistory := append(initialReq.Messages, ai.ChatMessage{
		Role:      "assistant",
		Content:   initialResp.Content,
		ToolCalls: initialResp.ToolCalls,
	})

	// Execute tools
	toolResults, err := e.ExecuteToolCalls(ctx, initialResp.ToolCalls)
	if err != nil {
		return nil, fmt.Errorf("tool execution failed: %w", err)
	}

	// Add tool results to conversation
	for _, result := range toolResults {
		// Format tool result for AI consumption
		content := e.formatToolResultForAI(result)

		conversationHistory = append(conversationHistory, ai.ChatMessage{
			Role:       "tool",
			Content:    content,
			ToolCallID: result.ToolCall.ID,
		})
	}

	// Get final AI response with tool results
	finalReq := &ai.GenerateRequest{
		Messages:  conversationHistory,
		Model:     initialReq.Model,
		Tools:     initialReq.Tools,
		MaxTokens: initialReq.MaxTokens,
	}

	finalResp, err := provider.GenerateResponse(ctx, finalReq)
	if err != nil {
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// Check for additional tool calls (tool chaining)
	if len(finalResp.ToolCalls) > 0 {
		// Recursive tool calling with depth tracking
		return e.handleToolCallFlowRecursive(ctx, provider, finalReq, finalResp, depth+1)
	}

	// No more tool calls - return final response
	return &ConversationResponse{
		Content:     finalResp.Content,
		Usage:       e.combineUsage(&initialResp.Usage, &finalResp.Usage),
		Steps:       2 + depth, // Initial + final + any recursive steps
		ToolResults: toolResults,
		ChainDepth:  depth,
	}, nil
}

// formatToolResultForAI formats tool results for AI consumption
func (e *ExecutionEngine) formatToolResultForAI(result *ExecutionResult) string {
	if result.Error != nil {
		return fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Error.Error())
	}

	if result.Result == nil {
		return fmt.Sprintf("Tool '%s' executed but returned no result", result.ToolCall.Name)
	}

	if !result.Result.Success {
		return fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Result.Error)
	}

	// Return the content, with metadata if available
	content := result.Result.Content
	if len(result.Result.Data) > 0 {
		// Add structured data as JSON for AI context
		if dataJSON, err := json.Marshal(result.Result.Data); err == nil {
			content += fmt.Sprintf("\n\nStructured data: %s", string(dataJSON))
		}
	}

	return content
}

// combineUsage combines usage statistics from multiple AI calls
func (e *ExecutionEngine) combineUsage(usage1, usage2 *ai.Usage) *ai.Usage {
	if usage1 == nil && usage2 == nil {
		return nil
	}
	if usage1 == nil {
		return usage2
	}
	if usage2 == nil {
		return usage1
	}

	return &ai.Usage{
		PromptTokens:     usage1.PromptTokens + usage2.PromptTokens,
		CompletionTokens: usage1.CompletionTokens + usage2.CompletionTokens,
		TotalTokens:      usage1.TotalTokens + usage2.TotalTokens,
	}
}

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
	lm.logger("Executing tool: %s with args: %v", call.Name, call.Args)
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
