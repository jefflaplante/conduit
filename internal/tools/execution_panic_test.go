package tools

import (
	"context"
	"errors"
	"testing"

	"conduit/internal/ai"
	"conduit/internal/tools/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// conduit-31jg.47: panics in middleware, hooks and event callbacks run
// outside Registry.ExecuteTool's recover; they must not crash the process.

type panicMiddleware struct{ before, after bool }

func (p *panicMiddleware) BeforeExecution(context.Context, *ai.ToolCall) error {
	if p.before {
		panic("before boom")
	}
	return nil
}

func (p *panicMiddleware) AfterExecution(context.Context, *ai.ToolCall, *ExecutionResult) error {
	if p.after {
		panic("after boom")
	}
	return nil
}

func newPanicTestEngine(t *testing.T) *ExecutionEngine {
	t.Helper()
	reg := NewMockRegistry()
	reg.AddTool(&MockTool{name: "ok_tool", executeFunc: func(context.Context, map[string]interface{}) (*ToolResult, error) {
		return &ToolResult{Success: true, Content: "tool ran"}, nil
	}})
	return NewExecutionEngine(reg, 2, 0, 5)
}

func parallelCalls() []ai.ToolCall {
	return []ai.ToolCall{
		{ID: "1", Name: "ok_tool", Args: map[string]interface{}{}},
		{ID: "2", Name: "ok_tool", Args: map[string]interface{}{}},
	}
}

func TestExecuteParallel_PanicInBeforeMiddlewareYieldsErrorResult(t *testing.T) {
	e := newPanicTestEngine(t)
	e.AddMiddleware(&panicMiddleware{before: true})

	results, err := e.ExecuteToolCalls(context.Background(), parallelCalls())
	require.NoError(t, err)
	require.Len(t, results, 2)
	for _, r := range results {
		require.NotNil(t, r)
		require.Error(t, r.Error)
		assert.Contains(t, r.Error.Error(), "panic")
		require.NotNil(t, r.Result)
		assert.False(t, r.Result.Success)
		assert.Contains(t, e.formatToolResultForAI(r), "panic")
	}
}

func TestExecuteSingle_PanicInAfterHooksKeepsToolResult(t *testing.T) {
	for name, setup := range map[string]func(e *ExecutionEngine){
		"after middleware": func(e *ExecutionEngine) { e.AddMiddleware(&panicMiddleware{after: true}) },
		"after exec hook": func(e *ExecutionEngine) {
			e.SetAfterExecutionHook(func(context.Context, string, *ExecutionResult) { panic("hook boom") })
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newPanicTestEngine(t)
			setup(e)
			results, err := e.ExecuteToolCalls(context.Background(), parallelCalls())
			require.NoError(t, err)
			for _, r := range results {
				require.NotNil(t, r)
				// The tool already ran; reporting failure would invite a
				// retry of a side-effecting call.
				assert.NoError(t, r.Error)
				require.NotNil(t, r.Result)
				assert.Equal(t, "tool ran", r.Result.Content)
			}
		})
	}
}

func TestExecuteSingle_PanicInEventCallback(t *testing.T) {
	e := newPanicTestEngine(t)
	ctx := WithToolEventCallback(context.Background(), func(ev ToolEventInfo) {
		if ev.EventType == "start" {
			panic("callback boom")
		}
	})
	results, err := e.ExecuteToolCalls(ctx, parallelCalls())
	require.NoError(t, err)
	for _, r := range results {
		require.NotNil(t, r)
		require.Error(t, r.Error)
		assert.Contains(t, r.Error.Error(), "panic")
	}
}

// conduit-31jg.47: result Content survives a (result, error) return.
type resultAndErrorTool struct{}

func (resultAndErrorTool) Name() string        { return "both" }
func (resultAndErrorTool) Description() string { return "x" }
func (resultAndErrorTool) Parameters() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (resultAndErrorTool) Execute(context.Context, map[string]interface{}) (*types.ToolResult, error) {
	return &types.ToolResult{Success: false, Content: "partial output line"}, errors.New("exit 2")
}

func TestRegistryExecuteTool_KeepsContentWithError(t *testing.T) {
	registry, _ := setupTestRegistry(t, "", "")
	registry.mu.Lock()
	registry.tools["both"] = resultAndErrorTool{}
	registry.enabledTools[normalizeToolName("both")] = true
	registry.mu.Unlock()

	res, err := registry.ExecuteTool(context.Background(), "both", nil)
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "partial output line", res.Content)
	assert.Contains(t, res.Error, "exit 2")

	e := NewExecutionEngine(registry, 1, 0, 1)
	out := e.formatToolResultForAI(&ExecutionResult{ToolCall: &ai.ToolCall{Name: "both"}, Result: res, Error: err})
	assert.Contains(t, out, "exit 2")
	assert.Contains(t, out, "partial output line")
}
