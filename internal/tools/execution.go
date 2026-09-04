package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/debuglog"
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
	EventType string // "start", "complete", "error", "thinking"
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

// startThinkingIndicator emits periodic "thinking" events via the tool event callback.
// Returns a stop function that must be called when the LLM responds.
func startThinkingIndicator(ctx context.Context, depth int) func() {
	cb := getToolEventCallback(ctx)
	if cb == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		msg := thinkingMessage(depth)
		// Thinking indicators go to ring buffer only (pure noise in journal)
		cb(ToolEventInfo{
			ToolName:  msg,
			EventType: "thinking",
		})
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				msg := thinkingMessage(depth)
				cb(ToolEventInfo{
					ToolName:  msg,
					EventType: "thinking",
				})
			}
		}
	}()
	return func() { close(done) }
}

func thinkingMessage(depth int) string {
	if depth == 0 {
		return "Thinking..."
	}
	return fmt.Sprintf("Thinking (step %d)...", depth+1)
}

// DefaultMaxToolResultChars is the default max characters for tool result content.
const DefaultMaxToolResultChars = 8192

// TruncationConfig controls smart truncation behavior for tool results.
type TruncationConfig struct {
	MaxChars         int      // Maximum characters for tool result (0 = use DefaultMaxToolResultChars)
	HeadLines        int      // Number of lines to preserve from the start (default 20)
	TailLines        int      // Number of lines to preserve from the end (default 20)
	PreservePatterns []string // Patterns to preserve (case-insensitive matching)
}

// DefaultTruncationConfig returns the default truncation configuration.
func DefaultTruncationConfig() TruncationConfig {
	return TruncationConfig{
		MaxChars:  DefaultMaxToolResultChars,
		HeadLines: 20,
		TailLines: 20,
		PreservePatterns: []string{
			"error", "Error", "ERROR",
			"fail", "Fail", "FAIL",
			"exception", "Exception", "EXCEPTION",
			"denied", "Denied", "DENIED",
			"timeout", "Timeout", "TIMEOUT",
			"panic", "Panic", "PANIC",
			"fatal", "Fatal", "FATAL",
			"warning", "Warning", "WARNING",
		},
	}
}

// AfterExecutionFunc is an optional callback invoked after each tool
// execution completes (success or failure). It is used by the reflection
// subsystem to capture tool outcomes without a hard dependency on the
// reflection package. The callback must be safe for concurrent use.
type AfterExecutionFunc func(ctx context.Context, toolName string, result *ExecutionResult)

// ExecutionEngine handles tool execution, chaining, and middleware
type ExecutionEngine struct {
	registry         ToolRegistry
	middleware       []Middleware
	maxParallel      int
	timeout          time.Duration
	maxChains        int                  // Prevent infinite tool chains
	maxResultChars   int                  // Max chars for tool result content (0 = use default)
	truncationConfig TruncationConfig     // Smart truncation configuration
	debugBuffer      *debuglog.RingBuffer // In-memory ring buffer for debug entries (nil-safe)
	verboseLogging   bool                 // When true, log full args to journal
	refocusInterval  int                  // Inject goal reminder every N tool calls (0 = disabled, default 10)
	patternTracker   *PatternTracker      // Detects circular tool call patterns
	failureTracker   *FailureTracker      // Tracks consecutive tool failures for pivot prompts
	afterExecHook    AfterExecutionFunc   // Optional hook for reflection capture (nil-safe)
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

// NewExecutionEngine creates a new tool execution engine.
// debugBuffer may be nil (debug logging disabled). verboseLogging controls journal output.
func NewExecutionEngine(registry ToolRegistry, maxParallel int, timeout time.Duration, maxChains int) *ExecutionEngine {
	// Default to 25 if not specified or invalid
	if maxChains <= 0 {
		maxChains = 25
	}

	return &ExecutionEngine{
		registry:         registry,
		middleware:       []Middleware{},
		maxParallel:      maxParallel,
		timeout:          timeout,
		maxChains:        maxChains,
		maxResultChars:   DefaultMaxToolResultChars,
		truncationConfig: DefaultTruncationConfig(),
		refocusInterval:  10, // Default: remind of goal every 10 tool calls
		patternTracker:   NewPatternTracker(10),
		failureTracker:   NewFailureTracker(3),
	}
}

// SetDebugBuffer configures the in-memory ring buffer for debug log entries.
func (e *ExecutionEngine) SetDebugBuffer(buf *debuglog.RingBuffer) {
	e.debugBuffer = buf
}

// SetVerboseLogging controls whether full tool args are logged to the journal.
func (e *ExecutionEngine) SetVerboseLogging(v bool) {
	e.verboseLogging = v
}

// SetMaxResultChars configures the maximum characters for tool result content.
func (e *ExecutionEngine) SetMaxResultChars(maxChars int) {
	if maxChars > 0 {
		e.maxResultChars = maxChars
	}
}

// SetRefocusInterval configures how often (every N tool calls) a goal reminder is injected.
// Set to 0 to disable refocusing. Default is 10.
func (e *ExecutionEngine) SetRefocusInterval(n int) {
	e.refocusInterval = n
}

// SetAfterExecutionHook registers a callback that fires after every tool
// execution. It is intended for the reflection middleware to capture tool
// outcomes. Only one hook can be active; subsequent calls replace the
// previous hook. Pass nil to remove the hook.
func (e *ExecutionEngine) SetAfterExecutionHook(fn AfterExecutionFunc) {
	e.afterExecHook = fn
}

// SetTruncationConfig configures smart truncation behavior for tool results.
func (e *ExecutionEngine) SetTruncationConfig(cfg TruncationConfig) {
	e.truncationConfig = cfg
	// Also update maxResultChars if MaxChars is specified in config
	if cfg.MaxChars > 0 {
		e.maxResultChars = cfg.MaxChars
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
		log.Printf("[ExecutionEngine] < Tool: %s (%s) ERROR", call.Name, execResult.Duration)
		log.Printf("Tool execution failed: tool=%s error=%v", call.Name, err)
		// Record error in ring buffer
		if e.debugBuffer != nil {
			e.debugBuffer.Add(debuglog.ToolError(call.Name, execResult.Duration, err.Error()))
		}
		// Track consecutive failures for pivot detection
		if e.failureTracker != nil {
			e.failureTracker.RecordFailure(call.Name, err.Error())
		}
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
		// Record successful call for pattern detection (with args for accurate detection)
		if e.patternTracker != nil {
			e.patternTracker.RecordCall(call.Name, call.Args)
		}
		// Reset consecutive failure count on success
		if e.failureTracker != nil {
			e.failureTracker.RecordSuccess(call.Name)
		}
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
	return e.handleToolCallFlowRecursive(ctx, provider, initialReq, initialResp, 0, time.Now())
}

// handleToolCallFlowRecursive handles tool chaining with depth limits
func (e *ExecutionEngine) handleToolCallFlowRecursive(
	ctx context.Context,
	provider ai.Provider,
	initialReq *ai.GenerateRequest,
	initialResp *ai.GenerateResponse,
	depth int,
	chainStart time.Time,
) (*ConversationResponse, error) {
	// conduit-1z6d: hard wall-clock cap for the whole chain. On breach, stop
	// recursing and return a user-visible timeout message as normal content —
	// never a silent end.
	if timeoutMsg, timedOut := watchdogTimeoutResponse(chainStart); timedOut {
		log.Printf("[Watchdog] chain hard cap exceeded at depth %d (elapsed %s) — aborting with visible timeout (conduit-1z6d)",
			depth, time.Since(chainStart).Round(time.Second))
		return &ConversationResponse{
			Content:    timeoutMsg,
			Usage:      &initialResp.Usage,
			Steps:      depth + 1,
			ChainDepth: depth,
		}, nil
	}

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

	// Mid-chain goal refocusing: inject a reminder of the original goal at intervals
	var refocusMessage string
	if e.refocusInterval > 0 && depth > 0 && depth%e.refocusInterval == 0 {
		originalGoal := e.extractOriginalGoal(initialReq.Messages)
		if originalGoal != "" {
			refocusMessage = fmt.Sprintf(
				"Reminder: Your original goal was: %s. Stay focused on completing this.",
				originalGoal,
			)
			log.Printf("[ExecutionEngine] Injecting goal refocus at depth %d: %s", depth, originalGoal)
		}
	}

	// Start conversation history with initial request/response.
	// Use an explicit copy to avoid mutating the caller's slice when spare capacity exists.
	msgs := make([]ai.ChatMessage, len(initialReq.Messages))
	copy(msgs, initialReq.Messages)
	conversationHistory := append(msgs, ai.ChatMessage{
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

	// Inject goal refocus reminder if applicable
	if refocusMessage != "" {
		conversationHistory = append(conversationHistory, ai.ChatMessage{
			Role:    "system",
			Content: refocusMessage,
		})
	}

	// Check for consecutive tool failures and suggest pivoting if threshold reached
	if e.failureTracker != nil {
		failedTools := e.failureTracker.GetFailedTools()
		for _, toolName := range failedTools {
			pivotMsg := fmt.Sprintf(
				"Multiple failures detected with '%s'. Consider a different approach or tool.",
				toolName,
			)
			log.Printf("[ExecutionEngine] Injecting pivot suggestion for tool: %s", toolName)
			conversationHistory = append(conversationHistory, ai.ChatMessage{
				Role:    "system",
				Content: pivotMsg,
			})
		}
	}

	// Get final AI response with tool results
	finalReq := &ai.GenerateRequest{
		Messages:  conversationHistory,
		Model:     initialReq.Model,
		Tools:     initialReq.Tools,
		MaxTokens: initialReq.MaxTokens,
	}

	stopThinking := startThinkingIndicator(ctx, depth)
	rtStart := time.Now()
	finalResp, err := provider.GenerateResponse(ctx, finalReq)
	stopThinking()
	if err != nil {
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// conduit-18vj: raw-empty round trips after tool execution were the proven
	// dead-turn mechanism (2026-09-03) — retry once, then a visible fallback.
	finalResp, err = ai.GuardEmptyResponse(ctx, provider, finalReq, finalResp, err, fmt.Sprintf("depth%d", depth))

	// conduit-1z6d: per-round-trip instrumentation — dead turns diagnosable
	// from the journal alone.
	log.Printf("[RoundTrip] phase=post-tools depth=%d model=%q duration=%s prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d",
		depth, finalReq.Model, time.Since(rtStart).Round(time.Millisecond),
		finalResp.Usage.PromptTokens, finalResp.Usage.CompletionTokens,
		len(finalResp.Content), len(finalResp.ToolCalls))

	// Check for additional tool calls (tool chaining)
	if len(finalResp.ToolCalls) > 0 {
		// Check for circular tool call patterns before recursing
		if e.patternTracker != nil {
			if detected, pattern := e.patternTracker.DetectCircular(); detected {
				log.Printf("[ExecutionEngine] Circular pattern detected: %s", pattern)
				// Inject think step to force LLM to pause and reflect
				thinkMsg := InjectThinkStep(pattern)
				finalReq.Messages = append(finalReq.Messages, ai.ChatMessage{
					Role:    "system",
					Content: thinkMsg,
				})
			}
		}
		// Recursive tool calling with depth tracking
		return e.handleToolCallFlowRecursive(ctx, provider, finalReq, finalResp, depth+1, chainStart)
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
		msg := fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Result.Error)

		// Surface rich error details when available
		if details := result.Result.ErrorDetails; details != nil {
			if details.Type != "" {
				msg += fmt.Sprintf("\nError type: %s", details.Type)
			}
			if len(details.Suggestions) > 0 {
				msg += "\nSuggestions:"
				for _, s := range details.Suggestions {
					msg += fmt.Sprintf("\n- %s", s)
				}
			}
			if len(details.AvailableValues) > 0 {
				msg += fmt.Sprintf("\nAvailable values: %s", strings.Join(details.AvailableValues, ", "))
			}
			if len(details.Examples) > 0 {
				msg += fmt.Sprintf("\nExamples: %s", strings.Join(details.Examples, ", "))
			}
		}

		return msg
	}

	// Return the content, with metadata if available
	content := result.Result.Content
	if len(result.Result.Data) > 0 {
		// Add structured data as JSON for AI context
		if dataJSON, err := json.Marshal(result.Result.Data); err == nil {
			content += fmt.Sprintf("\n\nStructured data: %s", string(dataJSON))
		}
	}

	// Smart truncation to protect context window while preserving important content
	maxChars := e.maxResultChars
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}
	if len(content) > maxChars {
		content = e.smartTruncate(content, maxChars)
	}

	return content
}

// smartTruncate performs intelligent truncation preserving head, tail, and error lines.
func (e *ExecutionEngine) smartTruncate(content string, maxChars int) string {
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	cfg := e.truncationConfig
	headLines := cfg.HeadLines
	tailLines := cfg.TailLines
	if headLines <= 0 {
		headLines = 20
	}
	if tailLines <= 0 {
		tailLines = 20
	}

	// If content is small enough by line count, fall back to char-based truncation
	if totalLines <= headLines+tailLines {
		// Simple char truncation: keep first 80% and last 20%
		headSize := maxChars * 4 / 5
		tailSize := maxChars / 5
		return content[:headSize] +
			fmt.Sprintf("\n\n...(truncated, showing %d of %d chars)...\n\n", maxChars, len(content)) +
			content[len(content)-tailSize:]
	}

	// Collect head lines
	head := lines[:headLines]

	// Collect tail lines
	tail := lines[totalLines-tailLines:]

	// Find important lines in the middle section
	middleStart := headLines
	middleEnd := totalLines - tailLines
	var preservedMiddle []string

	for i := middleStart; i < middleEnd; i++ {
		if e.lineContainsPattern(lines[i]) {
			preservedMiddle = append(preservedMiddle, lines[i])
		}
	}

	// Calculate truncated line count
	truncatedCount := (middleEnd - middleStart) - len(preservedMiddle)

	// Build result
	var result strings.Builder
	result.WriteString(strings.Join(head, "\n"))

	if len(preservedMiddle) > 0 || truncatedCount > 0 {
		result.WriteString(fmt.Sprintf("\n\n[...truncated %d lines, preserved %d lines with errors/warnings...]\n\n",
			truncatedCount, len(preservedMiddle)))

		if len(preservedMiddle) > 0 {
			result.WriteString(strings.Join(preservedMiddle, "\n"))
			result.WriteString("\n\n[...end of preserved section...]\n\n")
		}
	} else {
		result.WriteString("\n")
	}

	result.WriteString(strings.Join(tail, "\n"))

	// Final char limit check
	finalContent := result.String()
	if len(finalContent) > maxChars {
		// Truncate preserved middle if still too long
		headSize := maxChars * 4 / 5
		tailSize := maxChars / 5
		return finalContent[:headSize] +
			fmt.Sprintf("\n\n...(final truncation, showing %d of %d chars)...\n\n", maxChars, len(finalContent)) +
			finalContent[len(finalContent)-tailSize:]
	}

	return finalContent
}

// lineContainsPattern checks if a line contains any of the configured preserve patterns.
func (e *ExecutionEngine) lineContainsPattern(line string) bool {
	for _, pattern := range e.truncationConfig.PreservePatterns {
		if strings.Contains(line, pattern) {
			return true
		}
	}
	return false
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

// extractOriginalGoal finds the original user goal from the message history.
// It looks for the last user message in the conversation, which typically
// contains the original request that initiated the tool chain.
func (e *ExecutionEngine) extractOriginalGoal(messages []ai.ChatMessage) string {
	// Search backwards to find the most recent user message
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" {
			goal := messages[i].Content
			// Truncate long goals to keep the reminder concise
			const maxGoalLen = 200
			if len(goal) > maxGoalLen {
				goal = goal[:maxGoalLen] + "..."
			}
			return goal
		}
	}
	return ""
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
