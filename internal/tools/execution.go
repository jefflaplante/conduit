package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"conduit/internal/ai"
	"conduit/internal/sandbox"
	"conduit/internal/tools/debuglog"
	"conduit/internal/tools/types"
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
	afterExecHook    AfterExecutionFunc   // Optional hook for reflection capture (nil-safe)
	// conduit-31jg.13: pattern/failure trackers are per-turn (chainState),
	// never engine-wide. These hooks are config: they forward per-turn
	// triggers to SPAR for cross-session learning.
	pivotHook    PivotHook
	circularHook CircularHook
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

// SetAfterExecutionHook registers a callback that fires after every tool
// execution. It is intended for the reflection middleware to capture tool
// outcomes. Only one hook can be active; subsequent calls replace the
// previous hook. Pass nil to remove the hook.
func (e *ExecutionEngine) SetAfterExecutionHook(fn AfterExecutionFunc) {
	e.afterExecHook = fn
}

// SetPivotHook registers the SPAR callback fired when a tool crosses the
// per-turn consecutive-failure threshold (conduit-17wz, conduit-31jg.13).
// Call during construction only.
func (e *ExecutionEngine) SetPivotHook(fn PivotHook) {
	e.pivotHook = fn
}

// SetCircularHook registers the SPAR callback fired when a per-turn circular
// tool-call pattern is detected (conduit-2ngi, conduit-31jg.13). Call during
// construction only.
func (e *ExecutionEngine) SetCircularHook(fn CircularHook) {
	e.circularHook = fn
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

// callTimeoutSlack lets a tool's own per-call timeout fire (and report a
// proper timeout result) before the engine deadline does.
const callTimeoutSlack = 5 * time.Second

// callTimeout returns the deadline for one call: the engine default, or
// longer when the tool honours a per-call `timeout` (Bash). conduit-31jg.39.
func (e *ExecutionEngine) callTimeout(call ai.ToolCall) time.Duration {
	d := e.timeout
	if d <= 0 {
		d = 60 * time.Second
	}
	if p, ok := e.registry.(interface {
		CallTimeout(name string, args map[string]interface{}) (time.Duration, bool)
	}); ok {
		if req, ok := p.CallTimeout(call.Name, call.Args); ok && req+callTimeoutSlack > d {
			d = req + callTimeoutSlack
		}
	}
	return d
}

// conduit-31jg.88: once the gateway drains for a restart, a tool call may
// run at most until the drain deadline minus drainToolMargin (never less
// than drainToolFloor): a `sleep 150` started mid-drain was force-cancelled
// with no chance for the model to wrap up.
const (
	drainToolMargin = 3 * time.Second
	drainToolFloor  = time.Second
)

// DrainToolHint is appended to every tool result produced during a drain.
const DrainToolHint = "[System: the gateway is restarting; wrap up now. Give the user a short status and do not start new long-running work. This tool call was limited to %s.]"

// drainCappedTimeout caps d to the remaining drain budget when ctx reports
// a shutdown drain (types.DrainDeadline). draining reports whether it did.
func drainCappedTimeout(ctx context.Context, d time.Duration) (time.Duration, bool) {
	deadline, ok := types.DrainDeadline(ctx)
	if !ok {
		return d, false
	}
	left := time.Until(deadline)
	capped := left - drainToolMargin
	if capped < drainToolFloor {
		capped = min(drainToolFloor, left)
	}
	if capped <= 0 {
		// Drain already over: the turn is being force-cancelled anyway.
		capped = drainToolFloor
	}
	return min(d, capped), true
}

// addDrainHint appends DrainToolHint to the result the model will see. The
// result is copied: tools may hand out shared (cached) results.
func addDrainHint(res *ExecutionResult, limit time.Duration) {
	hint := fmt.Sprintf(DrainToolHint, limit.Round(100*time.Millisecond))
	if res.Result == nil {
		res.Result = &ToolResult{Success: res.Error == nil, Content: hint}
		return
	}
	r := *res.Result
	if r.Content != "" {
		r.Content += "\n\n"
	}
	r.Content += hint
	res.Result = &r
}

// executeParallel executes multiple tools in parallel with controlled
// concurrency. conduit-31jg.58: calls touching the same file, at least one of
// them writing it, run sequentially in model order; all others stay parallel.
func (e *ExecutionEngine) executeParallel(ctx context.Context, calls []ai.ToolCall) []*ExecutionResult {
	results := make([]*ExecutionResult, len(calls))

	// Use worker pool for controlled concurrency. A slot is held per call,
	// not per group, so a serialized group never pins more than one slot.
	limit := e.maxParallel
	if limit < 1 {
		limit = 1
	}
	semaphore := make(chan struct{}, limit)
	var wg sync.WaitGroup

	runOne := func(idx int) {
		toolCall := calls[idx]
		semaphore <- struct{}{}
		defer func() { <-semaphore }()

		// conduit-31jg.47: never let one call's panic kill the process.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[ExecutionEngine] PANIC in parallel worker for tool %q: %v\n%s", toolCall.Name, rec, debug.Stack())
				results[idx] = pipelinePanicResult(toolCall, time.Now(), rec)
			}
		}()

		results[idx] = e.executeSingle(ctx, toolCall)
	}

	for _, group := range e.pathGroups(calls) {
		wg.Add(1)
		go func(group []int) {
			defer wg.Done()
			for _, idx := range group {
				runOne(idx)
			}
		}(group)
	}

	wg.Wait()
	return results
}

// pathGroups partitions call indices into groups that may run concurrently
// with one another; each group runs in model order. conduit-31jg.58.
//
// Read is included: a Read of a file that is also written in the same turn is
// ordered relative to that write as the model issued them, so it sees the
// state the model expects rather than a stale or half-written file. A path
// that is only read is not serialized: reads cannot clobber each other.
func (e *ExecutionEngine) pathGroups(calls []ai.ToolCall) [][]int {
	keys := make([]string, len(calls))
	written := map[string]bool{}
	for i, c := range calls {
		key, write := e.fileAccess(c)
		keys[i] = key
		if key != "" && write {
			written[key] = true
		}
	}
	var groups [][]int
	byKey := map[string]int{} // canonical path -> index into groups
	for i, key := range keys {
		if key == "" || !written[key] {
			groups = append(groups, []int{i})
			continue
		}
		if g, ok := byKey[key]; ok {
			groups[g] = append(groups[g], i)
			continue
		}
		byKey[key] = len(groups)
		groups = append(groups, []int{i})
	}
	return groups
}

// fileAccess returns the canonical local path a call reads or writes and
// whether it writes it; key is "" for calls that are not file tools.
// Bash is not classified: its file effects can't be known from its args.
// conduit-31jg.58.
func (e *ExecutionEngine) fileAccess(call ai.ToolCall) (key string, write bool) {
	str := func(k string) string { s, _ := call.Args[k].(string); return s }
	var p string
	workspaceRelative := true
	switch call.Name {
	case "Write":
		p, write = str("path"), true
	case "Edit":
		p, write = str("path"), true
		if p == "" {
			p = str("file_path") // EditTool's alternative param name
		}
	case "Read":
		p = str("path")
	case "Ssh": // scp moves a local file; local_path is used as given
		workspaceRelative = false
		switch str("action") {
		case "scp_download":
			p, write = str("local_path"), true
		case "scp_upload":
			p = str("local_path")
		}
	}
	if p == "" {
		return "", false
	}
	if workspaceRelative && !filepath.IsAbs(p) {
		if r, ok := e.registry.(interface{ workspacePathBase() string }); ok {
			if base := r.workspacePathBase(); base != "" {
				p = filepath.Join(base, p)
			}
		}
	}
	if real, err := sandbox.Canonicalize(p); err == nil {
		return real, write
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs, write
	}
	return filepath.Clean(p), write
}

// workspacePathBase is the directory Read/Write/Edit resolve relative paths
// against (see their resolvePath). conduit-31jg.58.
func (r *Registry) workspacePathBase() string {
	if r.services != nil && r.services.ConfigMgr != nil {
		if d := r.services.ConfigMgr.Workspace.ContextDir; d != "" {
			return d
		}
		if d := r.services.ConfigMgr.Tools.Sandbox.WorkspaceDir; d != "" {
			return d
		}
	}
	return r.sandboxCfg.WorkspaceDir
}

// turnState is the tool loop's per-turn state (conduit-31jg.37). One is built
// per HandleToolCallFlow call and advanced round by round; nothing in it
// outlives the turn. Before this bead every round was a recursive call that
// re-copied the whole history.
type turnState struct {
	provider ai.Provider
	// model, tools and maxTokens are copied into every round's request (the
	// loop never forwards any other GenerateRequest field).
	model     string
	tools     []ai.Tool
	maxTokens int

	// history is the turn's one conversation slice, appended to in place.
	// Every request gets a capacity-capped view of it (history[:n:n]), so
	// neither the loop's later appends nor anyone appending to a request's
	// Messages can write into a slot an earlier request still shows.
	history []ai.ChatMessage
	// resp is the reply whose tool calls the current round executes.
	resp       *ai.GenerateResponse
	depth      int
	chainStart time.Time
	budget     *turnBudget // trackers (budget.chain), usage, extensions, refocus one-shot
}

// request builds the round's provider request over the current history.
func (ts *turnState) request() *ai.GenerateRequest {
	n := len(ts.history)
	return &ai.GenerateRequest{
		Messages:  ts.history[:n:n],
		Model:     ts.model,
		Tools:     ts.tools,
		MaxTokens: ts.maxTokens,
	}
}

// advance carries the round's request history into the next round and makes
// resp the reply to execute. guidanceAt is the index of this round's
// ephemeral guidance message (-1 when none): it existed for this round trip
// only and is dropped (conduit-8ba7, conduit-31jg.13). The request itself is
// never modified: its pointer may already be recorded by mocks/telemetry.
func (ts *turnState) advance(req *ai.GenerateRequest, guidanceAt int, resp *ai.GenerateResponse) {
	msgs := req.Messages
	switch {
	case guidanceAt >= 0:
		// The capped prefix makes append copy, so the guidance slot the
		// provider saw is never overwritten.
		ts.history = append(msgs[:guidanceAt:guidanceAt], msgs[guidanceAt+1:]...)
	case len(msgs) != len(ts.history):
		// Auto-continue appended to req.Messages (a fresh array). Cap it:
		// a view the provider saw may extend past its current length.
		ts.history = msgs[:len(msgs):len(msgs)]
	default:
		// Same contents as ts.history; keep its spare capacity.
	}
	ts.resp = resp
	ts.depth++
}

// HandleToolCallFlow runs the tool loop for one turn: execute the reply's
// tool calls, send the results back, and repeat until the model answers
// without tool calls, the chain hits maxChains, or the turn window closes.
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
	// conduit-31jg.13: fresh trackers per turn, on the turn budget and on
	// ctx into executeSingle.
	tb := newTurnBudget(time.Now())
	tb.chain = e.newChainState(ctx)
	// conduit-31jg.15: the router's first round trip (with its own guard
	// retries / auto-continues folded in) opens the turn's usage.
	tb.usage.Add(initialResp.Usage)

	// The caller's slice is copied once, with room to grow, so appends
	// never write into the caller's backing array.
	history := make([]ai.ChatMessage, len(initialReq.Messages), len(initialReq.Messages)+16)
	copy(history, initialReq.Messages)
	ts := &turnState{
		provider:   provider,
		model:      initialReq.Model,
		tools:      initialReq.Tools,
		maxTokens:  initialReq.MaxTokens,
		history:    history,
		resp:       initialResp,
		chainStart: time.Now(),
		budget:     tb,
	}
	return e.runToolLoop(withChainState(ctx, tb.chain), ts)
}

// runToolLoop executes rounds until the chain ends. conduit-31jg.37.
func (e *ExecutionEngine) runToolLoop(ctx context.Context, ts *turnState) (*ConversationResponse, error) {
	tb := ts.budget
	for {
		depth := ts.depth

		// conduit-1z6d + adaptive extension: enforce the turn window.
		// Productive coding chains get bounded extensions (each announced);
		// everything else stops with a user-visible timeout — never a silent
		// end.
		if stop := e.checkTurnWindow(ctx, ts.chainStart, tb, depth); stop != nil {
			if stop.Usage == nil {
				stop.Usage = tb.usageSnapshot() // conduit-31jg.15
			}
			return stop, nil
		}

		// Prevent infinite tool chains
		if depth >= e.maxChains {
			return e.chainLimitResponse(ts), nil
		}

		refocusMessage := e.refocusMessage(ts)

		// This round: the reply's tool calls, then their results.
		ts.history = append(ts.history, ai.ChatMessage{
			Role:      "assistant",
			Content:   ts.resp.Content,
			ToolCalls: ts.resp.ToolCalls,
		})
		toolResults, err := e.ExecuteToolCalls(ctx, ts.resp.ToolCalls)
		if err != nil {
			return nil, fmt.Errorf("tool execution failed: %w", err)
		}

		// Turn budget: record this round's activity for extension eligibility.
		anySuccess := false
		for _, r := range toolResults {
			if r != nil && r.Error == nil && r.Result != nil && r.Result.Success {
				anySuccess = true
				break
			}
		}
		tb.markRound(ts.resp.ToolCalls, anySuccess, time.Now())

		for _, result := range toolResults {
			ts.history = append(ts.history, ai.ChatMessage{
				Role:       "tool",
				Content:    e.formatToolResultForAI(result),
				ToolCallID: result.ToolCall.ID,
				// conduit-31jg.45: tell the model the call failed (tool_result.is_error).
				IsError: result.Error != nil || (result.Result != nil && !result.Result.Success),
			})
		}

		// conduit-31jg.13: failure-pivot and circular-pattern guidance, once
		// per trigger, from this turn's trackers only. Sent as a USER-role
		// message after the tool results — not system-role — so providers
		// that hoist system messages (anthropic.go, openai.go) don't rewrite
		// the system prefix and bust the prompt cache. The Anthropic
		// converter puts it in the same user message as the tool_results,
		// after them (conduit-31jg.45). conduit-31jg.14: the one-per-chain
		// conduit-8ba7 progress reminder rides in this same message. It is
		// dropped again before the next round (turnState.advance).
		guidanceAt := -1
		if guidance := tb.chain.takeGuidance(refocusMessage); guidance != "" {
			log.Printf("[ExecutionEngine] Injecting tool-loop guidance at depth %d (conduit-31jg.13)", depth)
			guidanceAt = len(ts.history)
			ts.history = append(ts.history, ai.ChatMessage{Role: "user", Content: guidance})
		}

		req := ts.request()
		resp, err := e.roundTrip(ctx, ts.provider, req, depth, tb)
		if err != nil {
			return nil, err
		}

		if len(resp.ToolCalls) == 0 {
			// conduit-31jg.15: usage is the whole turn, not the last two calls.
			return &ConversationResponse{
				Content:     resp.Content,
				Usage:       tb.usageSnapshot(),
				Steps:       2 + depth, // Initial + final + any chained steps
				ToolResults: toolResults,
				ChainDepth:  depth,
			}, nil
		}
		ts.advance(req, guidanceAt, resp)
	}
}

// roundTrip sends one round's request: the provider call, the EmptyGuard
// retry/failover and the length auto-continue. Every billed call's usage is
// added to tb exactly once (GuardEmptyResponse and ContinueLengthTruncated
// each return the sum of the calls they made).
func (e *ExecutionEngine) roundTrip(ctx context.Context, provider ai.Provider, req *ai.GenerateRequest, depth int, tb *turnBudget) (*ai.GenerateResponse, error) {
	stopThinking := startThinkingIndicator(ctx, depth)
	rtStart := time.Now()
	resp, err := provider.GenerateResponse(ctx, req)
	stopThinking()
	if err != nil {
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// conduit-18vj: raw-empty round trips after tool execution were the proven
	// dead-turn mechanism (2026-09-03) — retry once, then a visible fallback.
	label := fmt.Sprintf("depth%d", depth)
	resp, err = ai.GuardEmptyResponse(ctx, provider, req, resp, err, label)
	if err != nil {
		return nil, fmt.Errorf("AI response after tool execution failed: %w", err)
	}

	// conduit-1z6d: per-round-trip instrumentation — dead turns diagnosable
	// from the journal alone.
	log.Printf("[RoundTrip] phase=post-tools depth=%d model=%q duration=%s prompt_tokens=%d completion_tokens=%d content_bytes=%d tool_calls=%d",
		depth, req.Model, time.Since(rtStart).Round(time.Millisecond),
		resp.Usage.PromptTokens, resp.Usage.CompletionTokens,
		len(resp.Content), len(resp.ToolCalls))

	// bd-1k3o / conduit-31jg.51: length-truncation guard, shared with the
	// router's first round trip. A max_tokens-severed fragment is continued
	// (at most twice) instead of delivered as the answer; req.Messages grows
	// by each fragment + "continue" pair. When the continuation ends in tool
	// calls, Content stays the last fragment only — the earlier ones are
	// already in req.Messages, which becomes the next round's history. The
	// helper folds every continuation's usage into resp.Usage.
	resp = ai.ContinueLengthTruncated(ctx, provider, req, resp, label)
	tb.usage.Add(resp.Usage) // conduit-31jg.15: this round's calls, exactly once
	return resp, nil
}

// chainLimitResponse is the reply when the chain reaches maxChains.
func (e *ExecutionEngine) chainLimitResponse(ts *turnState) *ConversationResponse {
	depth := ts.depth
	log.Printf("Tool chain depth limit reached: %d/%d", depth, e.maxChains)
	limitMessage := fmt.Sprintf(
		"%s\n\n**Tool chain limit reached (%d steps).** "+
			"I've completed %d tool operations but reached the maximum allowed chain length. "+
			"This prevents runaway tool usage while still allowing complex workflows. "+
			"If you need to continue, you can:\n"+
			"- Ask me to pick up where I left off with a more focused approach\n"+
			"- Break the task into smaller steps\n"+
			"- Increase the `max_tool_chains` setting in config.json if this limit is too restrictive",
		ts.resp.Content, e.maxChains, depth,
	)
	return &ConversationResponse{
		Content:    limitMessage,
		Usage:      ts.budget.usageSnapshot(), // conduit-31jg.15
		Steps:      depth + 1,
		ChainDepth: depth,
	}
}

// refocusDepthThreshold is the depth of the one mid-chain progress reminder.
const refocusDepthThreshold = 20

// refocusMessage returns the conduit-8ba7 mid-chain progress reminder for
// this round, or "". The old every-10-depth verbatim goal reminder is gone;
// deep chains get exactly one progress-aware user-role guidance message at
// the first depth >= 20, and depth milestones 30/40/50 emit chain_depth
// telemetry (log only, no injection).
func (e *ExecutionEngine) refocusMessage(ts *turnState) string {
	depth, tb := ts.depth, ts.budget
	var msg string
	if depth >= refocusDepthThreshold && !tb.injected {
		if originalGoal := e.extractOriginalGoal(ts.history); originalGoal != "" {
			msg = fmt.Sprintf("%s%d of max %d. Original request: %s",
				progressReminderMarker, depth, e.maxChains, originalGoal)
			tb.injected = true
			log.Printf("[ExecutionEngine] operation=refocus_inject depth=%d max=%d goal=%q (conduit-8ba7)", depth, e.maxChains, originalGoal)
		}
	}
	switch depth {
	case 30, 40, 50:
		log.Printf("[ExecutionEngine] operation=chain_depth milestone=%d max=%d (conduit-8ba7)", depth, e.maxChains)
	}
	return msg
}

// maybeSendExtensionNotice delivers the extension announcement via the
// StatusUpdate tool so the user sees the turn is still alive. Best-effort:
// a failed notice never aborts the chain.
func (e *ExecutionEngine) maybeSendExtensionNotice(ctx context.Context, tb *turnBudget) {
	msg := extensionStatusMessage(tb, time.Now())
	if e.registry == nil {
		log.Printf("[TurnBudget] no registry; extension notice not sent: %s", msg)
		return
	}
	if _, err := e.registry.ExecuteTool(ctx, "StatusUpdate", map[string]interface{}{"message": msg}); err != nil {
		log.Printf("[TurnBudget] extension notice send failed (continuing): %v", err)
	}
}

// checkTurnWindow enforces the adaptive cap: grant an extension when eligible,
// otherwise terminate with the user-visible timeout message. Returns non-nil
// ConversationResponse when the chain must stop.
func (e *ExecutionEngine) checkTurnWindow(ctx context.Context, chainStart time.Time, tb *turnBudget, depth int) *ConversationResponse {
	now := time.Now()
	if _, pastWindow := watchdogTimeoutResponse(chainStart, tb.extensions); pastWindow {
		if ok, reason := tb.extensionEligible(now); ok {
			tb.extensions++
			log.Printf("[TurnBudget] extension %d/%d granted at depth %d (elapsed %s, %s)",
				tb.extensions, turnMaxExtensions, depth, now.Sub(chainStart).Round(time.Second), reason)
			e.maybeSendExtensionNotice(ctx, tb)
		} else {
			log.Printf("[TurnBudget] chain cap exceeded at depth %d (elapsed %s, extensions used %d/%d, %s) — aborting with visible timeout (conduit-1z6d)",
				depth, now.Sub(chainStart).Round(time.Second), tb.extensions, turnMaxExtensions, reason)
			return &ConversationResponse{
				Content:    watchdogTerminalMessage(reason),
				Steps:      depth + 1,
				ChainDepth: depth,
			}
		}
	}
	return nil
}

// formatToolResultForAI formats tool results for AI consumption
func (e *ExecutionEngine) formatToolResultForAI(result *ExecutionResult) string {
	if result.Error != nil {
		msg := fmt.Sprintf("Tool '%s' failed: %s", result.ToolCall.Name, result.Error.Error())
		// conduit-31jg.47: surface output a tool returned alongside its error.
		if result.Result != nil {
			if out := strings.TrimSpace(result.Result.Content); out != "" && !strings.Contains(msg, out) {
				msg += "\nOutput:\n" + e.truncateForModel(out)
			}
		}
		return msg
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

		// conduit-31jg.10: surface the tool's output (e.g. compiler/test stderr
		// from a non-zero Bash exit) so the model isn't blind to why it failed.
		// ErrorDetails.Context["output"] is intentionally not rendered, so the
		// output appears exactly once.
		if out := strings.TrimSpace(result.Result.Content); out != "" && out != strings.TrimSpace(result.Result.Error) {
			maxChars := e.maxResultChars
			if maxChars <= 0 {
				maxChars = DefaultMaxToolResultChars
			}
			if len(out) > maxChars {
				out = e.smartTruncate(out, maxChars)
			}
			msg += "\nOutput:\n" + out
		}

		return msg
	}

	// conduit-31jg.39: Data is appended only for tools that opt in (their
	// payload lives in Data) or when Content is empty. Appending it to every
	// result doubled Glob's file list and re-sent Chain step outputs.
	content := result.Result.Content
	if len(result.Result.Data) > 0 && (strings.TrimSpace(content) == "" || e.toolWantsDataInOutput(result.ToolCall.Name)) {
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

// truncateForModel applies the result budget to s (conduit-31jg.47).
func (e *ExecutionEngine) truncateForModel(s string) string {
	maxChars := e.maxResultChars
	if maxChars <= 0 {
		maxChars = DefaultMaxToolResultChars
	}
	if len(s) > maxChars {
		return e.smartTruncate(s, maxChars)
	}
	return s
}

// toolWantsDataInOutput reports whether the named tool opted into having
// ToolResult.Data rendered for the model (Registry.IncludeDataInModelOutput).
func (e *ExecutionEngine) toolWantsDataInOutput(name string) bool {
	p, ok := e.registry.(interface{ IncludeDataInModelOutput(name string) bool })
	return ok && p.IncludeDataInModelOutput(name)
}

// headTailRunes keeps headSize bytes from the start and tailSize from the
// end of s with marker between, never splitting a UTF-8 rune (conduit-31jg.39).
func headTailRunes(s string, headSize, tailSize int, marker string) string {
	if headSize < 0 {
		headSize = 0
	}
	if tailSize < 0 {
		tailSize = 0
	}
	if headSize+tailSize >= len(s) {
		return s
	}
	for headSize > 0 && !utf8.RuneStart(s[headSize]) {
		headSize--
	}
	tailStart := len(s) - tailSize
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	return s[:headSize] + marker + s[tailStart:]
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
		return headTailRunes(content, headSize, tailSize,
			fmt.Sprintf("\n\n...(truncated, showing %d of %d chars)...\n\n", maxChars, len(content)))
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
		return headTailRunes(finalContent, headSize, tailSize,
			fmt.Sprintf("\n\n...(final truncation, showing %d of %d chars)...\n\n", maxChars, len(finalContent)))
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

// progressReminderMarker is the stable prefix of the conduit-8ba7 mid-chain
// progress reminder (tests match on it).
const progressReminderMarker = "Turn progress: depth "
