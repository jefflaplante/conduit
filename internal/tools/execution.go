package tools

import (
	"context"
	"time"

	"conduit/internal/ai"
	"conduit/internal/tools/debuglog"
)

// ToolRegistry interface for tool execution
type ToolRegistry interface {
	ExecuteTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, error)
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
